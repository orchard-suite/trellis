// Package gate judges one change to a live thing, handed in by a caller,
// and answers a route: no one, or the people who must look first. The
// change is a platform kit statement. Its rules are the gate packs: the
// built-in base pack, which always counts, and the estate's own, tuned and
// layered like any other pack. Before the rules run, each effect gets the
// proof the watch has earned for it, read from Annals by key; the fences
// then hold whatever the packs say. Nothing here reads the estate on a
// schedule or writes a finding: the gate is off the board.
package gate

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/orchard-suite/platformkit/v3/change"

	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/pack"
)

// Name is the judge's name in every answer.
const Name = "trellis"

// Limit is how long one judgment may wait on proof reads. A read not back
// by then is no proof, so the effect routes to people; the rules never
// wait. It is set from BenchmarkJudge at the scale target: README.md, "The
// judge", records the measurement.
const Limit = time.Second

// Settings are the estate's choices for the judge.
type Settings struct {
	// Approvers decide when a change goes to people and no rule named
	// anyone. Required: a fence always has someone to send to.
	Approvers []change.Grant
	// Proofs is where the watch's proof is read; nil when the estate keeps
	// no watch, so no disruption is proven past claimed.
	Proofs Proofs
	// Catalog is the board's facts about the things a change touches; nil
	// judges with none.
	Catalog *Catalog
	// Version is trellis's own, named in every answer.
	Version string
	// Limit bounds the proof reads of one judgment; Limit when zero.
	Limit time.Duration
	// Now is the judgment's clock; time.Now when nil.
	Now func() time.Time
	Log *slog.Logger
}

// Judge routes changes with a set of gate packs.
type Judge struct {
	set      *pack.Set
	base     *pack.Pack
	estate   []*pack.Pack
	params   judge.Params
	kinds    *change.Kinds
	measured int64
	s        Settings
}

// Answer is the judge's whole answer for one statement: who judged, the
// route, why, and what each gate pack that does not count yet would have
// answered.
type Answer struct {
	Judge   change.Judge         `json:"judge"`
	Route   change.Route         `json:"route"`
	Reasons []change.RouteReason `json:"reasons"`
	Drafts  []change.Draft       `json:"drafts"`
}

// New makes the judge over a gate set: the base pack first, then the
// estate's, as pack.LoadGate reads them.
func New(set *pack.Set, s Settings) (*Judge, error) {
	if len(set.Packs) == 0 || set.Packs[0].ID != pack.BaseGate {
		return nil, fmt.Errorf("gate: the set starts with the base gate pack %s", pack.BaseGate)
	}
	if len(s.Approvers) == 0 {
		return nil, fmt.Errorf("gate: the judge needs default approvers, who decide when a fence sends a change to people and no rule names anyone")
	}
	if err := change.ValidateRoute("default approvers", change.Route{To: change.RoutePeople, Grants: s.Approvers, Quorum: 1}, nil); err != nil {
		return nil, fmt.Errorf("gate: %w", err)
	}
	if len(set.Packs)-1 > change.MaxDrafts {
		return nil, fmt.Errorf("gate: at most %d estate gate packs, since each may answer as a draft", change.MaxDrafts)
	}
	if s.Limit == 0 {
		s.Limit = Limit
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	if s.Log == nil {
		s.Log = slog.New(slog.DiscardHandler)
	}
	j := &Judge{set: set, base: set.Packs[0], estate: set.Packs[1:], params: judge.Merge(set), s: s}
	kinds, err := registry(set)
	if err != nil {
		return nil, err
	}
	j.kinds = kinds
	n, ok := j.params[pack.BaseGate]["measured_after"].(float64)
	if !ok || n < 1 || n != float64(int64(n)) {
		return nil, fmt.Errorf("gate: params.%s.measured_after is a whole number of at least 1", pack.BaseGate)
	}
	j.measured = int64(n)
	return j, nil
}

// registry is the suite's base effect kinds with every gate pack's own. Two
// packs may name one kind only alike.
func registry(set *pack.Set) (*change.Kinds, error) {
	reg := change.Base()
	from := map[string]string{}
	for _, p := range set.Packs {
		for _, k := range p.EffectKinds() {
			if other, ok := from[k.Name]; ok {
				if was, _ := reg.Get(k.Name); was != k {
					return nil, fmt.Errorf("gate: packs %s and %s name the effect kind %q differently", other, p.ID, k.Name)
				}
			}
			if err := reg.Add(k); err != nil {
				return nil, fmt.Errorf("gate: pack %s: %w", p.ID, err)
			}
			from[k.Name] = p.ID
		}
	}
	return reg, nil
}

// Kinds are the effect kinds a statement may name: the base set and the
// gate packs'.
func (j *Judge) Kinds() *change.Kinds { return j.kinds }

// Pack names the gate packs that count, as an answer names them: the
// estate's that are not drafts, or the base pack when none.
func (j *Judge) Pack() string {
	var refs []string
	for _, p := range j.estate {
		if p.State != pack.Draft {
			refs = append(refs, p.Ref())
		}
	}
	if len(refs) == 0 {
		return j.base.Ref()
	}
	return strings.Join(refs, ",")
}

// Judge answers the route for a statement the caller validated against
// Kinds. It reads proof first, judges every counted rule and the fences,
// then each draft. An answer that breaks the kit's rules for a route is an
// error, never given.
func (j *Judge) Judge(ctx context.Context, st *change.Statement) (*Answer, error) {
	now := j.s.Now().UTC()
	in := j.prepare(ctx, st, j.s.Proofs, now)
	ans := &Answer{Judge: change.Judge{Name: Name, Version: j.s.Version, Pack: j.Pack()}, Reasons: []change.RouteReason{}, Drafts: []change.Draft{}}
	if err := ans.Judge.Validate(); err != nil {
		return nil, err
	}
	var err error
	if ans.Route, ans.Reasons, err = j.route(in, j.counted(now)); err != nil {
		return nil, err
	}
	for _, p := range j.set.Packs {
		if !j.drafting(p, now) {
			continue
		}
		d := change.Draft{Pack: p.Ref()}
		if d.Route, d.Reasons, err = j.route(in, j.draft(p, now)); err != nil {
			return nil, fmt.Errorf("draft %s: %w", p.Ref(), err)
		}
		if err := d.Validate("draft " + p.Ref()); err != nil {
			return nil, err
		}
		ans.Drafts = append(ans.Drafts, d)
	}
	return ans, nil
}

// counted are the rules that count at now: every counted rule of the base
// pack and of the estate's packs that are not drafts.
func (j *Judge) counted(now time.Time) []*pack.Rule {
	var out []*pack.Rule
	for _, p := range j.set.Packs {
		if p.State == pack.Draft {
			continue
		}
		for _, r := range p.Rules {
			if r.PhaseAt(now) == pack.Counted {
				out = append(out, r)
			}
		}
	}
	return out
}

// drafting says a pack does not count yet, whole or in part: a draft pack,
// or one with a rule drafted or announced.
func (j *Judge) drafting(p *pack.Pack, now time.Time) bool {
	if p.State == pack.Draft {
		return true
	}
	return slices.ContainsFunc(p.Rules, func(r *pack.Rule) bool { return r.PhaseAt(now) != pack.Counted })
}

// draft are the rules a pack answers with as a draft: the base pack's
// counted rules and every rule of its own, as if it counted alone.
func (j *Judge) draft(p *pack.Pack, now time.Time) []*pack.Rule {
	var out []*pack.Rule
	if p != j.base {
		for _, r := range j.base.Rules {
			if r.PhaseAt(now) == pack.Counted {
				out = append(out, r)
			}
		}
	}
	return append(out, p.Rules...)
}

// route judges rules over the prepared change and holds the fences: the
// strict way, so any rule that asks people wins over no one, grants join,
// the largest quorum stands, the earliest by, and the latest start.
func (j *Judge) route(in *input, rules []*pack.Rule) (change.Route, []change.RouteReason, error) {
	var a ask
	st := in.st
	for i := range st.Effects {
		if in.proof[i].Level == change.ProofUnknown {
			a.people(nil, nil, nil)
			a.because(change.RouteReason{Effect: at(i), Fence: change.FenceUnknown, Proof: change.ProofUnknown})
		}
	}
	for i := range st.Unknown {
		a.people(nil, nil, nil)
		a.because(change.RouteReason{Part: at(i), Fence: change.FenceUnknown})
	}
	for _, r := range rules {
		params := j.params[r.Pack]
		for i, p := range in.subjects(r.Kind) {
			res := r.Judge(p, params)
			if res.Status != pack.Fail && res.Status != pack.NoData {
				continue
			}
			by, start, err := r.Times(p, params)
			if err != nil {
				j.s.Log.Warn("a gate rule's route time could not be read; the change goes to people without it", "rule", r.ID, "error", err)
			}
			a.people(r.Route, by, start)
			reason := change.RouteReason{Rule: r.ID}
			switch r.Kind {
			case pack.GateEffect:
				reason.Effect, reason.Proof = at(i), in.proof[i].Level
			case pack.GateUnknown:
				reason.Part = at(i)
			}
			a.because(reason)
		}
	}
	if !a.asked && st.Applier == change.ApplierAsker {
		a.people(nil, nil, nil)
		a.because(change.RouteReason{Fence: change.FenceAsker})
	}
	route := a.route(j.s.Approvers)
	if a.dropped > 0 {
		j.s.Log.Warn("a route's reasons were cut to the kit's limit", "key", st.Key, "dropped", a.dropped, "kept", len(a.reasons))
	}
	if err := change.ValidateRoute("route", route, a.reasons); err != nil {
		return change.Route{}, nil, err
	}
	return route, a.reasons, nil
}

// ask gathers what the rules and fences asked for one route.
type ask struct {
	asked     bool
	grants    []change.Grant
	quorum    int
	by, start *time.Time
	reasons   []change.RouteReason
	dropped   int
}

func (a *ask) people(r *pack.Route, by, start *time.Time) {
	a.asked = true
	if r != nil {
		for _, g := range r.Grants {
			a.grant(g)
		}
		a.quorum = max(a.quorum, r.Quorum)
	}
	if by != nil && (a.by == nil || by.Before(*a.by)) {
		a.by = by
	}
	if start != nil && (a.start == nil || start.After(*a.start)) {
		a.start = start
	}
}

// grant joins a grant to the ones asked: values of one path and field are
// one grant, each value once.
func (a *ask) grant(g change.Grant) {
	for i, have := range a.grants {
		if have.Path == g.Path && have.Field == g.Field {
			for _, v := range g.Values {
				if !slices.Contains(have.Values, v) {
					a.grants[i].Values = append(a.grants[i].Values, v)
				}
			}
			return
		}
	}
	a.grants = append(a.grants, change.Grant{Path: g.Path, Field: g.Field, Values: slices.Clone(g.Values)})
}

// because records a reason, within the kit's limit on reasons; the route is
// whole either way.
func (a *ask) because(r change.RouteReason) {
	if len(a.reasons) >= change.MaxReasons {
		a.dropped++
		return
	}
	a.reasons = append(a.reasons, r)
}

func (a *ask) route(approvers []change.Grant) change.Route {
	if a.reasons == nil {
		a.reasons = []change.RouteReason{}
	}
	if !a.asked {
		return change.Route{To: change.RouteNoOne}
	}
	grants := a.grants
	if len(grants) == 0 {
		grants = approvers
	}
	return change.Route{To: change.RoutePeople, Grants: grants, Quorum: max(1, a.quorum), By: a.by, Start: a.start}
}

func at(i int) *int { return &i }
