// Package meter is trellis's own instruments. Their names are a contract
// with the estate's dashboard and alerts. Labels are bounded sets: rules,
// packs, severities, kinds, pillars, levels, statuses, sources, outcomes;
// never a subject or an owner. A rule that does not count yet, drafted or
// announced, is never in a finding count.
package meter

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/pack"
	"github.com/orchard-suite/trellis/internal/sources"
)

// Service is the OTLP service name and the prefix of every metric.
const Service = "trellis"

// Metrics is the board's own instruments. The gauges read the last
// evaluation at each export; the counters count as things happen.
type Metrics struct {
	sourceRuns metric.Int64Counter
	sourceTime metric.Float64Histogram
	pushes     metric.Int64Counter
	judgments  metric.Int64Counter
	judgeTime  metric.Float64Histogram

	mu        sync.Mutex
	last      *judge.Evaluation
	runs      []sources.Run
	refreshed time.Time
}

// New registers the instruments.
func New() (*Metrics, error) {
	m := otel.Meter(Service)
	var err error
	out := &Metrics{}
	if out.sourceRuns, err = m.Int64Counter("trellis.source.run", metric.WithDescription("Source runs, by source and outcome.")); err != nil {
		return nil, err
	}
	if out.sourceTime, err = m.Float64Histogram("trellis.source.duration", metric.WithUnit("s"), metric.WithDescription("How long each source's run took.")); err != nil {
		return nil, err
	}
	if out.pushes, err = m.Int64Counter("trellis.push", metric.WithDescription("Pushes of findings to the catalog, by outcome.")); err != nil {
		return nil, err
	}
	if out.judgments, err = m.Int64Counter("trellis.judgments", metric.WithDescription("Changes the judge answered or refused, by outcome: no_one, people, refused, or failed.")); err != nil {
		return nil, err
	}
	if out.judgeTime, err = m.Float64Histogram("trellis.judge.duration", metric.WithUnit("s"), metric.WithDescription("How long the judge took to answer one change, proof reads included.")); err != nil {
		return nil, err
	}
	findings, err := m.Int64ObservableGauge("trellis.findings", metric.WithDescription("Open findings a waiver does not cover, by rule, pack, severity, and kind."))
	if err != nil {
		return nil, err
	}
	waived, err := m.Int64ObservableGauge("trellis.waived", metric.WithDescription("Findings a waiver covers, by rule and pack."))
	if err != nil {
		return nil, err
	}
	packVerdict, err := m.Int64ObservableGauge("trellis.pack.verdict", metric.WithDescription("1 for the current level of each pack, 0 for the others."))
	if err != nil {
		return nil, err
	}
	packSubjects, err := m.Int64ObservableGauge("trellis.pack.subjects", metric.WithDescription("Subjects each pack judged, by level."))
	if err != nil {
		return nil, err
	}
	verdict, err := m.Int64ObservableGauge("trellis.verdict", metric.WithDescription("1 for the current level of each kind and of the estate, 0 for the others."))
	if err != nil {
		return nil, err
	}
	subjects, err := m.Int64ObservableGauge("trellis.subjects", metric.WithDescription("Subjects judged, by kind and level."))
	if err != nil {
		return nil, err
	}
	pillarVerdict, err := m.Int64ObservableGauge("trellis.pillar.verdict", metric.WithDescription("1 for the current level of each platform pillar, 0 for the others."))
	if err != nil {
		return nil, err
	}
	overdue, err := m.Int64ObservableGauge("trellis.findings.overdue", metric.WithDescription("Open findings past their deadline, by rule, pack, and severity."))
	if err != nil {
		return nil, err
	}
	results, err := m.Int64ObservableGauge("trellis.rule.results", metric.WithDescription("How each rule answered across its subjects, by rule, pack, and status: pass, fail, nodata, na, waived."))
	if err != nil {
		return nil, err
	}
	facts, err := m.Int64ObservableGauge("trellis.source.facts", metric.WithDescription("Facts a source could not read this pass, by source and state: missed (none to carry) or carried (an earlier reading kept)."))
	if err != nil {
		return nil, err
	}
	estateShare, err := m.Float64ObservableGauge("trellis.conformant", metric.WithUnit("%"), metric.WithDescription("The percent of judged subjects that are green, for the estate and each pack."))
	if err != nil {
		return nil, err
	}
	age, err := m.Float64ObservableGauge("trellis.refresh.age", metric.WithUnit("s"), metric.WithDescription("Seconds since the board last refreshed."))
	if err != nil {
		return nil, err
	}
	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		out.mu.Lock()
		defer out.mu.Unlock()
		if out.last == nil {
			return nil
		}
		o.ObserveFloat64(age, time.Since(out.refreshed).Seconds())
		for _, r := range out.last.Rules {
			for status, n := range map[string]int{"pass": r.Pass, "fail": r.Fail, "nodata": r.NoData, "na": r.NA, "waived": r.Waived} {
				o.ObserveInt64(results, int64(n), metric.WithAttributes(attribute.String("rule", r.Rule), attribute.String("pack", r.Pack), attribute.String("status", status)))
			}
			if r.Phase != pack.Counted {
				continue
			}
			o.ObserveInt64(findings, int64(r.Fail), metric.WithAttributes(
				attribute.String("rule", r.Rule), attribute.String("pack", r.Pack), attribute.String("severity", string(r.Severity)), attribute.String("kind", r.Kind)))
			o.ObserveInt64(waived, int64(r.Waived), metric.WithAttributes(attribute.String("rule", r.Rule), attribute.String("pack", r.Pack)))
			o.ObserveInt64(overdue, int64(r.Overdue), metric.WithAttributes(attribute.String("rule", r.Rule), attribute.String("pack", r.Pack), attribute.String("severity", string(r.Severity))))
		}
		for _, run := range out.runs {
			o.ObserveInt64(facts, int64(run.Missed), metric.WithAttributes(attribute.String("source", run.Source), attribute.String("state", "missed")))
			o.ObserveInt64(facts, int64(run.Carried), metric.WithAttributes(attribute.String("source", run.Source), attribute.String("state", "carried")))
		}
		if c := out.last.Estate.Conformant; c != nil {
			o.ObserveFloat64(estateShare, *c, metric.WithAttributes(attribute.String("pack", "estate")))
		}
		levels := estate.Levels
		for _, pl := range out.last.Pillars {
			for _, l := range levels {
				o.ObserveInt64(pillarVerdict, one(pl.Level == l), metric.WithAttributes(attribute.String("pillar", pl.Pillar), attribute.String("level", string(l))))
			}
		}
		for _, pk := range out.last.Packs {
			if pk.State == pack.Draft {
				continue
			}
			if pk.Conformant != nil {
				o.ObserveFloat64(estateShare, *pk.Conformant, metric.WithAttributes(attribute.String("pack", pk.Pack)))
			}
			counts := map[estate.Level]int{estate.Red: pk.Red, estate.Amber: pk.Amber, estate.Green: pk.Green, estate.Grey: pk.Grey}
			for _, l := range levels {
				attrs := metric.WithAttributes(attribute.String("pack", pk.Pack), attribute.String("level", string(l)))
				o.ObserveInt64(packVerdict, one(pk.Level == l), attrs)
				o.ObserveInt64(packSubjects, int64(counts[l]), attrs)
			}
		}
		for _, l := range levels {
			o.ObserveInt64(verdict, one(out.last.Estate.Level == l), metric.WithAttributes(attribute.String("kind", "estate"), attribute.String("level", string(l))))
		}
		for _, k := range out.last.Kinds {
			counts := map[estate.Level]int{estate.Red: k.Red, estate.Amber: k.Amber, estate.Green: k.Green, estate.Grey: k.Grey}
			for _, l := range levels {
				attrs := metric.WithAttributes(attribute.String("kind", k.Kind), attribute.String("level", string(l)))
				o.ObserveInt64(verdict, one(k.Level == l), attrs)
				o.ObserveInt64(subjects, int64(counts[l]), attrs)
			}
		}
		return nil
	}, findings, waived, overdue, results, facts, estateShare, packVerdict, packSubjects, verdict, subjects, pillarVerdict, age)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func one(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// SourceRun records one source's run.
func (m *Metrics) SourceRun(ctx context.Context, source, outcome string, d time.Duration) {
	attrs := metric.WithAttributes(attribute.String("source", source), attribute.String("outcome", outcome))
	m.sourceRuns.Add(ctx, 1, attrs)
	m.sourceTime.Record(ctx, d.Seconds(), metric.WithAttributes(attribute.String("source", source)))
}

// Refreshed stores the pass the gauges read.
func (m *Metrics) Refreshed(ev *judge.Evaluation, runs []sources.Run, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.last = ev
	m.runs = runs
	m.refreshed = at
}

// Push counts one push of findings by outcome.
func (m *Metrics) Push(ctx context.Context, outcome string) {
	m.pushes.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// Judged counts one change the judge answered (its route, no_one or
// people) or did not (refused or failed), and how long it took.
func (m *Metrics) Judged(ctx context.Context, outcome string, d time.Duration) {
	if m == nil {
		return
	}
	m.judgments.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	m.judgeTime.Record(ctx, d.Seconds())
}
