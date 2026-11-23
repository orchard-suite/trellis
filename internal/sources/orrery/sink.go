package orrery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/trellis/internal/board"
	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/meter"
	"github.com/orchard-suite/trellis/internal/pack"
	"github.com/orchard-suite/trellis/internal/trend"
)

// Scope is the hub scope every finding and verdict is written under;
// PackScope is the packs'.
const (
	Scope     = "trellis"
	PackScope = "trellis-packs"
)

// Write names the blueprints trellis writes into, by their role in
// blueprints.yaml. An empty name keeps the role's id from blueprints.yaml,
// except for the pillar, the rule list, and the waiver list, which are
// written only into a blueprint the estate names. Verdict empty with
// Verdicts false writes none.
type Write struct {
	FindingSecurity string `yaml:"finding_security"`
	FindingPlatform string `yaml:"finding_platform"`
	Verdict         string `yaml:"verdict"`
	Pack            string `yaml:"pack"`
	// Pillar is the blueprint a finding's platform pillar is an entity of;
	// empty links no pillar.
	Pillar string `yaml:"pillar"`
	// Rule is the blueprint each rule the board judged with is written
	// into, hand-written or brought by a source; empty writes none.
	Rule string `yaml:"rule"`
	// Waiver is the blueprint each waiver the board weighed is written
	// into, with whether it counts and why; empty writes none.
	Waiver string `yaml:"waiver"`
	// Verdicts writes one verdict per subject and tiered pack; on unless false.
	Verdicts *bool `yaml:"verdicts"`
}

// roles are the ids blueprints.yaml uses for what trellis writes.
const (
	roleSecurity = "finding_security"
	rolePlatform = "finding_platform"
	roleVerdict  = "verdict"
	rolePack     = "pack"
	rolePillar   = "pillar"
	roleRule     = "rule"
	roleWaiver   = "waiver"
)

func (w Write) named() map[string]string {
	or := func(v, role string) string {
		if v == "" {
			return role
		}
		return v
	}
	return map[string]string{
		roleSecurity: or(w.FindingSecurity, roleSecurity),
		rolePlatform: or(w.FindingPlatform, rolePlatform),
		roleVerdict:  or(w.Verdict, roleVerdict),
		rolePack:     or(w.Pack, rolePack),
		rolePillar:   w.Pillar,
		roleRule:     w.Rule,
		roleWaiver:   w.Waiver,
	}
}

// Adopted is one pack the board judges with, as the catalog records it.
type Adopted struct {
	ID, Title, About, Version, Family, Status, Source string
	Rules                                             int
	Tiers                                             []string
	// Tiered are the pack's rules that name a tier, in the pack's order.
	Tiered []*pack.Rule
}

// Adopt is each pack of a pass's set as the catalog records it.
func Adopt(set *pack.Set) []Adopted {
	if set == nil {
		return nil
	}
	out := make([]Adopted, 0, len(set.Packs))
	for _, p := range set.Packs {
		var tiered []*pack.Rule
		for _, r := range p.Rules {
			if r.Tier != "" {
				tiered = append(tiered, r)
			}
		}
		out = append(out, Adopted{ID: p.ID, Title: p.Title, About: p.About, Version: p.Version, Family: p.Family, Status: string(p.State), Source: p.Source, Rules: len(p.Rules), Tiers: p.Tiers, Tiered: tiered})
	}
	return out
}

// Sink writes each pass's open findings, verdicts, and weighed waivers to
// the hub as one snapshot, so a finding that clears, or that a waiver now
// covers, leaves the catalog, and the packs and rules the pass judged with
// as another. Draft packs write no findings or verdicts. The packs come
// with each pass, since a source may bring its own.
type Sink struct {
	client   *Client
	board    string
	names    map[string]string
	verdicts bool
	// roles are the blueprints trellis writes, under the estate's names,
	// without the links; inverse is, for each one that links, the title its
	// links read from the other side.
	roles   []map[string]any
	inverse map[string]string
	// targets says which lists a finding, verdict, or waiver may link to
	// now: the ones the estate maps that the hub has.
	targets func() []string
	// blueprints is the offer the entities are written to, and linked the
	// relation each list is linked through in it. taken is the fingerprint
	// of the offer the hub last took, empty until it took one.
	blueprints []map[string]any
	linked     map[string]string
	taken      string
	// scorecards, when set, puts a scorecard per tiered pack; put is the
	// hash of the body last put for each id, and refused the ids whose
	// refusal was already said.
	scorecards *Scorecards
	put        map[string]string
	refused    map[string]bool
	metrics    *meter.Metrics
	log        *slog.Logger
}

// NewSink writes with a source token registered for what Write names.
// blueprintsYAML is what trellis offers, by role; Write renames each role,
// and a role written only when named is not offered unless it is. linked
// says, each pass, which lists the estate maps to subjects that the hub
// has: each finding, verdict, and waiver gains a relation to each, named
// after it, so one on a mapped entity links to it. A list the hub lacks is
// never among them, since the hub refuses a relation to a list it does not
// have; it is linked on the pass that first finds it. A relation to another
// role targets the blueprint that role is written into, and is dropped when
// that role is not. cards, when set, puts a scorecard per tiered pack.
func NewSink(client *Client, boardURL string, blueprintsYAML []byte, write Write, cards *Scorecards, linked func() []string, metrics *meter.Metrics, log *slog.Logger) (*Sink, error) {
	var file struct {
		Blueprints []map[string]any `yaml:"blueprints"`
	}
	if err := yaml.Unmarshal(blueprintsYAML, &file); err != nil {
		return nil, fmt.Errorf("blueprints.yaml: %w", err)
	}
	names := write.named()
	verdicts := write.Verdicts == nil || *write.Verdicts
	titles := map[string]string{roleSecurity: "Security findings", rolePlatform: "Platform findings", roleVerdict: "Verdicts", roleWaiver: "Waivers"}
	inverse := map[string]string{}
	var roles []map[string]any
	for _, bp := range file.Blueprints {
		role, _ := bp["id"].(string)
		if (role == roleVerdict && !verdicts) || names[role] == "" {
			continue
		}
		out := map[string]any{}
		for k, v := range bp {
			out[k] = v
		}
		out["id"] = names[role]
		rels := map[string]any{}
		if old, ok := bp["relations"].(map[string]any); ok {
			for k, v := range old {
				d := map[string]any{}
				for dk, dv := range v.(map[string]any) {
					d[dk] = dv
				}
				if target, _ := d["target"].(string); target != "" {
					if _, isRole := names[target]; isRole {
						if names[target] == "" {
							continue
						}
						d["target"] = names[target]
					}
				}
				rels[k] = d
			}
		}
		out["relations"] = rels
		if titles[role] != "" {
			inverse[names[role]] = titles[role]
		}
		roles = append(roles, out)
	}
	if err := cards.Check(); err != nil {
		return nil, err
	}
	if linked == nil {
		linked = func() []string { return nil }
	}
	s := &Sink{client: client, board: strings.TrimRight(boardURL, "/"), names: names, verdicts: verdicts, roles: roles, inverse: inverse, targets: linked, scorecards: cards, put: map[string]string{}, refused: map[string]bool{}, metrics: metrics, log: log}
	s.blueprints, s.linked = s.shape(linked())
	return s, nil
}

// shape is the offer for the lists a finding may link to: each role's
// blueprint, and on each one that links a relation to each list, named
// after it, with the relation each list is linked through.
func (s *Sink) shape(targets []string) ([]map[string]any, map[string]string) {
	links := map[string]string{}
	offer := make([]map[string]any, 0, len(s.roles))
	for _, role := range s.roles {
		out := map[string]any{}
		for k, v := range role {
			out[k] = v
		}
		rels := map[string]any{}
		for k, v := range role["relations"].(map[string]any) {
			rels[k] = v
		}
		if title := s.inverse[role["id"].(string)]; title != "" {
			for _, target := range targets {
				name := relationName(target)
				if _, taken := rels[name]; !taken {
					rels[name] = map[string]any{"target": target, "many": false, "title": target, "inverseTitle": title}
				}
				links[target] = name
			}
		}
		delete(out, "relations")
		if len(rels) > 0 {
			out["relations"] = rels
		}
		offer = append(offer, out)
	}
	return offer, links
}

// offer brings the blueprints to the hub when they are not the ones it
// last took: on the first pass, and on a pass that finds a list a finding
// may now link to. It says whether the pass can be written. A refused
// offer is made again next pass; until the hub has taken one nothing is
// written, and after that the pass is written to the offer it last took.
func (s *Sink) offer(ctx context.Context) bool {
	blueprints, links := s.shape(s.targets())
	raw, err := json.Marshal(blueprints)
	if err != nil {
		s.log.ErrorContext(ctx, "orrery blueprint offer failed", "error", err)
		return s.taken != ""
	}
	sum := sha256.Sum256(raw)
	key := hex.EncodeToString(sum[:])
	if key == s.taken {
		return true
	}
	if err := s.client.Offer(ctx, blueprints); err != nil {
		s.log.ErrorContext(ctx, "orrery blueprint offer failed", "error", err, "written_to_the_last_offer", s.taken != "")
		return s.taken != ""
	}
	s.blueprints, s.linked, s.taken = blueprints, links, key
	return true
}

// relationName is the relation a finding links an entity of the blueprint
// through: the blueprint's id, which the hub accepts as a field name.
func relationName(blueprint string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return '_'
	}, blueprint)
}

func (s *Sink) relate(rels map[string]any, ref *estate.Ref) {
	if ref == nil {
		return
	}
	if name, ok := s.linked[ref.Blueprint]; ok {
		// The hub reads a bare target up to its first colon as a
		// blueprint, which an image key with a tag would trip.
		rels[name] = ref.Blueprint + ":" + ref.Key
	}
}

// Entities shapes a snapshot's open findings, then its verdicts, then the
// waivers it weighed. Only a finding that counts is written: a waived,
// drafted, or announced one stays on the board.
func (s *Sink) Entities(snap *board.Snapshot) []Entity {
	families := map[string]string{}
	for _, p := range Adopt(snap.Packs) {
		families[p.ID] = p.Family
	}
	open := snap.Evaluation.Open()
	out := make([]Entity, 0, len(open))
	for _, f := range open {
		blueprint := s.names[rolePlatform]
		if families[f.Pack] == "security" {
			blueprint = s.names[roleSecurity]
		}
		first := f.FirstSeen
		if first.IsZero() {
			first = snap.RefreshedAt
		}
		fields := map[string]any{
			"rule": f.Rule, "pack": f.Pack, "standard": f.Standard, "severity": string(f.Severity),
			"kind": f.Kind, "subject": f.Subject, "detail": f.Detail, "fix": f.Fix,
			"first_seen": first.UTC().Format(time.RFC3339),
			"days_open":  int(snap.RefreshedAt.Sub(first).Hours() / 24),
			"overdue":    f.Overdue, "carried": f.Carried,
			"board": s.board + "/?view=findings&open=" + url.QueryEscape("f:"+f.Key()),
		}
		if f.Owner != "" {
			fields["owner"] = f.Owner
		}
		if f.Due != nil {
			fields["due"] = f.Due.UTC().Format(time.RFC3339)
		}
		if !f.AsOf.IsZero() {
			fields["as_of"] = dayOf(f.AsOf).Format(time.RFC3339)
		}
		if t := snap.Trends; t != nil {
			if ft, ok := t.Findings[trend.FindingKey(f.Rule, f.Subject)]; ok {
				fields["reopened"] = ft.Reopened
				if ft.Cause != "" {
					fields["cause"] = ft.Cause
				}
			}
		}
		if len(f.Evidence) > 0 {
			fields["evidence"] = Evidence(f.Evidence)
		}
		if len(f.Controls) > 0 {
			fields["controls"] = f.Controls
		}
		if f.Tier != "" {
			fields["tier"] = f.Tier
		}
		rels := map[string]any{}
		s.relate(rels, f.Ref)
		if p := s.names[rolePillar]; p != "" && f.Pillar != "" {
			rels[rolePillar] = p + ":" + f.Pillar
		}
		out = append(out, Entity{Blueprint: blueprint, Key: f.Key(), Title: f.Title + ": " + f.Rule, Fields: fields, Relations: rels})
	}
	out = append(out, s.Verdicts(snap)...)
	return append(out, s.Waivers(snap)...)
}

// Verdicts are one entity per subject and tiered pack it was judged by: the
// tier reached, its rank, and the pack's open findings and unchecked rules
// on it. A draft pack writes none.
func (s *Sink) Verdicts(snap *board.Snapshot) []Entity {
	if !s.verdicts {
		return nil
	}
	tiers := map[string][]string{}
	draft := map[string]bool{}
	for _, p := range Adopt(snap.Packs) {
		tiers[p.ID] = p.Tiers
		draft[p.ID] = p.Status == "draft"
	}
	var out []Entity
	for _, sv := range snap.Evaluation.Subjects {
		packs := make([]string, 0, len(sv.Tiers))
		for id := range sv.Tiers {
			if !draft[id] {
				packs = append(packs, id)
			}
		}
		sort.Strings(packs)
		for _, id := range packs {
			tier := sv.Tiers[id]
			rank := 0
			for i, t := range tiers[id] {
				if t == tier {
					rank = i + 1
				}
			}
			findings, unchecked := 0, 0
			for _, f := range sv.Findings {
				if f.Pack == id && f.State == judge.Open {
					findings++
				}
			}
			for _, u := range sv.Unchecked {
				if u.Pack == id {
					unchecked++
				}
			}
			rels := map[string]any{}
			s.relate(rels, sv.Ref)
			title := sv.Title
			if title == "" {
				title = sv.Key
			}
			link := s.board + "/?view=subjects&open=" + url.QueryEscape("s:"+sv.Kind+"|"+sv.Key)
			checks := map[string]any{}
			for rule, st := range sv.Checks[id] {
				checks[rule] = string(st)
			}
			fields := map[string]any{
				"pack": id, "kind": sv.Kind, "subject": sv.Key, "tier": tier, "tier_rank": rank, "findings": findings, "unchecked": unchecked,
				"checks": checks, "board": link,
			}
			if t := snap.Trends; t != nil {
				if vt, ok := t.Verdicts[trend.VerdictKey(id, sv.Kind, sv.Key)]; ok {
					fields["trend"] = TierTrend(vt, tiers[id], dayOf(snap.RefreshedAt), t.Window, link)
					fields["tier_since"] = vt.Since.UTC().Format(time.RFC3339)
					fields["tier_best"] = vt.Best
					fields["tier_direction"] = vt.Direction
				}
			}
			out = append(out, Entity{Blueprint: s.names[roleVerdict], Key: id + "/" + sv.Kind + "/" + sv.Key, Title: title + ": " + id, Relations: rels, Fields: fields})
		}
	}
	return out
}

// dayOf is the start of t's day in UTC. A field or a trend bound written by
// the day is the same from every pass of that day, so the hub sees a change
// only when something moved.
func dayOf(t time.Time) time.Time { return t.UTC().Truncate(24 * time.Hour) }

// TierTrend is a verdict's trend as a hub trend field holds it: the ranks
// it changed to, drawn as steps, labelled with the pack's tiers, and a link
// to the subject on the board. The hub holds the last step to `to`, so the
// axis ends at the end of the pass's day, never at the pass's own time.
func TierTrend(v trend.Verdict, tiers []string, day time.Time, window time.Duration, link string) map[string]any {
	points := make([]map[string]any, 0, len(v.Points))
	for _, p := range v.Points {
		points = append(points, map[string]any{"at": p.At.UTC().Format(time.RFC3339), "value": p.Value})
	}
	labels := map[string]string{"0": "None"}
	for i, t := range tiers {
		labels[fmt.Sprint(i+1)] = t
	}
	to := day.Add(24 * time.Hour)
	return map[string]any{
		"points": points, "step": true, "min": 0, "max": len(tiers), "labels": labels,
		"from": to.Add(-window).Format(time.RFC3339), "to": to.Format(time.RFC3339),
		"link": link, "link_title": "Open in trellis",
	}
}

// Evidence renders the queries behind a finding as markdown.
func Evidence(evs []estate.Evidence) string {
	var b strings.Builder
	for i, ev := range evs {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "**%s**\n\n```\n%s\n```\n\n%s", ev.Source, ev.Query, ev.Got)
	}
	return b.String()
}

// Push offers the blueprints when they moved, then writes the snapshot,
// then puts the scorecards that moved. It never fails the pass; it says
// what the hub did not take.
func (s *Sink) Push(ctx context.Context, snap *board.Snapshot) error {
	if !s.offer(ctx) {
		s.metrics.Push(ctx, "failed")
		return errors.New("orrery took no blueprints, so nothing was written")
	}
	entities := s.Entities(snap)
	counts, err := s.client.Snapshot(ctx, Scope, entities)
	if err != nil {
		s.metrics.Push(ctx, "failed")
		s.log.ErrorContext(ctx, "orrery push failed", "error", err, "entities", len(entities))
		return fmt.Errorf("orrery findings write: %w", err)
	}
	s.metrics.Push(ctx, "ok")
	s.log.InfoContext(ctx, "orrery push done", "entities", len(entities), "created", counts.Created, "updated", counts.Updated, "sent", counts.Sent, "sighted", counts.Sighted, "deleted", counts.Deleted, "rejected", counts.Rejected)
	var errs []error
	if counts.Rejected > 0 {
		errs = append(errs, fmt.Errorf("orrery rejected %d of %d entities", counts.Rejected, len(entities)))
	}
	if _, err := s.client.Snapshot(ctx, PackScope, append(s.PackEntities(snap), s.Rules(snap)...)); err != nil {
		s.log.ErrorContext(ctx, "orrery pack record failed", "error", err)
		errs = append(errs, fmt.Errorf("orrery pack record: %w", err))
	}
	errs = append(errs, s.putScorecards(ctx, snap))
	return errors.Join(errs...)
}

// PackEntities are the packs the pass judged with, one each.
func (s *Sink) PackEntities(snap *board.Snapshot) []Entity {
	packs := Adopt(snap.Packs)
	out := make([]Entity, 0, len(packs))
	for _, p := range packs {
		tiers := p.Tiers
		if tiers == nil {
			tiers = []string{}
		}
		fields := map[string]any{
			"title": p.Title, "version": p.Version, "family": p.Family, "status": p.Status, "rules": p.Rules, "tiers": tiers, "source": p.Source,
			"board": s.board + "/?view=findings&pack=" + url.QueryEscape(p.ID),
		}
		for _, pv := range snap.Evaluation.Packs {
			if pv.Pack != p.ID {
				continue
			}
			fields["level"] = string(pv.Level)
			fields["overdue"] = pv.Overdue
			if pv.Conformant != nil {
				fields["conformant"] = float64(int64(*pv.Conformant*10+0.5)) / 10
			}
		}
		if snap.Trends != nil {
			if pt, ok := snap.Trends.Packs[p.ID]; ok && len(pt.Points) > 0 {
				fields["trend"] = ShareTrend(pt, snap.Trends.Window, s.board+"/?view=overview&pack="+url.QueryEscape(p.ID))
				if pt.Change7 != nil {
					fields["change_7d"] = *pt.Change7
				}
				if pt.Change30 != nil {
					fields["change_30d"] = *pt.Change30
				}
			}
		}
		out = append(out, Entity{Blueprint: s.names[rolePack], Key: p.ID, Title: p.Title, Fields: fields})
	}
	return out
}

// Rules are the rules the pass judged with on the board, one each, whether
// a pack file holds it or a source brought it, with how it answered across
// the estate; none when the estate names no rule blueprint.
func (s *Sink) Rules(snap *board.Snapshot) []Entity {
	if s.names[roleRule] == "" || snap.Packs == nil {
		return nil
	}
	stats := map[string]judge.RuleStat{}
	for _, st := range snap.Evaluation.Rules {
		stats[st.Rule] = st
	}
	var out []Entity
	for _, p := range snap.Packs.Packs {
		for _, r := range p.Rules {
			if !r.RunsIn(pack.Board) {
				continue
			}
			st := stats[r.ID]
			fields := map[string]any{
				"rule": r.ID, "pack": r.Pack, "kind": r.Kind, "severity": string(r.Severity), "standard": r.Standard,
				"status": string(r.PhaseAt(snap.RefreshedAt)), "fix": r.Fix, "source": p.Source,
				"pass": st.Pass, "fail": st.Fail, "nodata": st.NoData, "na": st.NA, "waived": st.Waived, "overdue": st.Overdue,
				"board": s.board + "/?view=findings&rule=" + url.QueryEscape(r.ID),
			}
			if r.Aim != "" {
				fields["aim"] = r.Aim
			}
			if len(r.Controls) > 0 {
				fields["controls"] = r.Controls
			}
			if r.Tier != "" {
				fields["tier"] = r.Tier
			}
			rels := map[string]any{}
			if pl := s.names[rolePillar]; pl != "" && r.Pillar != "" {
				rels[rolePillar] = pl + ":" + r.Pillar
			}
			out = append(out, Entity{Blueprint: s.names[roleRule], Key: r.ID, Title: r.Title, Fields: fields, Relations: rels})
		}
	}
	return out
}

// Waivers are every waiver the pass weighed, from every store, one each:
// whether it counts and, when not, why, with its reason, category, end,
// and the store it came from, so a person reads each exception beside the
// rules. A waiver is keyed by its store, rule, subject, and match, and
// links to the entity its subject is when a finding names it. None when
// the estate names no waiver blueprint.
func (s *Sink) Waivers(snap *board.Snapshot) []Entity {
	if s.names[roleWaiver] == "" {
		return nil
	}
	type about struct {
		title string
		ref   *estate.Ref
	}
	subjects := map[string]about{}
	for _, f := range snap.Evaluation.Findings {
		subjects[f.Key()] = about{f.Title, f.Ref}
	}
	seen := map[string]int{}
	var out []Entity
	for _, w := range snap.Evaluation.Waivers {
		key := w.From + "/" + w.Key()
		if w.Match != "" {
			key += "/" + w.Match
		}
		if seen[key]++; seen[key] > 1 {
			key += fmt.Sprintf("/%d", seen[key])
		}
		fields := map[string]any{
			"rule": w.Rule, "subject": w.Subject, "status": string(w.Status), "reason": w.Reason,
			"category": string(w.Kind()), "covers": w.Covers, "source": w.From,
			"board": s.board + "/?view=waivers",
		}
		for name, v := range map[string]string{"problem": w.Problem, "owner": w.Owner, "control": w.Control, "match": w.Match, "id": w.ID} {
			if v != "" {
				fields[name] = v
			}
		}
		for name, t := range map[string]time.Time{"ends": w.Ends(), "expires_at": w.ExpiresAt, "review_at": w.ReviewAt, "granted_at": w.GrantedAt} {
			if !t.IsZero() {
				fields[name] = t.UTC().Format(time.RFC3339)
			}
		}
		a := subjects[w.Key()]
		title := a.title
		if title == "" {
			title = w.Subject
		}
		rels := map[string]any{}
		s.relate(rels, a.ref)
		out = append(out, Entity{Blueprint: s.names[roleWaiver], Key: key, Title: title + ": " + w.Rule, Fields: fields, Relations: rels})
	}
	return out
}

// ShareTrend is a pack's daily share of conformant subjects as a hub trend
// field holds it. The axis ends on the latest day with a point and reaches
// the window back from it, so the value is the same until a day is added.
func ShareTrend(p trend.Pack, window time.Duration, link string) map[string]any {
	points := make([]map[string]any, 0, len(p.Points))
	for _, pt := range p.Points {
		points = append(points, map[string]any{"at": pt.At.UTC().Format(time.RFC3339), "value": pt.Value})
	}
	out := map[string]any{
		"points": points, "min": 0, "max": 100, "unit": "%",
		"link": link, "link_title": "Open in trellis",
	}
	if len(p.Points) > 0 {
		to := dayOf(p.Points[len(p.Points)-1].At)
		out["from"], out["to"] = to.Add(-window).Format(time.RFC3339), to.Format(time.RFC3339)
	}
	return out
}
