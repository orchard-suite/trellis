package gate

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/orchard-suite/platformkit/v3/annals"
	"github.com/orchard-suite/platformkit/v3/change"
)

func stated(key string, at time.Time, applier string, effects ...annals.Effect) annals.Event {
	return annals.Event{
		Source: "orrery", EventKey: key, At: at,
		Actor:   annals.Actor{Kind: "person", ID: "p-3f9a1c2b7e4d"},
		Action:  annals.Action{Verb: change.EventStated, Target: annals.Target{System: "orrery", Kind: "change", ID: key}, Outcome: "ok"},
		Items:   []annals.Item{{System: "catalog", Kind: "app", ID: "beacon"}},
		Effects: effects,
		Labels:  map[string]string{"applier": applier, "writer": "infra"},
	}
}

func after(verb, of string, at time.Time, outcome string, sights ...annals.Effect) annals.Event {
	source := "orrery"
	if verb == change.EventObserved {
		source = "dispatch"
	}
	return annals.Event{
		Source: source, EventKey: of + "." + verb, At: at, Cause: of, CauseSource: "orrery",
		Actor:   annals.Actor{Kind: "system", ID: source},
		Action:  annals.Action{Verb: verb, Target: annals.Target{System: "orrery", Kind: "change", ID: of}, Outcome: outcome},
		Effects: sights,
	}
}

func sight(kind, key, path, verdict string) annals.Effect {
	return annals.Effect{Kind: kind, Subject: annals.Subject{Blueprint: "app", Key: key}, Path: path, Verdict: verdict}
}

// A replay judges each past change as the pack would have, with proof as it
// stood then, and counts what each then did.
func TestReplayCountsAsksAndMisses(t *testing.T) {
	t1 := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	t2, t3, t4 := t1.Add(time.Hour), t1.Add(2*time.Hour), t1.Add(24*time.Hour)
	size := int64(0)
	records := []annals.Event{
		stated("c1", t1, "hub", annals.Effect{Kind: "stops", Subject: annals.Subject{Blueprint: "app", Key: "beacon"}, Path: "swap", Undo: "yes", Claim: "writer"}),
		after(change.EventEnded, "c1", t1.Add(time.Minute), "ok"),
		after(change.EventObserved, "c1", t1.Add(30*time.Minute), "ok", sight("stops", "beacon", "swap", "harm")),
		stated("c2", t2, "hub", annals.Effect{Kind: "grants", Subject: annals.Subject{Blueprint: "app", Key: "beacon"}, Path: "in-place", Size: &size, Undo: "yes", Claim: "writer"}),
		after(change.EventEnded, "c2", t2.Add(time.Minute), "ok"),
		stated("c3", t3, "asker", annals.Effect{Kind: "opens", Subject: annals.Subject{Blueprint: "app", Key: "beacon"}, Path: "in-place", Undo: "yes", Claim: "writer"}),
		after(change.EventEnded, "c3", t3.Add(time.Minute), "ok"),
		after(change.EventObserved, "c3", t3.Add(30*time.Minute), "ok", sight("opens", "beacon", "in-place", "clean")),
		stated("c4", t4, "hub", annals.Effect{Kind: "stops", Subject: annals.Subject{Blueprint: "app", Key: "comet"}, Path: "swap", Undo: "yes", Claim: "writer"}),
		after(change.EventEnded, "c4", t4.Add(time.Minute), "ok"),
		after(change.EventObserved, "c4", t4.Add(30*time.Minute), "ok", sight("stops", "comet", "swap", "clean")),
	}
	key := annals.ProofKey{Kind: "stops", Blueprint: "app", Path: "swap"}
	now := &fakeAnnals{counts: map[annals.ProofKey]annals.Proof{key: {Watched: 6, Clean: 5, Harm: 1}}}
	j := newJudge(t, gatePacks(t, nil), now, nil)
	h := Join(records)
	got, err := j.Replay(t.Context(), h, "gate", now, t1, t4.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	want := Tally{Pack: "gate@1.0.0", Since: t1, Until: t4.Add(time.Hour), Changes: 4, Asked: 2, Passed: 2, Misses: 1, Blind: 1, Needless: 2, Partial: 4}
	if got != want {
		t.Fatalf("replay\n got %+v\nwant %+v", got, want)
	}
	if now.asked[key] != 1 {
		t.Fatalf("each key is read once for the whole replay: %v", now.asked)
	}
	if _, err := j.Replay(t.Context(), h, "acme-gate", now, t1, t4); err == nil {
		t.Fatal("a pack the judge does not hold cannot be replayed")
	}
	if _, err := j.Replay(t.Context(), h, "gate", &fakeAnnals{err: context.DeadlineExceeded}, t1, t4); err == nil {
		t.Fatal("a replay with no proof says so, never counts")
	}
}

// Proof as of a time is Annals' counts less what the watch saw from then on.
func TestProofAsOf(t *testing.T) {
	key := annals.ProofKey{Kind: "stops", Blueprint: "app", Path: "swap"}
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	a := asOf{
		now: &fakeAnnals{counts: map[annals.ProofKey]annals.Proof{key: {Watched: 4, Clean: 2, Harm: 1, Blind: 1}}},
		sights: []sighting{
			{at: at.Add(-time.Hour), keys: map[annals.ProofKey]string{key: "clean"}},
			{at: at, keys: map[annals.ProofKey]string{key: "harm"}},
			{at: at.Add(time.Hour), keys: map[annals.ProofKey]string{key: "blind"}},
		},
		at: at,
	}
	p, err := a.Proof(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if p.Watched != 2 || p.Clean != 2 || p.Harm != 0 || p.Blind != 0 {
		t.Fatalf("as of: %+v", p)
	}
}

// A proof read that does not come back within the limit is no proof: the
// effect goes to people, and the judge still answers.
func TestASlowAnnalsIsNoProof(t *testing.T) {
	j := newJudge(t, gatePacks(t, nil), slowAnnals{}, nil)
	j.s.Limit = 20 * time.Millisecond
	started := time.Now()
	ans := judgeOK(t, j, statement(change.ApplierHub, effect("stops", "app", "beacon", "swap", 1)))
	if took := time.Since(started); took > time.Second {
		t.Fatalf("the judge waited %s", took)
	}
	if ans.Route.To != change.RoutePeople || ans.Reasons[0].Fence != change.FenceUnknown {
		t.Fatalf("%+v %s", ans.Route, show(ans.Reasons))
	}
}

type slowAnnals struct{}

func (slowAnnals) Proof(ctx context.Context, _ annals.ProofKey) (*annals.Proof, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A record that keeps the whole statement replays its unknown parts, which
// meet the unknown fence; one stored before it did replays without them.
func TestReplayJudgesUnknownParts(t *testing.T) {
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	opens := annals.Effect{Kind: "opens", Subject: annals.Subject{Blueprint: "app", Key: "beacon"}, Path: "in-place", Undo: "yes", Claim: "writer", ClaimRule: "listener", ClaimVersion: "4.2.0"}
	whole := stated("c1", at, "hub", opens)
	whole.Statement = &annals.Statement{
		Key:      "run-1001",
		Writer:   change.Writer{Name: "infra", Version: "3.1.0"},
		Things:   []change.Ref{{Blueprint: "app", Key: "beacon"}},
		Artifact: change.Artifact{Kind: "plan", Digest: "sha256:" + strings.Repeat("a", 64)},
		Unknown:  []change.Unknown{{Thing: &change.Ref{Blueprint: "app", Key: "beacon"}, Part: "settings.feature_x", Why: change.WhyUnnamed}},
	}
	older := stated("c2", at.Add(time.Hour), "hub", opens)
	h := Join([]annals.Event{
		whole, after(change.EventEnded, "c1", at.Add(time.Minute), "ok"),
		older, after(change.EventEnded, "c2", at.Add(time.Hour+time.Minute), "ok"),
	})
	if h.Changes[0].Statement.Key != "run-1001" || h.Changes[0].Statement.Effects[0].Claim.Rule != "listener" || h.Changes[0].Partial {
		t.Fatalf("the whole statement: %+v", h.Changes[0])
	}
	if h.Changes[1].Statement.Key != "c2" || !h.Changes[1].Partial {
		t.Fatalf("an older record keys the change by its event key: %+v", h.Changes[1])
	}
	j := newJudge(t, gatePacks(t, nil), &fakeAnnals{}, nil)
	got, err := j.Replay(t.Context(), h, "gate", &fakeAnnals{}, at, at.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got.Changes != 2 || got.Asked != 1 || got.Passed != 1 || got.Partial != 1 {
		t.Fatalf("the unknown part asks people; the older record passes: %+v", got)
	}
}
