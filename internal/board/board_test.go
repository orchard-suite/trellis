package board

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/meter"
	"github.com/orchard-suite/trellis/internal/pack"
	"github.com/orchard-suite/trellis/internal/sources"
	"github.com/orchard-suite/trellis/internal/trend"
)

type fn func(w *estate.Writer) error

func (f fn) Collect(_ context.Context, w *estate.Writer) error { return f(w) }

type declared struct {
	fn
	facts []string
}

func (d declared) Facts() []string { return d.facts }

const kevPack = `
pack: security
title: Security
version: 1.0.0
family: security
rules:
  - id: image.cve-kev
    title: Runs a known-exploited CVE
    kind: image
    severity: critical
    standard: vulnerability-exposure-in-the-catalog-count-everything-make-
    pillar: platform/security
    needs: { scan.cve_kev: not scanned }
    check: scan.cve_kev == 0
    detail: '"exploited"'
    fix: Rebuild.
`

func newBoard(t *testing.T, srcs []sources.Named, opts judge.Options, sinks ...Sink) *Board {
	t.Helper()
	metrics, err := meter.New()
	if err != nil {
		t.Fatal(err)
	}
	p, err := pack.Parse([]byte(kevPack), "test")
	if err != nil {
		t.Fatal(err)
	}
	set := &pack.Set{Packs: []*pack.Pack{p}}
	opts.Params = judge.Merge(set)
	return New(srcs, set, opts, metrics, slog.New(slog.NewTextHandler(io.Discard, nil)), sinks...)
}

const key = "image.cve-kev/ghcr.io/example/app:1.0"

func TestAFailedSourceLeavesItsFindingsStandingUntilTheyAreTooOld(t *testing.T) {
	kev, scanFails := 1, false
	inventory := fn(func(w *estate.Writer) error {
		w.Subject("image", "ghcr.io/example/app:1.0", "app")
		return nil
	})
	scanner := fn(func(w *estate.Writer) error {
		if scanFails {
			return errors.New("boom")
		}
		return w.Subject("image", "ghcr.io/example/app:1.0", "app").Set("scan", map[string]any{"cve_kev": kev})
	})
	var sunk int
	b := newBoard(t, []sources.Named{{Name: "inventory", Source: inventory, MaxAge: time.Hour}, {Name: "scanner", Source: scanner, MaxAge: time.Hour}}, judge.Options{}, Sink{Name: "count", Push: func(context.Context, *Snapshot) error { sunk++; return nil }})
	if b.Latest() != nil {
		t.Fatal("a snapshot before the first pass")
	}
	first := b.Refresh(context.Background())
	if first.Evaluation.Estate.Level != estate.Red || len(first.Events) != 0 {
		t.Fatalf("first: %+v", first.Evaluation.Estate)
	}
	seen := first.Evaluation.Open()[0].FirstSeen

	scanFails = true
	second := b.Refresh(context.Background())
	open := second.Evaluation.Open()
	if second.Runs[1].Outcome != sources.Failed || len(open) != 1 || !open[0].Carried || !open[0].FirstSeen.Equal(seen) || second.Runs[1].Carried != 1 {
		t.Errorf("the scanner failed: its finding stands on the carried scan, first seen unchanged: %+v runs %+v", open, second.Runs)
	}
	if len(second.Events) != 0 {
		t.Errorf("a carried finding is no change: %+v", second.Events)
	}

	scanFails, kev = false, 0
	third := b.Refresh(context.Background())
	if len(third.Evaluation.Open()) != 0 || third.Evaluation.Estate.Level != estate.Green {
		t.Errorf("fixed: %+v", third.Evaluation.Findings)
	}
	if len(third.Events) != 1 || third.Events[0].Type != Resolved {
		t.Errorf("the fix is one event: %+v", third.Events)
	}
	if sunk != 3 || b.Latest() != third || len(third.Sent) != 1 || !third.Sent[0].OK {
		t.Errorf("sinks %d latest ok %v sent %+v", sunk, b.Latest() == third, third.Sent)
	}
}

func TestAGapIsNoDataWithItsReasonAndIsNamedOnItsRun(t *testing.T) {
	inventory := fn(func(w *estate.Writer) error {
		w.Subject("image", "ghcr.io/example/app:1.0", "app")
		return nil
	})
	// The scanner ran, but the two places it reads were not there.
	scanner := fn(func(w *estate.Writer) error {
		w.Gap("image", "scan", "the scanner has no list of images")
		w.Gap("host", "scan", "the scanner has no list of hosts")
		return nil
	})
	b := newBoard(t, []sources.Named{{Name: "inventory", Source: inventory, MaxAge: time.Hour}, {Name: "scanner", Source: scanner, MaxAge: time.Hour}}, judge.Options{})
	snap := b.Refresh(context.Background())
	run := snap.Runs[1]
	if run.Outcome != sources.OK || len(run.Gaps) != 2 {
		t.Fatalf("the run is not a failure, and names what it could not read: %+v", run)
	}
	if run.Missed != 2 {
		t.Errorf("one miss on the image, and one for the hosts no subject shows: %+v", run)
	}
	ev := snap.Evaluation
	if len(ev.Open()) != 0 || ev.Estate.Level != estate.Grey || ev.Estate.Unchecked != 1 {
		t.Errorf("a gap is no data, never a pass: %+v", ev.Estate)
	}
	if u := ev.Subjects[0].Unchecked; len(u) != 1 || u[0].Reason != "not scanned: the scanner has no list of images" {
		t.Errorf("the rule says why it has no data: %+v", u)
	}
}

func TestFirstSeenComesFromHistoryWhenItKeepsOne(t *testing.T) {
	src := fn(func(w *estate.Writer) error {
		return w.Subject("image", "ghcr.io/example/app:1.0", "app").Set("scan", map[string]any{"cve_kev": 1})
	})
	then := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b := newBoard(t, []sources.Named{{Name: "src", Source: src, MaxAge: time.Hour}}, judge.Options{Deadlines: map[estate.Severity]time.Duration{estate.Critical: 24 * time.Hour}})
	b.Record(func(context.Context, *Snapshot) (*trend.Trends, error) { return nil, nil }, func(context.Context) (map[string]time.Time, error) {
		return map[string]time.Time{key: then}, nil
	})
	f := b.Refresh(context.Background()).Evaluation.Open()[0]
	if !f.FirstSeen.Equal(then) || !f.Overdue {
		t.Errorf("a finding open since January, with a day to fix, is overdue after a restart: %+v", f)
	}
}

func TestWrittenIsKnownOnlyWhenEverySourceSays(t *testing.T) {
	a := declared{facts: []string{"scan"}}
	b := declared{facts: []string{"image"}}
	if w := Written([]sources.Named{{Source: a}, {Source: b}}); !w["scan"] || !w["image"] || len(w) != 2 {
		t.Errorf("both sources' facts: %v", w)
	}
	if w := Written([]sources.Named{{Source: a}, {Source: fn(nil)}}); w != nil {
		t.Errorf("a source that does not say makes it unknown: %v", w)
	}
}

func TestDiffNamesEveryChange(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	f := func(rule, subject string, st judge.State, overdue bool) judge.Finding {
		return judge.Finding{Rule: rule, Subject: subject, State: st, Overdue: overdue, Owner: "team"}
	}
	w := estate.Waiver{Rule: "r.w", Subject: "s", Reason: "x", Owner: "team", ExpiresAt: now}
	prev := judge.Evaluation{Findings: []judge.Finding{
		f("r.fixed", "s", judge.Open, false),
		f("r.dark", "s", judge.Open, false),
		f("r.waived", "s", judge.Open, false),
		f("r.late", "s", judge.Open, false),
		f("r.stays", "s", judge.Open, true),
	}, Waivers: []judge.WaiverView{{Waiver: w, Status: judge.WaiverActive}}}
	next := judge.Evaluation{Findings: []judge.Finding{
		f("r.waived", "s", judge.Waived, false),
		f("r.late", "s", judge.Open, true),
		f("r.stays", "s", judge.Open, true),
		f("r.new", "s", judge.Open, false),
		f("r.soon", "s", judge.Announced, false),
	}, Subjects: []judge.SubjectVerdict{{Key: "s", Unchecked: []judge.Unchecked{{Rule: "r.dark"}}}},
		Waivers: []judge.WaiverView{{Waiver: w, Status: judge.WaiverEnding}}}
	got := map[EventType]string{}
	for _, e := range Diff(prev, next) {
		if e.Finding != nil {
			got[e.Type] += e.Finding.Rule + " "
		} else {
			got[e.Type] += "waiver "
		}
		if e.Owner != "team" {
			t.Errorf("every event names its owner: %+v", e)
		}
	}
	want := map[EventType]string{Opened: "r.new ", Resolved: "r.fixed ", Dark: "r.dark ", Excused: "r.waived ", Overdue: "r.late ", Ending: "waiver "}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("events: %v", got)
	}
}

func TestUnwrittenNamesOnlyRulesTheBoardRuns(t *testing.T) {
	p, err := pack.Parse([]byte(kevPack+`
  - id: image.ci-only
    title: CI only
    kind: image
    severity: low
    standard: s
    pillar: platform/security
    stages: [check]
    needs: { breaking.changes: no release }
    check: size(breaking.changes) == 0
    detail: '"breaks"'
    fix: Fix it.
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	set := &pack.Set{Packs: []*pack.Pack{p}}
	got := Unwritten(set, map[string]bool{"image": true})
	if len(got) != 1 || got[0].Rule != "image.cve-kev" || got[0].Facts[0] != "scan" {
		t.Errorf("the board rule that reads scan, never the pipeline-only rule: %+v", got)
	}
	if Unwritten(set, nil) != nil {
		t.Error("with nothing known, nothing is named")
	}
}

// bringer is a source that brings its own pack.
type bringer struct {
	fn
	packs []*pack.Pack
}

func (b bringer) Packs() []*pack.Pack { return b.packs }

const azurePack = `
pack: azure-policy
title: Azure Policy
version: abc
family: security
rules:
  - id: azure.private
    title: Storage is private
    kind: azure_resource
    severity: high
    standard: s
    pillar: platform/security
    needs: { azure_policy: not read }
    check: azure_policy.state == "Compliant"
    detail: '"non-compliant"'
    fix: Fix it in Azure.
`

func TestABroughtPackIsJudgedAndCarriedWithItsFacts(t *testing.T) {
	p, err := pack.Parse([]byte(azurePack), "source azure")
	if err != nil {
		t.Fatal(err)
	}
	clash, err := pack.Parse([]byte(kevPack), "source rogue")
	if err != nil {
		t.Fatal(err)
	}
	fails := false
	azure := bringer{fn: func(w *estate.Writer) error {
		if fails {
			return errors.New("throttled")
		}
		return w.Subject("azure_resource", "/subscriptions/s/st", "st").Set("azure_policy", map[string]any{"state": "NonCompliant"})
	}, packs: []*pack.Pack{p}}
	rogue := bringer{fn: func(*estate.Writer) error { return nil }, packs: []*pack.Pack{clash}}
	b := newBoard(t, []sources.Named{{Name: "azure", Source: azure, MaxAge: time.Hour}, {Name: "rogue", Source: rogue, MaxAge: time.Hour}}, judge.Options{})
	if len(b.Packs().Packs) != 1 {
		t.Fatalf("before a pass, only the estate's packs: %d", len(b.Packs().Packs))
	}
	first := b.Refresh(context.Background())
	if len(first.Packs.Packs) != 2 || first.Packs.Packs[1].ID != "azure-policy" {
		t.Fatalf("the brought pack joins, and the one that clashes with the estate's does not: %+v", first.Packs.Packs)
	}
	if open := first.Evaluation.Open(); len(open) != 1 || open[0].Rule != "azure.private" {
		t.Fatalf("the brought rule judged: %+v", open)
	}
	if b.Packs() != first.Packs {
		t.Error("the board does not answer with the pass's packs")
	}

	fails = true
	second := b.Refresh(context.Background())
	if open := second.Evaluation.Open(); len(second.Packs.Packs) != 2 || len(open) != 1 || !open[0].Carried {
		t.Fatalf("a failed read keeps the pack and its finding, carried: %d packs, %+v", len(second.Packs.Packs), open)
	}

	br := b.brought["azure"]
	br.at = br.at.Add(-2 * time.Hour)
	b.brought["azure"] = br
	third := b.Refresh(context.Background())
	if len(third.Packs.Packs) != 1 || len(third.Evaluation.Open()) != 0 {
		t.Errorf("a pack older than its source's max age is dropped: %d packs", len(third.Packs.Packs))
	}
}

func TestAPassSaysHowEachSinkTookIt(t *testing.T) {
	src := fn(func(w *estate.Writer) error { w.Subject("image", "ghcr.io/example/app:1.0", "app"); return nil })
	b := newBoard(t, []sources.Named{{Name: "inventory", Source: src, MaxAge: time.Hour}}, judge.Options{}, Sink{Name: "hub", Push: func(context.Context, *Snapshot) error { return errors.New("hub refused") }})
	b.sinks = append(b.sinks, Sink{Name: "hook", Push: func(context.Context, *Snapshot) error { return nil }})
	snap := b.Refresh(context.Background())
	if len(snap.Sent) != 2 || snap.Sent[0].Sink != "hub" || snap.Sent[0].OK || snap.Sent[0].Error != "hub refused" || !snap.Sent[1].OK {
		t.Errorf("each sink's outcome, in order: %+v", snap.Sent)
	}
	if b.Latest() != snap {
		t.Error("the latest snapshot carries the sends")
	}
}

func TestAnAskDuringAPassGetsThePassAfterIt(t *testing.T) {
	entered, release := make(chan struct{}, 4), make(chan struct{})
	passes := 0
	src := fn(func(w *estate.Writer) error {
		passes++
		entered <- struct{}{}
		<-release
		return nil
	})
	b := newBoard(t, []sources.Named{{Name: "slow", Source: src, MaxAge: time.Hour}}, judge.Options{})
	queued := &counter{msg: "pass asked: it runs when the one running ends"}
	b.log = slog.New(queued)
	ctx := context.Background()
	first := make(chan *Snapshot)
	go func() { first <- b.Refresh(ctx) }()
	<-entered

	if b.Refresh(ctx) != nil {
		t.Error("a scheduled refresh during a pass is skipped")
	}
	asks := make(chan *Snapshot, 2)
	for range 2 {
		go func() {
			snap, err := b.Check(ctx)
			if err != nil {
				t.Error(err)
			}
			asks <- snap
		}()
	}
	waitFor(t, func() bool { return queued.n.Load() == 2 })

	release <- struct{}{}
	f := <-first
	<-entered
	release <- struct{}{}
	a, c := <-asks, <-asks
	if a == nil || a != c || !a.RefreshedAt.After(f.RefreshedAt) {
		t.Errorf("both asks share one pass that started after the running one: %v %v", a, c)
	}
	if passes != 2 {
		t.Errorf("two asks during a pass make one more pass, not %d", passes-1)
	}

	gone, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := b.Check(gone); !errors.Is(err, context.Canceled) {
		t.Errorf("a waiter that leaves gets its context's error: %v", err)
	}
	<-entered
	release <- struct{}{}
	waitFor(t, func() bool { b.mu.RLock(); defer b.mu.RUnlock(); return !b.running })
}

// counter is a log handler that counts one message.
type counter struct {
	msg string
	n   atomic.Int32
}

func (c *counter) Enabled(context.Context, slog.Level) bool { return true }
func (c *counter) Handle(_ context.Context, r slog.Record) error {
	if r.Message == c.msg {
		c.n.Add(1)
	}
	return nil
}
func (c *counter) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *counter) WithGroup(string) slog.Handler      { return c }

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
