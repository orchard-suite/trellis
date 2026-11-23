package orrery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/orchard-suite/trellis/internal/board"
	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/pack"
)

// Scorecards asks the sink to put one hub scorecard per tiered pack, whose
// rules are the pack's own: each reads its state from the checks a summary
// field on the subject's blueprint rolls up from the verdicts. Field and
// the ids are the estate's names; "{pack}" in them is the pack's id, with
// every character a hub id does not take turned to "_".
type Scorecards struct {
	// Field is the object field on each subject blueprint that holds the
	// pack's checks, such as trellis_{pack}_checks.
	Field string `yaml:"field"`
	// ID is the scorecard's id; trellis_{pack} when empty.
	ID string `yaml:"id"`
	// Packs, when set, are the only packs that get a scorecard, each with
	// the id and title it takes in place of the default and the pack's
	// title. A pack the estate gives no checks field is left out of it.
	Packs map[string]PackCard `yaml:"packs"`
}

// PackCard is one pack's scorecard as the estate names it.
type PackCard struct {
	ID    string `yaml:"id"`
	Title string `yaml:"title"`
}

// Check says what is wrong with the setting, or nil.
func (c *Scorecards) Check() error {
	if c == nil {
		return nil
	}
	if strings.TrimSpace(c.Field) == "" {
		return errors.New("scorecards needs field, the summary field that holds a pack's checks, such as trellis_{pack}_checks")
	}
	return nil
}

func (c *Scorecards) fieldOf(packID string) string {
	return strings.ReplaceAll(c.Field, "{pack}", relationName(packID))
}

// puts says whether the pack gets a scorecard: every tiered pack, or only those Packs names.
func (c *Scorecards) puts(packID string) bool {
	if c.Packs == nil {
		return true
	}
	_, ok := c.Packs[packID]
	return ok
}

func (c *Scorecards) idOf(packID string) string {
	if id := c.Packs[packID].ID; id != "" {
		return id
	}
	id := c.ID
	if id == "" {
		id = "trellis_{pack}"
	}
	return strings.ReplaceAll(id, "{pack}", relationName(packID))
}

// Scorecard is a hub scorecard over several blueprints, as trellis puts it.
type Scorecard struct {
	Title       string                   `json:"title"`
	Description string                   `json:"description"`
	Levels      []ScorecardLevel         `json:"levels"`
	Blueprints  map[string]ScorecardPart `json:"blueprints"`
}

// ScorecardLevel is one of the pack's tiers.
type ScorecardLevel struct {
	Name string `json:"name"`
}

// ScorecardPart is one blueprint's scope and its rules by level name.
type ScorecardPart struct {
	Where ScorecardFilter            `json:"where"`
	Rules map[string][]ScorecardRule `json:"rules"`
}

// ScorecardFilter is the one filter trellis writes: the checks field is set.
type ScorecardFilter struct {
	Field string `json:"field"`
	Op    string `json:"op"`
}

// ScorecardRule is one pack rule, judged by trellis.
type ScorecardRule struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Fix         string `json:"fix,omitempty"`
	Judged      Judged `json:"judged"`
}

// Judged says a rule's state is the entry check of the object in field.
type Judged struct {
	Field string `json:"field"`
	Check string `json:"check"`
}

// ScorecardRuleID is a trellis rule id as a hub scorecard rule id: lower
// case, every character outside [a-z0-9_] turned to "_", and starting with
// a letter.
func ScorecardRuleID(rule string) string {
	id := relationName(rule)
	if id == "" || id[0] < 'a' || id[0] > 'z' {
		id = "r_" + id
	}
	return id
}

// Level is a tier as a level's name: its first letter upper, underscores
// as spaces.
func Level(tier string) string {
	s := strings.ReplaceAll(tier, "_", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// BuildScorecards shapes one scorecard per tiered pack that is not a draft
// and judged a subject the catalog holds, by id. A blueprint's rules are
// the pack's counted tiered rules of the kinds whose subjects carry it, in
// the pack's order; a level with none is left out of its rules, and a
// blueprint with none is left out.
func BuildScorecards(c *Scorecards, packs []Adopted, ev judge.Evaluation) map[string]Scorecard {
	out := map[string]Scorecard{}
	if c == nil {
		return out
	}
	// kinds is, per pack, the kinds each blueprint's judged subjects are.
	kinds := map[string]map[string]map[string]bool{}
	for _, sv := range ev.Subjects {
		if sv.Ref == nil || sv.Ref.Blueprint == "" {
			continue
		}
		for id := range sv.Tiers {
			if kinds[id] == nil {
				kinds[id] = map[string]map[string]bool{}
			}
			if kinds[id][sv.Ref.Blueprint] == nil {
				kinds[id][sv.Ref.Blueprint] = map[string]bool{}
			}
			kinds[id][sv.Ref.Blueprint][sv.Kind] = true
		}
	}
	for _, p := range packs {
		if len(p.Tiers) == 0 || p.Status == string(pack.Draft) || len(kinds[p.ID]) == 0 || !c.puts(p.ID) {
			continue
		}
		field := c.fieldOf(p.ID)
		parts := map[string]ScorecardPart{}
		for blueprint, ks := range kinds[p.ID] {
			rules := map[string][]ScorecardRule{}
			seen := map[string]bool{}
			for _, r := range p.Tiered {
				if r.Tier == "" || !ks[r.Kind] || seen[r.ID] || !r.RunsIn(pack.Board) || r.PhaseAt(ev.At) != pack.Counted {
					continue
				}
				seen[r.ID] = true
				sr := ScorecardRule{ID: ScorecardRuleID(r.ID), Title: r.Title, Fix: r.Fix, Judged: Judged{Field: field, Check: r.ID}}
				if r.Aim != "" {
					sr.Title, sr.Description = r.Aim, r.Title
				}
				rules[Level(r.Tier)] = append(rules[Level(r.Tier)], sr)
			}
			if len(rules) > 0 {
				parts[blueprint] = ScorecardPart{Where: ScorecardFilter{Field: field, Op: "exists"}, Rules: rules}
			}
		}
		if len(parts) == 0 {
			continue
		}
		levels := make([]ScorecardLevel, len(p.Tiers))
		for i, t := range p.Tiers {
			levels[i] = ScorecardLevel{Name: Level(t)}
		}
		about := strings.TrimRight(strings.TrimSpace(p.About), ".")
		desc := fmt.Sprintf("Judged by trellis's %s pack.", p.Title)
		if about != "" {
			desc = about + ". " + desc
		}
		title := p.Title
		if t := c.Packs[p.ID].Title; t != "" {
			title = t
		}
		out[c.idOf(p.ID)] = Scorecard{Title: title, Description: desc, Levels: levels, Blueprints: parts}
	}
	return out
}

// putScorecards puts each scorecard whose body moved since it was last put,
// so a steady estate sends nothing. A failure never fails the pass; the
// hub's refusal until the source holds the scorecards grant is said once
// per scorecard.
func (s *Sink) putScorecards(ctx context.Context, snap *board.Snapshot) error {
	if s.scorecards == nil {
		return nil
	}
	var errs []error
	cards := BuildScorecards(s.scorecards, Adopt(snap.Packs), snap.Evaluation)
	ids := make([]string, 0, len(cards))
	for id := range cards {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		body, err := json.Marshal(cards[id])
		if err != nil {
			s.log.ErrorContext(ctx, "orrery scorecard not shaped", "scorecard", id, "error", err)
			errs = append(errs, fmt.Errorf("orrery scorecard %s: %w", id, err))
			continue
		}
		sum := sha256.Sum256(body)
		hash := hex.EncodeToString(sum[:])
		if s.put[id] == hash {
			continue
		}
		if err := s.client.PutScorecard(ctx, id, json.RawMessage(body)); err != nil {
			var se *StatusError
			if errors.As(err, &se) && se.Code == http.StatusForbidden {
				if !s.refused[id] {
					s.refused[id] = true
					s.log.WarnContext(ctx, "orrery refused the scorecard: an admin must give trellis's source the scorecards grant", "scorecard", id, "error", err)
				}
				errs = append(errs, fmt.Errorf("orrery refused scorecard %s: trellis's source lacks the scorecards grant", id))
				continue
			}
			s.log.ErrorContext(ctx, "orrery scorecard put failed", "scorecard", id, "error", err)
			errs = append(errs, fmt.Errorf("orrery scorecard %s: %w", id, err))
			continue
		}
		s.put[id] = hash
		delete(s.refused, id)
		s.log.InfoContext(ctx, "orrery scorecard put", "scorecard", id, "blueprints", len(cards[id].Blueprints))
	}
	return errors.Join(errs...)
}
