// Package board runs the sources, judges the estate, and keeps the latest
// result for the pages, the API, the metrics, and the sinks. Between passes
// it keeps the last estate, so a source that cannot read this time leaves
// its last facts in place while they are young enough, and the findings on
// them stand, marked as carried, instead of vanishing and coming back.
package board

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/meter"
	"github.com/orchard-suite/trellis/internal/pack"
	"github.com/orchard-suite/trellis/internal/sources"
	"github.com/orchard-suite/trellis/internal/trend"
)

// Snapshot is one pass over the estate.
type Snapshot struct {
	RefreshedAt time.Time        `json:"refreshed_at"`
	Took        time.Duration    `json:"took"`
	Runs        []sources.Run    `json:"sources"`
	Evaluation  judge.Evaluation `json:"evaluation"`
	// Packs are the packs the pass judged with: the estate's, and the ones
	// sources brought.
	Packs *pack.Set `json:"-"`
	// Events are what changed since the pass before; none on the first.
	Events []Event `json:"events"`
	// Trends are what history knew after this pass was recorded; nil with
	// no history.
	Trends *trend.Trends `json:"-"`
	// Sent is how each sink took the pass, set once they all have.
	Sent []Send `json:"sent"`
}

// Send is how one sink took a pass.
type Send struct {
	Sink     string        `json:"sink"`
	OK       bool          `json:"ok"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration"`
}

// Recorder keeps a pass and returns the trends it now knows. It runs
// before the sinks, so they can send the trends on.
type Recorder func(ctx context.Context, s *Snapshot) (*trend.Trends, error)

// OpenedAt says when each finding open in history began, so a deadline
// counts from the first time it was seen and not from the last restart.
type OpenedAt func(ctx context.Context) (map[string]time.Time, error)

// Sink receives each snapshot after it is taken. Push says what it could
// not deliver, and never fails the pass.
type Sink struct {
	Name string
	Push func(ctx context.Context, s *Snapshot) error
}

// Board is the scheduler and the latest snapshot.
type Board struct {
	sources []sources.Named
	packs   *pack.Set
	opts    judge.Options
	metrics *meter.Metrics
	sinks   []Sink
	record  Recorder
	opened  OpenedAt
	log     *slog.Logger

	mu        sync.RWMutex
	latest    *Snapshot
	last      *estate.Estate
	firstSeen map[string]time.Time
	// running is true while a pass runs; next is the pass asked for
	// meanwhile, which every ask until it starts shares.
	running bool
	next    *ticket
	// brought is each source's last brought packs and when it read them.
	// Only a refresh touches it, and one runs at a time.
	brought map[string]brought
}

// ticket is a pass someone waits for.
type ticket struct {
	done chan struct{}
	snap *Snapshot
}

// brought is the packs a source brought on its last full read.
type brought struct {
	packs []*pack.Pack
	at    time.Time
}

// New makes a board over sources, run in the order given: the catalog
// first, then the ones that add facts to what it found. opts are the
// estate's positions; the board fills Stage, FirstSeen, and Written.
func New(srcs []sources.Named, packs *pack.Set, opts judge.Options, metrics *meter.Metrics, log *slog.Logger, sinks ...Sink) *Board {
	opts.Stage = pack.Board
	opts.Written = Written(srcs)
	return &Board{sources: srcs, packs: packs, opts: opts, metrics: metrics, sinks: sinks, log: log, firstSeen: map[string]time.Time{}, brought: map[string]brought{}}
}

// Written is every fact the sources say they write, or nil when one of
// them does not say.
func Written(srcs []sources.Named) map[string]bool {
	out := map[string]bool{}
	for _, s := range srcs {
		d, ok := s.Source.(sources.Declares)
		if !ok {
			return nil
		}
		for _, f := range d.Facts() {
			out[f] = true
		}
	}
	return out
}

// Unread is a rule the board runs that reads facts no source writes.
type Unread struct {
	Rule  string
	Facts []string
}

// Unwritten names every rule the board runs that reads a fact no source
// in written writes; none when written is nil, which is not knowing.
func Unwritten(set *pack.Set, written map[string]bool) []Unread {
	if written == nil {
		return nil
	}
	var out []Unread
	for _, r := range set.Rules() {
		if !r.RunsIn(pack.Board) {
			continue
		}
		var missing []string
		for _, f := range r.Reads {
			if !written[f] {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			out = append(out, Unread{Rule: r.ID, Facts: missing})
		}
	}
	return out
}

// Record keeps every pass with r before the sinks see it, and takes each
// open finding's start from opened. A failure is logged and the pass goes
// on without them.
func (b *Board) Record(r Recorder, opened OpenedAt) {
	b.record = r
	b.opened = opened
}

// Packs are the packs the board judged with on its last pass, the
// estate's own before the first.
func (b *Board) Packs() *pack.Set {
	if snap := b.Latest(); snap != nil && snap.Packs != nil {
		return snap.Packs
	}
	return b.packs
}

// Options are the positions the board judges with.
func (b *Board) Options() judge.Options { return b.opts }

// Latest is the newest snapshot, or nil before the first pass finishes.
func (b *Board) Latest() *Snapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.latest
}

// Run refreshes now and then every interval until ctx ends.
func (b *Board) Run(ctx context.Context, interval time.Duration) {
	b.Refresh(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.Refresh(ctx)
		}
	}
}

// Refresh takes one snapshot on the schedule. While a pass runs it is
// skipped, and the latest snapshot is returned.
func (b *Board) Refresh(ctx context.Context) *Snapshot {
	b.mu.Lock()
	if b.running {
		latest := b.latest
		b.mu.Unlock()
		b.log.InfoContext(ctx, "refresh skipped: one is running")
		return latest
	}
	b.running = true
	b.mu.Unlock()
	return b.drive(ctx)
}

// Check asks for a pass that starts after the ask, and waits for it and
// its sends. While a pass runs, the ask is the pass right after it, which
// every ask until then shares. The pass runs to its end even when ctx ends
// first; only the wait stops.
func (b *Board) Check(ctx context.Context) (*Snapshot, error) {
	b.mu.Lock()
	var t *ticket
	if b.running {
		if b.next == nil {
			b.next = &ticket{done: make(chan struct{})}
		}
		t = b.next
		b.mu.Unlock()
		b.log.InfoContext(ctx, "pass asked: it runs when the one running ends")
	} else {
		b.running = true
		t = &ticket{done: make(chan struct{})}
		b.mu.Unlock()
		b.log.InfoContext(ctx, "pass asked: it runs now")
		go b.serve(context.WithoutCancel(ctx), t)
	}
	select {
	case <-t.done:
		return t.snap, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *Board) serve(ctx context.Context, t *ticket) {
	t.snap = b.drive(ctx)
	close(t.done)
}

// drive runs a pass for a caller that set running, then starts the pass
// asked for meanwhile, if any, or clears running.
func (b *Board) drive(ctx context.Context) *Snapshot {
	snap := b.pass(ctx)
	b.mu.Lock()
	t := b.next
	b.next = nil
	if t == nil {
		b.running = false
	}
	b.mu.Unlock()
	if t != nil {
		go b.serve(ctx, t)
	}
	return snap
}

// pass reads every source, judges, records, publishes, and sends.
func (b *Board) pass(ctx context.Context) *Snapshot {
	b.mu.RLock()
	prev, last := b.latest, b.last
	b.mu.RUnlock()

	ctx, span := otel.Tracer(meter.Service).Start(ctx, "refresh", trace.WithNewRoot(), trace.WithAttributes(attribute.String("task", "refresh")))
	defer span.End()
	started := time.Now().UTC()
	e := estate.New(started)
	var runs []sources.Run
	failed := map[string]bool{}
	maxAge := map[string]time.Duration{}
	for _, src := range b.sources {
		run := b.collect(ctx, src, e)
		runs = append(runs, run)
		maxAge[src.Name] = src.MaxAge
		if run.Outcome == sources.Failed {
			failed[src.Name] = true
		} else if br, ok := src.Source.(sources.Brings); ok {
			b.brought[src.Name] = brought{packs: br.Packs(), at: started}
		}
	}
	e.Carry(last, failed, func(source string) time.Duration { return maxAge[source] })
	sources.Tally(runs, e)
	set := b.passPacks(ctx, started)

	opts := b.opts
	opts.Params = judge.Merge(set)
	opts.FirstSeen = b.firstSeenOf(ctx)
	ev := judge.Evaluate(e, set, opts)

	b.mu.Lock()
	seen := map[string]time.Time{}
	for _, f := range ev.Open() {
		seen[f.Key()] = f.FirstSeen
	}
	b.firstSeen = seen
	b.mu.Unlock()

	snap := &Snapshot{RefreshedAt: started, Took: time.Since(started), Runs: runs, Evaluation: ev, Packs: set}
	if prev != nil {
		snap.Events = Diff(prev.Evaluation, ev)
	}
	if b.record != nil {
		t, err := b.record(ctx, snap)
		if err != nil {
			span.RecordError(err)
			b.log.ErrorContext(ctx, "history record failed", "error", err)
		}
		snap.Trends = t
	}
	b.mu.Lock()
	b.latest = snap
	b.last = e
	b.mu.Unlock()

	b.metrics.Refreshed(&snap.Evaluation, runs, snap.RefreshedAt)
	open := ev.Open()
	span.SetAttributes(attribute.String("estate", string(ev.Estate.Level)), attribute.Int("findings", len(open)))
	b.log.InfoContext(ctx, "refreshed", "estate", ev.Estate.Level, "findings", len(open), "overdue", ev.Estate.Overdue, "unchecked", ev.Estate.Unchecked, "subjects", len(ev.Subjects), "events", len(snap.Events), "took", snap.Took.Round(time.Millisecond).String())
	if len(b.sinks) == 0 {
		return snap
	}
	sent := make([]Send, 0, len(b.sinks))
	for _, sink := range b.sinks {
		started := time.Now()
		err := sink.Push(ctx, snap)
		s := Send{Sink: sink.Name, OK: err == nil, Duration: time.Since(started)}
		if err != nil {
			s.Error = err.Error()
			span.RecordError(err)
		}
		sent = append(sent, s)
	}
	// The pass was published before its sends; publish it again with them,
	// as a new value, since readers hold the first without a lock.
	done := *snap
	done.Sent = sent
	b.mu.Lock()
	if b.latest == snap {
		b.latest = &done
	}
	b.mu.Unlock()
	return &done
}

// passPacks is the estate's packs and those each source brought, in the
// sources' order. A source that failed this pass keeps the packs it last
// brought while they are younger than its max age, as its facts are
// carried, so its findings do not leave and come back; older ones are
// dropped, and the facts they read are gone too.
func (b *Board) passPacks(ctx context.Context, now time.Time) *pack.Set {
	var more []*pack.Pack
	for _, src := range b.sources {
		br, ok := b.brought[src.Name]
		if !ok {
			continue
		}
		if age := now.Sub(br.at); age > src.MaxAge {
			delete(b.brought, src.Name)
			b.log.WarnContext(ctx, "a source's brought packs are older than its max age; dropped", "source", src.Name, "age", age.Round(time.Minute).String())
			continue
		}
		more = append(more, br.packs...)
	}
	set, refused := b.packs.With(more)
	for _, err := range refused {
		b.log.ErrorContext(ctx, "brought pack refused", "error", err)
	}
	return set
}

// firstSeenOf answers when a finding first opened: from history when it
// keeps one, else from this process's passes.
func (b *Board) firstSeenOf(ctx context.Context) func(string) (time.Time, bool) {
	b.mu.RLock()
	local := b.firstSeen
	b.mu.RUnlock()
	var kept map[string]time.Time
	if b.opened != nil {
		var err error
		if kept, err = b.opened(ctx); err != nil {
			b.log.ErrorContext(ctx, "history opened read failed; deadlines count from this process's passes", "error", err)
		}
	}
	return func(key string) (time.Time, bool) {
		if t, ok := kept[key]; ok {
			return t, true
		}
		t, ok := local[key]
		return t, ok
	}
}

func (b *Board) collect(ctx context.Context, n sources.Named, e *estate.Estate) sources.Run {
	ctx, span := otel.Tracer(meter.Service).Start(ctx, "source "+n.Name, trace.WithAttributes(attribute.String("source", n.Name), attribute.String("type", n.Type)))
	defer span.End()
	run := sources.Collect(ctx, n, e)
	if run.Outcome == sources.Failed {
		span.SetStatus(codes.Error, run.Error)
		b.log.ErrorContext(ctx, "source failed; its facts are carried while younger than its max age", "source", n.Name, "error", run.Error, "max_age", n.MaxAge.String(), "took", run.Duration.Round(time.Millisecond).String())
	} else {
		b.log.InfoContext(ctx, "source ran", "source", n.Name, "took", run.Duration.Round(time.Millisecond).String())
	}
	b.metrics.SourceRun(ctx, n.Name, string(run.Outcome), run.Duration)
	return run
}
