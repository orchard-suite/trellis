// Package judge is the one place a verdict is made. It runs every rule of
// the packs switched on over every subject of the rule's kind, sets aside
// what a valid waiver covers, gives each finding its owner and its
// deadline, and rolls the rest up per subject, pack, kind, pillar, owner,
// and the estate. The same judge runs on the board and in a pipeline's
// check, so the two cannot disagree.
package judge

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/pack"
)

// State is where a finding stands.
type State string

const (
	// Open findings count: they set levels, carry deadlines, are written
	// to the catalog, and fail a gate at their severity.
	Open State = "open"
	// Waived findings are covered by a valid waiver until it ends.
	Waived State = "waived"
	// Announced findings are of a rule that counts from a later day.
	Announced State = "announced"
	// Draft findings are of a rule or pack being tried.
	Draft State = "draft"
)

// Finding is one rule one subject fails.
type Finding struct {
	Rule     string            `json:"rule"`
	Pack     string            `json:"pack"`
	Severity estate.Severity   `json:"severity"`
	Standard string            `json:"standard"`
	Pillar   string            `json:"pillar"`
	Controls []string          `json:"controls,omitempty"`
	Tier     string            `json:"tier,omitempty"`
	Kind     string            `json:"kind"`
	Subject  string            `json:"subject"`
	Title    string            `json:"title"`
	Ref      *estate.Ref       `json:"ref,omitempty"`
	Detail   string            `json:"detail"`
	Fix      string            `json:"fix"`
	Evidence []estate.Evidence `json:"evidence,omitempty"`
	State    State             `json:"state"`
	// Owner is who answers for the subject, from the estate's owner
	// expressions; "" when none says.
	Owner string `json:"owner"`
	// AsOf is when the oldest fact behind the finding was seen, and
	// Carried says one is from an earlier pass the source could not repeat.
	AsOf    time.Time `json:"as_of,omitzero"`
	Carried bool      `json:"carried,omitempty"`
	// FirstSeen is when the finding first opened, from history when the
	// board keeps it.
	FirstSeen time.Time `json:"first_seen,omitzero"`
	// Due is when an open finding must be fixed by, from the estate's
	// deadline for its severity; Overdue says that day has passed.
	Due     *time.Time `json:"due,omitempty"`
	Overdue bool       `json:"overdue,omitempty"`
	// CountsFrom is the day an announced finding's rule starts to count.
	CountsFrom *time.Time `json:"counts_from,omitempty"`
	// Waiver is the exception that covers it, and WaiverEnding says it
	// ends within the estate's warning.
	Waiver       *estate.Waiver `json:"waiver,omitempty"`
	WaiverEnding bool           `json:"waiver_ending,omitempty"`
}

// Key is rule/subject.
func (f Finding) Key() string { return estate.FindingKey(f.Rule, f.Subject) }

// Rollup is how a level is made from many subjects. Worst takes the worst
// subject: right for a small estate. Share holds a group red only for a
// critical finding, and otherwise by the share of its judged subjects that
// are green: green at or over Green percent, amber at or over Amber, red
// under it.
type Rollup struct {
	Mode  string  `yaml:"mode" json:"mode"`
	Green float64 `yaml:"green" json:"green,omitempty"`
	Amber float64 `yaml:"amber" json:"amber,omitempty"`
}

// Rollup modes.
const (
	Worst = "worst"
	Share = "share"
)

// Check says what is wrong with the rollup, or nil.
func (r Rollup) Check() error {
	switch r.Mode {
	case "", Worst:
		return nil
	case Share:
		if r.Green <= 0 || r.Green > 100 || r.Amber < 0 || r.Amber > r.Green {
			return fmt.Errorf("a share rollup needs 0 <= amber <= green <= 100, not green %v amber %v", r.Green, r.Amber)
		}
		return nil
	}
	return fmt.Errorf("rollup mode %q is not worst or share", r.Mode)
}

// Tally is a level and how many subjects sit at each level.
type Tally struct {
	Level    estate.Level `json:"level"`
	Subjects int          `json:"subjects"`
	Red      int          `json:"red"`
	Amber    int          `json:"amber"`
	Green    int          `json:"green"`
	Grey     int          `json:"grey"`
	// Draft counts the subjects only drafts judged: they have a reading,
	// but no level until a rule counts.
	Draft     int `json:"draft"`
	Findings  int `json:"findings"`
	Waived    int `json:"waived"`
	Overdue   int `json:"overdue"`
	Unchecked int `json:"unchecked"`
	// Critical counts the subjects with an open critical finding.
	Critical int `json:"critical"`
	// Conformant is the percent of judged subjects that are green; nil
	// when none was judged.
	Conformant *float64 `json:"conformant,omitempty"`
}

func newTally() Tally { return Tally{Level: estate.Grey} }

func (t *Tally) count(l estate.Level, c counts) {
	t.Subjects++
	t.Findings += c.findings
	t.Waived += c.waived
	t.Overdue += c.overdue
	t.Unchecked += c.unchecked
	if c.critical {
		t.Critical++
	}
	switch l {
	case estate.Red:
		t.Red++
	case estate.Amber:
		t.Amber++
	case estate.Green:
		t.Green++
	default:
		t.Grey++
	}
}

// settle sets the level by the rollup.
func (t *Tally) settle(r Rollup) {
	judged := t.Red + t.Amber + t.Green
	if judged == 0 {
		t.Level = estate.Grey
		t.Conformant = nil
		return
	}
	share := float64(t.Green) * 100 / float64(judged)
	t.Conformant = &share
	if r.Mode != Share {
		switch {
		case t.Red > 0:
			t.Level = estate.Red
		case t.Amber > 0:
			t.Level = estate.Amber
		default:
			t.Level = estate.Green
		}
		return
	}
	switch {
	case t.Critical > 0 || share < r.Amber:
		t.Level = estate.Red
	case share < r.Green:
		t.Level = estate.Amber
	default:
		t.Level = estate.Green
	}
}

// counts are one subject's numbers within a group.
type counts struct {
	findings, waived, overdue, unchecked int
	critical                             bool
}

// SubjectVerdict is one subject's level with the rules that decided it.
type SubjectVerdict struct {
	Kind  string       `json:"kind"`
	Key   string       `json:"key"`
	Title string       `json:"title"`
	Level estate.Level `json:"level"`
	Owner string       `json:"owner"`
	// Ref is the catalog entity the subject is, when it is one.
	Ref *estate.Ref `json:"ref,omitempty"`
	// Draft says only drafts judged the subject.
	Draft bool `json:"draft,omitempty"`
	// Outside says no rule applies to it: every one is out of scope or
	// skips it, so it is in no count.
	Outside bool `json:"outside,omitempty"`
	// Findings are every rule the subject fails, in every state.
	Findings []Finding `json:"findings"`
	// Unchecked names the counted rules that had no data, with why.
	Unchecked []Unchecked `json:"unchecked,omitempty"`
	// Tiers is the highest tier the subject reached in each tiered pack,
	// "" when not even the lowest.
	Tiers map[string]string `json:"tiers,omitempty"`
	// Checks is, per tiered pack, the state of every counted tiered rule
	// that applies to the subject, by rule id: pass (passed, or failed
	// under a valid waiver), fail (an open finding), or unknown (no data).
	// The subject reaches a tier exactly when every rule here of that tier
	// or a lower one is pass.
	Checks map[string]map[string]CheckState `json:"checks,omitempty"`
	// Facts are the subject's facts with where and when each was read.
	Facts map[string]estate.Fact `json:"facts,omitempty"`
	// Missed are the facts a source failed to read, with why.
	Missed map[string]estate.Miss `json:"missed,omitempty"`
}

// CheckState is where one counted tiered rule stands on a subject.
type CheckState string

const (
	CheckPass    CheckState = "pass"
	CheckFail    CheckState = "fail"
	CheckUnknown CheckState = "unknown"
)

// Unchecked is a rule that could say nothing about a subject.
type Unchecked struct {
	Rule   string `json:"rule"`
	Pack   string `json:"pack"`
	Reason string `json:"reason"`
}

// PackVerdict is one pack's level and counts over the subjects its counted
// rules judged.
type PackVerdict struct {
	Pack    string     `json:"pack"`
	Title   string     `json:"title"`
	About   string     `json:"about"`
	Version string     `json:"version"`
	Family  string     `json:"family"`
	State   pack.State `json:"status"`
	Rollup  Rollup     `json:"rollup"`
	// Tiers counts the subjects at each tier reached; "" is none.
	Tiers map[string]int `json:"tiers,omitempty"`
	Tally
}

// KindVerdict is one kind's level and counts.
type KindVerdict struct {
	Kind string `json:"kind"`
	Tally
}

// PillarVerdict is one platform pillar's level, computed from the rules
// that serve it: the platform's maturity read from live state.
type PillarVerdict struct {
	Pillar string   `json:"pillar"`
	Rules  []string `json:"rules"`
	Tally
}

// OwnerVerdict is what one owner answers for.
type OwnerVerdict struct {
	Owner string `json:"owner"`
	Tally
}

// RuleStat is how one rule answered across the estate: a rule mostly at no
// data reads a broken source, one mostly waived is a wrong rule.
type RuleStat struct {
	Rule     string          `json:"rule"`
	Pack     string          `json:"pack"`
	Kind     string          `json:"kind"`
	Title    string          `json:"title"`
	Severity estate.Severity `json:"severity"`
	Phase    pack.Phase      `json:"phase"`
	Pass     int             `json:"pass"`
	Fail     int             `json:"fail"`
	NoData   int             `json:"nodata"`
	NA       int             `json:"na"`
	Waived   int             `json:"waived"`
	Carried  int             `json:"carried"`
	Overdue  int             `json:"overdue"`
	// Unwritten are facts the rule reads that no configured source writes,
	// so it can never answer; nil when that is not known.
	Unwritten []string `json:"unwritten,omitempty"`
}

// WaiverStatus is where a waiver stands.
type WaiverStatus string

const (
	WaiverActive   WaiverStatus = "active"
	WaiverEnding   WaiverStatus = "ending"
	WaiverEnded    WaiverStatus = "ended"
	WaiverRejected WaiverStatus = "rejected"
)

// WaiverView is a waiver as the board shows it.
type WaiverView struct {
	estate.Waiver
	Status  WaiverStatus `json:"status"`
	Problem string       `json:"problem,omitempty"`
	// Covers counts the findings it sets aside now.
	Covers int `json:"covers"`
}

// Evaluation is the estate judged.
type Evaluation struct {
	At       time.Time        `json:"at"`
	Estate   Tally            `json:"estate"`
	Rollup   Rollup           `json:"rollup"`
	Packs    []PackVerdict    `json:"packs"`
	Kinds    []KindVerdict    `json:"kinds"`
	Pillars  []PillarVerdict  `json:"pillars"`
	Owners   []OwnerVerdict   `json:"owners"`
	Subjects []SubjectVerdict `json:"subjects"`
	// Findings are every failing rule on every subject, in every state.
	Findings []Finding  `json:"findings"`
	Rules    []RuleStat `json:"rules"`
	// WaiversKnown is false when no waiver store could be read, so every
	// finding stands.
	WaiversKnown bool         `json:"waivers_known"`
	Waivers      []WaiverView `json:"waivers"`
}

// Open is the findings that count.
func (ev Evaluation) Open() []Finding { return ev.In(Open) }

// In is the findings in one state.
func (ev Evaluation) In(st State) []Finding {
	var out []Finding
	for _, f := range ev.Findings {
		if f.State == st {
			out = append(out, f)
		}
	}
	return out
}

// Params are each pack's parameters, the pack's defaults with the estate's
// overrides laid over them.
type Params map[string]map[string]any

// Merge lays the set's tuning over each pack's defaults.
func Merge(set *pack.Set) Params {
	overrides := set.Params
	out := Params{}
	for _, p := range set.Packs {
		m := map[string]any{}
		for k, v := range p.Params {
			m[k] = v
		}
		for k, v := range overrides[p.ID] {
			m[k] = v
		}
		plain, _ := estate.Plain(m)
		out[p.ID], _ = plain.(map[string]any)
	}
	return out
}

// Owners says who answers for a subject: an expression per kind, and one
// for every other kind under "*".
type Owners map[string]*pack.Expr

// NewOwners compiles the estate's owner expressions.
func NewOwners(exprs map[string]string) (Owners, error) {
	out := Owners{}
	for kind, src := range exprs {
		e, err := pack.NewExpr(src)
		if err != nil {
			return nil, fmt.Errorf("owner of %s: %w", kind, err)
		}
		out[kind] = e
	}
	return out, nil
}

func (o Owners) of(p *pack.Prepared) string {
	e, ok := o[p.Subject.Kind]
	if !ok {
		e = o["*"]
	}
	if e == nil {
		return ""
	}
	s, err := e.String(p)
	if err != nil {
		return ""
	}
	return s
}

// Options are the estate's positions the judge applies.
type Options struct {
	Stage  pack.Stage
	Params Params
	// Rollup is the estate's, and Packs' a pack's own.
	Rollup     Rollup
	PackRollup map[string]Rollup
	// Deadlines are how long an open finding of each severity may stay
	// open; a severity without one has no deadline.
	Deadlines map[estate.Severity]time.Duration
	Owners    Owners
	Waivers   estate.Policy
	// FirstSeen is when a finding first opened, when known.
	FirstSeen func(key string) (time.Time, bool)
	// Written are the facts the configured sources write, nil when not
	// known, so a rule that reads another is named.
	Written map[string]bool
}

func levelOf(s estate.Severity) estate.Level {
	if s == estate.Critical {
		return estate.Red
	}
	return estate.Amber
}

// group is one subject's standing within a pack or pillar. Judged says a
// counted rule passed or failed on it; seen that one answered at all, so a
// subject every rule had no data for still counts, as grey.
type group struct {
	level  estate.Level
	c      counts
	judged bool
	seen   bool
}

func (g *group) pass() {
	g.judged, g.seen = true, true
	if g.level == estate.Grey {
		g.level = estate.Green
	}
}

func (g *group) fail(f Finding) {
	g.judged, g.seen = true, true
	g.c.findings++
	if f.Overdue {
		g.c.overdue++
	}
	if f.Severity == estate.Critical {
		g.c.critical = true
	}
	g.level = estate.Worst(estate.Worst(g.level, estate.Green), levelOf(f.Severity))
}

// Evaluate judges the estate.
func Evaluate(e *estate.Estate, set *pack.Set, o Options) Evaluation {
	ev := Evaluation{At: e.Now, Estate: newTally(), Rollup: o.Rollup, Findings: []Finding{}, WaiversKnown: e.WaiversKnown}
	rules := map[string]*pack.Rule{}
	for _, r := range set.Rules() {
		rules[r.ID] = r
	}
	waivers, views := sortWaivers(e, rules, o.Waivers)

	packs := map[string]*PackVerdict{}
	byKind := map[string][]*pack.Rule{}
	pillarRules := map[string][]string{}
	stats := map[string]*RuleStat{}
	tierRank := map[string]map[string]int{}
	for _, p := range set.Packs {
		r := o.Rollup
		if pr, ok := o.PackRollup[p.ID]; ok {
			r = pr
		}
		packs[p.ID] = &PackVerdict{Pack: p.ID, Title: p.Title, About: p.About, Version: p.Version, Family: p.Family, State: p.State, Rollup: r, Tally: newTally()}
		tierRank[p.ID] = map[string]int{}
		for i, t := range p.Tiers {
			tierRank[p.ID][t] = i
		}
		for _, rl := range p.Rules {
			if !rl.RunsIn(o.Stage) {
				continue
			}
			st := &RuleStat{Rule: rl.ID, Pack: p.ID, Kind: rl.Kind, Title: rl.Title, Severity: rl.Severity, Phase: rl.PhaseAt(e.Now)}
			if o.Written != nil {
				for _, f := range rl.Reads {
					if !o.Written[f] {
						st.Unwritten = append(st.Unwritten, f)
					}
				}
			}
			stats[rl.ID] = st
			byKind[rl.Kind] = append(byKind[rl.Kind], rl)
			if st.Phase == pack.Counted {
				pillarRules[rl.Pillar] = append(pillarRules[rl.Pillar], rl.ID)
			}
		}
	}
	pillars := map[string]*PillarVerdict{}
	for pl, ids := range pillarRules {
		pillars[pl] = &PillarVerdict{Pillar: pl, Rules: ids, Tally: newTally()}
	}
	owners := map[string]*OwnerVerdict{}
	back := e.Backlinks()

	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		kv := KindVerdict{Kind: kind, Tally: newTally()}
		for _, s := range e.Of(kind) {
			prepared := pack.Prepare(s, e.Now, e.Linked(s, back))
			sv := SubjectVerdict{Kind: kind, Key: s.Key, Title: s.Title, Level: estate.Grey, Ref: s.Ref, Findings: []Finding{}, Facts: s.Facts, Missed: s.Missed}
			sv.Owner = o.Owners.of(prepared)
			perPack := map[string]*group{}
			perPillar := map[string]*group{}
			whole := &group{level: estate.Grey}
			drafted := false
			// blocked is, per pack, the lowest tier rank a failing or
			// unknown counted rule holds the subject below; tiered says a
			// counted tiered rule spoke at all.
			blocked := map[string]int{}
			tiered := map[string]bool{}
			// note records a counted tiered rule's state on the subject.
			note := func(r *pack.Rule, st CheckState) {
				if r.Tier == "" {
					return
				}
				if sv.Checks == nil {
					sv.Checks = map[string]map[string]CheckState{}
				}
				if sv.Checks[r.Pack] == nil {
					sv.Checks[r.Pack] = map[string]CheckState{}
				}
				sv.Checks[r.Pack][r.ID] = st
			}
			block := func(r *pack.Rule) {
				if r.Tier == "" {
					return
				}
				rank := tierRank[r.Pack][r.Tier]
				if b, ok := blocked[r.Pack]; !ok || rank < b {
					blocked[r.Pack] = rank
				}
			}
			groupOf := func(m map[string]*group, id string) *group {
				g := m[id]
				if g == nil {
					g = &group{level: estate.Grey}
					m[id] = g
				}
				return g
			}
			for _, r := range byKind[kind] {
				st := stats[r.ID]
				res := r.Judge(prepared, o.Params[r.Pack])
				if res.Carried {
					st.Carried++
				}
				counted := st.Phase == pack.Counted
				if r.Tier != "" && counted && res.Status != pack.NotApplicable {
					tiered[r.Pack] = true
				}
				switch res.Status {
				case pack.NotApplicable:
					st.NA++
					continue
				case pack.NoData:
					st.NoData++
					if counted {
						sv.Unchecked = append(sv.Unchecked, Unchecked{Rule: r.ID, Pack: r.Pack, Reason: res.Detail})
						for _, g := range []*group{groupOf(perPack, r.Pack), groupOf(perPillar, r.Pillar), whole} {
							g.c.unchecked++
							g.seen = true
						}
						block(r)
						note(r, CheckUnknown)
					}
					continue
				case pack.Pass:
					st.Pass++
					if counted {
						note(r, CheckPass)
						groupOf(perPack, r.Pack).pass()
						groupOf(perPillar, r.Pillar).pass()
						whole.pass()
					} else {
						drafted = true
					}
					continue
				}
				f := Finding{Rule: r.ID, Pack: r.Pack, Severity: r.Severity, Standard: r.Standard, Pillar: r.Pillar, Controls: r.Controls, Tier: r.Tier, Kind: kind, Subject: s.Key, Title: s.Title, Ref: s.Ref, Detail: res.Detail, Fix: r.Fix, Evidence: res.Evidence, Owner: sv.Owner, AsOf: res.AsOf, Carried: res.Carried}
				if o.FirstSeen != nil {
					if t, ok := o.FirstSeen(f.Key()); ok {
						f.FirstSeen = t
					}
				}
				if f.FirstSeen.IsZero() {
					f.FirstSeen = e.Now
				}
				switch st.Phase {
				case pack.Drafted:
					f.State = Draft
					drafted = true
				case pack.Announced:
					f.State = Announced
					f.CountsFrom = r.From
					drafted = true
				default:
					if w, ok := covering(waivers[f.Key()], f); ok {
						f.State = Waived
						f.Waiver = &w
						f.WaiverEnding = o.Waivers.Ending(w, e.Now)
						st.Waived++
						note(r, CheckPass)
						for i := range views {
							if sameWaiver(views[i].Waiver, w) {
								views[i].Covers++
							}
						}
						for _, g := range []*group{groupOf(perPack, r.Pack), groupOf(perPillar, r.Pillar), whole} {
							g.pass()
							g.c.waived++
						}
					} else {
						f.State = Open
						if d, ok := o.Deadlines[f.Severity]; ok && d > 0 {
							due := f.FirstSeen.Add(d)
							f.Due = &due
							f.Overdue = e.Now.After(due)
						}
						st.Fail++
						if f.Overdue {
							st.Overdue++
						}
						block(r)
						note(r, CheckFail)
						for _, g := range []*group{groupOf(perPack, r.Pack), groupOf(perPillar, r.Pillar), whole} {
							g.fail(f)
						}
					}
				}
				if f.State == Draft || f.State == Announced {
					st.Fail++
				}
				sv.Findings = append(sv.Findings, f)
				ev.Findings = append(ev.Findings, f)
			}
			for id := range tiered {
				p, _ := set.Pack(id)
				reached := ""
				top := len(p.Tiers)
				if b, ok := blocked[id]; ok {
					top = b
				}
				if top > 0 {
					reached = p.Tiers[top-1]
				}
				if sv.Tiers == nil {
					sv.Tiers = map[string]string{}
				}
				sv.Tiers[id] = reached
				if packs[id].Tiers == nil {
					packs[id].Tiers = map[string]int{}
				}
				packs[id].Tiers[reached]++
			}
			for id, g := range perPack {
				if g.seen {
					packs[id].count(g.level, g.c)
				}
			}
			for id, g := range perPillar {
				if g.seen && pillars[id] != nil {
					pillars[id].count(g.level, g.c)
				}
			}
			sv.Level = whole.level
			sv.Draft = !whole.seen && drafted
			sv.Outside = !whole.seen && !drafted
			switch {
			case sv.Outside:
			case sv.Draft:
				kv.Subjects++
				kv.Draft++
			default:
				kv.count(sv.Level, whole.c)
				ev.Estate.count(sv.Level, whole.c)
				if whole.seen {
					ov := owners[sv.Owner]
					if ov == nil {
						ov = &OwnerVerdict{Owner: sv.Owner, Tally: newTally()}
						owners[sv.Owner] = ov
					}
					ov.count(sv.Level, whole.c)
				}
			}
			ev.Subjects = append(ev.Subjects, sv)
		}
		kv.settle(o.Rollup)
		ev.Kinds = append(ev.Kinds, kv)
	}
	ev.Estate.settle(o.Rollup)
	for _, p := range set.Packs {
		pv := packs[p.ID]
		pv.settle(pv.Rollup)
		ev.Packs = append(ev.Packs, *pv)
	}
	for _, id := range sortedKeys(pillars) {
		pillars[id].settle(o.Rollup)
		ev.Pillars = append(ev.Pillars, *pillars[id])
	}
	for _, id := range sortedKeys(owners) {
		owners[id].settle(o.Rollup)
		ev.Owners = append(ev.Owners, *owners[id])
	}
	for _, r := range set.Rules() {
		if st, ok := stats[r.ID]; ok {
			ev.Rules = append(ev.Rules, *st)
		}
	}
	ev.Waivers = views
	sortFindings(ev.Findings)
	return ev
}

// sortWaivers sets aside every waiver that does not count, with why, and
// indexes the rest by the finding they cover.
func sortWaivers(e *estate.Estate, rules map[string]*pack.Rule, policy estate.Policy) (map[string][]estate.Waiver, []WaiverView) {
	active := map[string][]estate.Waiver{}
	var views []WaiverView
	for _, w := range e.Waivers {
		v := WaiverView{Waiver: w}
		r, known := rules[w.Rule]
		switch p := w.Problem(); {
		case p != "":
			v.Status, v.Problem = WaiverRejected, p
		case !known:
			v.Status, v.Problem = WaiverRejected, "it names a rule no pack switched on has"
		case policy.TooLong(w, r.Severity, e.Now) != "":
			v.Status, v.Problem = WaiverRejected, policy.TooLong(w, r.Severity, e.Now)
		case !w.Ends().After(e.Now):
			v.Status = WaiverEnded
		case policy.Ending(w, e.Now):
			v.Status = WaiverEnding
		default:
			v.Status = WaiverActive
		}
		if v.Status == WaiverActive || v.Status == WaiverEnding {
			active[w.Key()] = append(active[w.Key()], w)
		}
		views = append(views, v)
	}
	sort.SliceStable(views, func(i, j int) bool { return views[i].Ends().Before(views[j].Ends()) })
	return active, views
}

// covering is the waiver that sets a finding aside: one for its rule and
// subject whose match, when it names one, is in the finding's detail.
func covering(ws []estate.Waiver, f Finding) (estate.Waiver, bool) {
	for _, w := range ws {
		if w.Match == "" || strings.Contains(f.Detail, w.Match) {
			return w, true
		}
	}
	return estate.Waiver{}, false
}

func sameWaiver(a, b estate.Waiver) bool {
	return a.Rule == b.Rule && a.Subject == b.Subject && a.Match == b.Match && a.Ends().Equal(b.Ends()) && a.From == b.From
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Severity != b.Severity {
			return a.Severity.Rank() < b.Severity.Rank()
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		return a.Subject < b.Subject
	})
}
