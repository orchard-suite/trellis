package gate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orchard-suite/platformkit/v3/annals"
	"github.com/orchard-suite/platformkit/v3/change"

	"github.com/orchard-suite/trellis"
	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/pack"
)

var now = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

var approvers = []change.Grant{{Field: "team", Values: []string{"platform"}}}

// fakeAnnals answers proof from a table, and counts each key it was asked.
type fakeAnnals struct {
	mu     sync.Mutex
	counts map[annals.ProofKey]annals.Proof
	asked  map[annals.ProofKey]int
	err    error
}

func (f *fakeAnnals) Proof(_ context.Context, k annals.ProofKey) (*annals.Proof, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.asked == nil {
		f.asked = map[annals.ProofKey]int{}
	}
	f.asked[k]++
	if f.err != nil {
		return nil, f.err
	}
	p := f.counts[k]
	p.Kind, p.Blueprint, p.Path = k.Kind, k.Blueprint, k.Path
	return &p, nil
}

// gatePack writes an estate gate pack into a folder and loads the gate's
// set from it.
func gatePacks(t *testing.T, files map[string]string, on ...string) *pack.Set {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	set, err := pack.LoadGate(trellis.Packs(), dir, on)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func newJudge(t *testing.T, set *pack.Set, proofs Proofs, cat *Catalog) *Judge {
	t.Helper()
	s := Settings{Approvers: approvers, Catalog: cat, Version: "6.2.0", Now: func() time.Time { return now }}
	if proofs != nil {
		s.Proofs = proofs
	}
	j, err := New(set, s)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func statement(applier string, effects ...change.Effect) *change.Statement {
	st := &change.Statement{
		Key:      "run-1001",
		Writer:   change.Writer{Name: "infra", Version: "3.1.0"},
		Actor:    change.Actor{Kind: change.ActorPerson, ID: "ada@example.com"},
		Things:   []change.Ref{{Blueprint: "app", Key: "beacon"}},
		Artifact: change.Artifact{Kind: "plan", Digest: "sha256:" + strings.Repeat("a", 64)},
		Applier:  applier,
		Effects:  effects,
	}
	return st
}

func effect(kind, blueprint, key, path string, size ...int64) change.Effect {
	e := change.Effect{Kind: kind, Subject: change.Ref{Blueprint: blueprint, Key: key}, Path: path, Undo: change.UndoYes, Claim: change.Claim{From: change.ClaimWriter}}
	if len(size) > 0 {
		e.Size = &size[0]
	}
	return e
}

func judgeOK(t *testing.T, j *Judge, st *change.Statement) *Answer {
	t.Helper()
	if err := st.Validate(j.Kinds()); err != nil {
		t.Fatalf("the test's statement is invalid: %v", err)
	}
	ans, err := j.Judge(t.Context(), st)
	if err != nil {
		t.Fatal(err)
	}
	return ans
}

// Two estate rules that each ask people join the strict way: the grants
// union, the largest quorum stands, the earliest by, the latest start.
func TestRulesCombineTheStrictWay(t *testing.T) {
	set := gatePacks(t, map[string]string{"acme.yaml": `
pack: acme-gate
title: Acme gate
about: Who looks
version: 2.0.0
family: gate
rules:
  - id: effect.grants
    title: Grants access
    kind: effect
    standard: s
    pillar: platform/security
    skip: [{ if: effect.kind != "grants", because: not a grant }]
    check: effect.size == 0
    detail: '"grants"'
    fix: Ask.
    route:
      grants: [{ field: team, values: [platform] }]
      quorum: 1
      by: now + duration("24h")
      start: now + duration("2h")
  - id: effect.costs
    title: Costs
    kind: effect
    standard: s
    pillar: platform/control_plane
    skip: [{ if: effect.kind != "costs", because: no bill }]
    check: effect.size < 100
    detail: '"costs"'
    fix: Ask.
    route:
      grants: [{ field: team, values: [payments, platform] }, { path: owner, field: email, values: [ops@example.org] }]
      quorum: 2
      by: now + duration("4h")
      start: now + duration("1h")
`}, "acme-gate")
	j := newJudge(t, set, nil, nil)
	ans := judgeOK(t, j, statement(change.ApplierHub, effect("grants", "app", "beacon", "in-place", 3), effect("costs", "app", "beacon", "in-place", 900)))
	r := ans.Route
	if r.To != change.RoutePeople || r.Quorum != 2 {
		t.Fatalf("route %+v", r)
	}
	if len(r.Grants) != 2 || !slices.Equal(r.Grants[0].Values, []string{"platform", "payments"}) || r.Grants[1].Path != "owner" {
		t.Fatalf("grants join by path and field, each value once: %+v", r.Grants)
	}
	if !r.By.Equal(now.Add(4*time.Hour)) || !r.Start.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("by is the earliest and start the latest: by %v start %v", r.By, r.Start)
	}
	want := []change.RouteReason{{Effect: at(0), Rule: "effect.grants", Proof: change.ProofClaimed}, {Effect: at(1), Rule: "effect.costs", Proof: change.ProofClaimed}}
	if !sameReasons(ans.Reasons, want) {
		t.Fatalf("reasons %s", show(ans.Reasons))
	}
	if ans.Judge != (change.Judge{Name: "trellis", Version: "6.2.0", Pack: "acme-gate@2.0.0"}) {
		t.Fatalf("judge %+v", ans.Judge)
	}
}

// A change every rule passes, applied by a job the hub starts, goes to no
// one, with no reasons; the same change applied where it was asked meets
// the asker fence and goes to the estate's default approvers.
func TestNoOneOnlyOnTheHubsJob(t *testing.T) {
	j := newJudge(t, gatePacks(t, nil), nil, nil)
	ans := judgeOK(t, j, statement(change.ApplierHub, effect("opens", "app", "beacon", "in-place")))
	if ans.Route.To != change.RouteNoOne || len(ans.Reasons) != 0 || ans.Reasons == nil {
		t.Fatalf("hub: %+v %s", ans.Route, show(ans.Reasons))
	}
	ans = judgeOK(t, j, statement(change.ApplierAsker, effect("opens", "app", "beacon", "in-place")))
	if ans.Route.To != change.RoutePeople || ans.Route.Quorum != 1 || !slices.EqualFunc(ans.Route.Grants, approvers, grantEqual) {
		t.Fatalf("asker: %+v", ans.Route)
	}
	if !sameReasons(ans.Reasons, []change.RouteReason{{Fence: change.FenceAsker}}) {
		t.Fatalf("asker reasons %s", show(ans.Reasons))
	}
	if ans.Judge.Pack != "gate@1.0.0" {
		t.Fatalf("with no estate pack the base pack answers: %q", ans.Judge.Pack)
	}
}

// An effect whose proof Annals could not give, and every part the writer
// could not name, meet the unknown fence and go to the default approvers.
func TestUnknownFence(t *testing.T) {
	down := &fakeAnnals{err: errors.New("annals did not answer")}
	j := newJudge(t, gatePacks(t, nil), down, nil)
	st := statement(change.ApplierHub, effect("stops", "app", "beacon", "swap", 1), effect("opens", "app", "beacon", "in-place"))
	st.Unknown = []change.Unknown{{Thing: &change.Ref{Blueprint: "app", Key: "beacon"}, Part: "settings.feature_x", Why: change.WhyUnnamed}}
	ans := judgeOK(t, j, st)
	if ans.Route.To != change.RoutePeople || !slices.EqualFunc(ans.Route.Grants, approvers, grantEqual) {
		t.Fatalf("route %+v", ans.Route)
	}
	want := []change.RouteReason{
		{Effect: at(0), Fence: change.FenceUnknown, Proof: change.ProofUnknown},
		{Part: at(0), Fence: change.FenceUnknown},
		{Effect: at(0), Rule: "effect.disruption-unproven", Proof: change.ProofUnknown},
	}
	if !sameReasons(ans.Reasons, want) {
		t.Fatalf("reasons %s", show(ans.Reasons))
	}
}

// With no watch, a disruption is proven no further than claimed, so the
// base pack sends it to people; an exposure is never read.
func TestNoWatchProvesNoDisruption(t *testing.T) {
	j := newJudge(t, gatePacks(t, nil), nil, nil)
	ans := judgeOK(t, j, statement(change.ApplierHub, effect("stops", "app", "beacon", "swap", 1)))
	if ans.Route.To != change.RoutePeople || !sameReasons(ans.Reasons, []change.RouteReason{{Effect: at(0), Rule: "effect.disruption-unproven", Proof: change.ProofClaimed}}) {
		t.Fatalf("%+v %s", ans.Route, show(ans.Reasons))
	}
}

// Proof is read by key, each key once, for disruptions alone, and the
// counts set the level the rules see.
func TestProofIsReadByKey(t *testing.T) {
	clean := annals.ProofKey{Kind: "stops", Blueprint: "app", Path: "swap"}
	once := annals.ProofKey{Kind: "cuts", Blueprint: "app", Path: "rolling"}
	fake := &fakeAnnals{counts: map[annals.ProofKey]annals.Proof{
		clean: {Watched: 7, Clean: 6, Blind: 1},
		once:  {Watched: 1, Clean: 1},
	}}
	j := newJudge(t, gatePacks(t, nil), fake, nil)
	st := statement(change.ApplierHub,
		effect("stops", "app", "beacon", "swap", 1),
		effect("stops", "app", "comet", "swap", 2),
		effect("cuts", "app", "beacon", "rolling"),
		effect("grants", "app", "beacon", "in-place", 0),
	)
	ans := judgeOK(t, j, st)
	if ans.Route.To != change.RouteNoOne {
		t.Fatalf("measured and rehearsed disruptions pass: %+v %s", ans.Route, show(ans.Reasons))
	}
	if len(fake.asked) != 2 || fake.asked[clean] != 1 || fake.asked[once] != 1 {
		t.Fatalf("each disruption key once, no exposure: %v", fake.asked)
	}
	in := j.prepare(t.Context(), st, fake, now)
	levels := []string{in.proof[0].Level, in.proof[1].Level, in.proof[2].Level, in.proof[3].Level}
	if !slices.Equal(levels, []string{change.ProofMeasured, change.ProofMeasured, change.ProofRehearsed, change.ProofClaimed}) {
		t.Fatalf("levels %v", levels)
	}
}

func TestLevel(t *testing.T) {
	for _, c := range []struct {
		counts   *annals.Proof
		absorbed bool
		want     string
	}{
		{nil, false, change.ProofClaimed},
		{&annals.Proof{Watched: 3, Blind: 3}, false, change.ProofClaimed},
		{&annals.Proof{Watched: 1, Clean: 1}, false, change.ProofRehearsed},
		{&annals.Proof{Watched: 5, Clean: 5}, false, change.ProofMeasured},
		{&annals.Proof{Watched: 9, Clean: 8, Harm: 1}, false, change.ProofClaimed},
		{&annals.Proof{Watched: 9, Clean: 8, Harm: 1}, true, change.ProofMeasured},
	} {
		if got := Level(c.counts, c.absorbed, 5); got != c.want {
			t.Errorf("Level(%+v, %v) = %s, want %s", c.counts, c.absorbed, got, c.want)
		}
	}
}

// A thing's safe path proven after it last did harm absorbs the harm a key
// saw elsewhere; one that failed since does not.
func TestSafePathAbsorbsOnlyOnceProven(t *testing.T) {
	key := annals.ProofKey{Kind: "stops", Blueprint: "app", Path: "swap"}
	fake := &fakeAnnals{counts: map[annals.ProofKey]annals.Proof{key: {Watched: 8, Clean: 7, Harm: 1}}}
	e := estate.New(now)
	w := e.Writer("orrery")
	app := w.Subject("app", "beacon", "Beacon")
	app.SetRef(estate.Ref{Blueprint: "app", Key: "beacon"})
	_ = app.Set("app", map[string]any{"tier": "1"})
	sp := w.Subject("safe_path", "app/beacon/swap", "swap on Beacon")
	_ = sp.Set("safe_path", map[string]any{"path": "swap", "subject_blueprint": "app", "subject_key": "beacon", "proven_at": "2026-11-03T00:00:00Z", "failed_at": "2026-10-01T00:00:00Z"})
	sp2 := w.Subject("safe_path", "app/comet/swap", "swap on Comet")
	_ = sp2.Set("safe_path", map[string]any{"path": "swap", "subject_blueprint": "app", "subject_key": "comet", "proven_at": "2026-10-01T00:00:00Z", "failed_at": "2026-11-03T00:00:00Z"})
	cat := NewCatalog("safe_path")
	cat.Update(e)
	j := newJudge(t, gatePacks(t, nil), fake, cat)
	st := statement(change.ApplierHub, effect("stops", "app", "beacon", "swap", 1), effect("stops", "app", "comet", "swap", 1))
	ans := judgeOK(t, j, st)
	if !sameReasons(ans.Reasons, []change.RouteReason{{Effect: at(1), Rule: "effect.disruption-unproven", Proof: change.ProofClaimed}}) {
		t.Fatalf("only Comet, whose swap failed since it was proven, waits: %s", show(ans.Reasons))
	}
	in := j.prepare(t.Context(), st, fake, now)
	view, _ := in.effects[0].Subject.Value("effect")
	v := view.(map[string]any)
	if v["safe"] != true || v["held"] != true {
		t.Fatalf("Beacon's view: %v", v)
	}
	if got := in.effects[0].Subject; got.Kind != pack.GateEffect {
		t.Fatalf("kind %s", got.Kind)
	}
}

// A draft pack answers in drafts and never in the route; a counted pack
// with an announced rule answers in drafts too, as if the rule counted.
func TestDraftsAnswerBesideTheRoute(t *testing.T) {
	rule := func(id, from string) string {
		return `
  - id: ` + id + `
    title: Costs
    kind: effect
    standard: s
    pillar: platform/control_plane
    skip: [{ if: effect.kind != "costs", because: no bill }]
    check: effect.size < 100
    detail: '"costs"'
    fix: Ask.` + from + `
    route: { grants: [{ field: team, values: [payments] }] }`
	}
	set := gatePacks(t, map[string]string{
		"next.yaml": "pack: acme-next\ntitle: Next\nabout: Next\nversion: 3.0.0-rc.1\nfamily: gate\nstatus: draft\nrules:" + rule("effect.costs-next", ""),
		"live.yaml": "pack: acme-live\ntitle: Live\nabout: Live\nversion: 2.0.0\nfamily: gate\nrules:" + rule("effect.costs-soon", "\n    from: 2026-12-01T00:00:00Z"),
	}, "acme-live", "acme-next")
	j := newJudge(t, set, nil, nil)
	ans := judgeOK(t, j, statement(change.ApplierHub, effect("costs", "app", "beacon", "in-place", 900)))
	if ans.Route.To != change.RouteNoOne {
		t.Fatalf("neither a draft nor an announced rule counts: %+v %s", ans.Route, show(ans.Reasons))
	}
	if ans.Judge.Pack != "acme-live@2.0.0" {
		t.Fatalf("the counting pack: %q", ans.Judge.Pack)
	}
	if len(ans.Drafts) != 2 {
		t.Fatalf("drafts %+v", ans.Drafts)
	}
	for i, want := range []struct{ pack, rule string }{{"acme-live@2.0.0", "effect.costs-soon"}, {"acme-next@3.0.0-rc.1", "effect.costs-next"}} {
		d := ans.Drafts[i]
		if d.Pack != want.pack || d.Route.To != change.RoutePeople || d.Route.Grants[0].Values[0] != "payments" || !sameReasons(d.Reasons, []change.RouteReason{{Effect: at(0), Rule: want.rule, Proof: change.ProofClaimed}}) {
			t.Fatalf("draft %d: %+v %s", i, d, show(d.Reasons))
		}
	}
}

// A rule that cannot say is no data, and no data asks people, never passes.
func TestNoDataAsksPeople(t *testing.T) {
	set := gatePacks(t, map[string]string{"acme.yaml": `
pack: acme-gate
title: Acme gate
about: Who looks
version: 2.0.0
family: gate
rules:
  - id: effect.held
    title: Changes a thing the catalog does not hold
    kind: effect
    standard: s
    pillar: platform/catalog
    needs: { effect.held: the board has not read the catalog }
    check: effect.held
    detail: '"not held"'
    fix: Add it to the catalog.
    route: {}
`}, "acme-gate")
	j := newJudge(t, set, nil, nil)
	ans := judgeOK(t, j, statement(change.ApplierHub, effect("opens", "app", "beacon", "in-place")))
	if ans.Route.To != change.RoutePeople || !sameReasons(ans.Reasons, []change.RouteReason{{Effect: at(0), Rule: "effect.held", Proof: change.ProofClaimed}}) {
		t.Fatalf("before the catalog is read: %+v %s", ans.Route, show(ans.Reasons))
	}
}

// An estate's own effect kinds are registered from its gate pack, and a
// statement naming one validates against the judge's kinds.
func TestEstateKindsAreRegistered(t *testing.T) {
	set := gatePacks(t, map[string]string{"acme.yaml": `
pack: acme-gate
title: Acme gate
about: Who looks
version: 2.0.0
family: gate
kinds:
  - { name: sends, family: exposure, means: People outside receive it., unit: people }
  - { name: stops, family: disruption, means: It stops or restarts., unit: copies, watch: 45m }
rules:
  - id: effect.sends
    title: Sends
    kind: effect
    standard: s
    pillar: platform/control_plane
    skip: [{ if: effect.kind != "sends", because: sends nothing }]
    check: effect.size == 0
    detail: '"sends"'
    fix: Ask.
    route: {}
`}, "acme-gate")
	j := newJudge(t, set, nil, nil)
	k, ok := j.Kinds().Get("sends")
	if !ok || k.Family != change.FamilyExposure {
		t.Fatalf("sends: %+v %v", k, ok)
	}
	if stops, _ := j.Kinds().Get("stops"); time.Duration(stops.Watch) != 45*time.Minute {
		t.Fatalf("an estate may give a base kind its own watch: %+v", stops)
	}
	ans := judgeOK(t, j, statement(change.ApplierHub, effect("sends", "app", "ember", "in-place", 1200)))
	if ans.Route.To != change.RoutePeople {
		t.Fatalf("%+v", ans.Route)
	}
}

func TestNewRefusesAJudgeWithNoApprovers(t *testing.T) {
	if _, err := New(gatePacks(t, nil), Settings{}); err == nil || !strings.Contains(err.Error(), "default approvers") {
		t.Fatalf("got %v", err)
	}
}

func grantEqual(a, b change.Grant) bool {
	return a.Path == b.Path && a.Field == b.Field && slices.Equal(a.Values, b.Values)
}

func sameReasons(got, want []change.RouteReason) bool {
	return show(got) == show(want)
}

func show(rs []change.RouteReason) string {
	var b strings.Builder
	for _, r := range rs {
		b.WriteString("{")
		if r.Effect != nil {
			b.WriteString("effect " + strconv.Itoa(*r.Effect) + " ")
		}
		if r.Part != nil {
			b.WriteString("part " + strconv.Itoa(*r.Part) + " ")
		}
		b.WriteString(r.Fence + " " + r.Rule + " " + r.Proof + "}")
	}
	return b.String()
}
