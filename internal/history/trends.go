package history

import (
	"context"
	"time"

	"github.com/orchard-suite/trellis/internal/board"
	"github.com/orchard-suite/trellis/internal/trend"
)

// Reader is what a pass's trends need from a store: the past of each
// verdict held and each finding open now, one at a time, and each pack's
// days. The spans handed to each are oldest first and are reused for the
// next call.
type Reader interface {
	Tiers(ctx context.Context, since time.Time, each func([]Span)) error
	Findings(ctx context.Context, since time.Time, each func([]Span)) error
	Pack(ctx context.Context, pack string, since time.Time) ([]Day, error)
}

// Recorder records each pass in the store and returns the trends over
// window. It holds one verdict's or one finding's past at a time, so what a
// pass needs grows with what is open, never with how much history is kept.
func Recorder(store interface {
	Store
	Reader
}, window time.Duration) board.Recorder {
	return func(ctx context.Context, snap *board.Snapshot) (*trend.Trends, error) {
		pass := FromSnapshot(snap)
		if err := store.Record(ctx, pass); err != nil {
			return nil, err
		}
		since := pass.At.Add(-window)
		start := WindowStart(pass.At, window)
		out := &trend.Trends{Window: window, Verdicts: map[string]trend.Verdict{}, Findings: map[string]trend.Finding{}, Packs: map[string]trend.Pack{}}
		if err := store.Tiers(ctx, since, func(spans []Span) {
			if v, ok := VerdictTrend(spans, start); ok {
				out.Verdicts[trend.VerdictKey(spans[0].Pack, spans[0].Kind, spans[0].Key)] = v
			}
		}); err != nil {
			return nil, err
		}
		if err := store.Findings(ctx, since, func(spans []Span) {
			if f, ok := FindingTrend(spans); ok {
				out.Findings[trend.FindingKey(spans[0].Rule, spans[0].Key)] = f
			}
		}); err != nil {
			return nil, err
		}
		for _, d := range pass.Days {
			days, err := store.Pack(ctx, d.Pack, since)
			if err != nil {
				return nil, err
			}
			out.Packs[d.Pack] = PackTrend(days)
		}
		return out, nil
	}
}

// WindowStart is where the window ending at now begins: on a day, so a
// point clipped to it stays put from one pass of the day to the next.
func WindowStart(now time.Time, window time.Duration) time.Time {
	return now.Add(-window).UTC().Truncate(24 * time.Hour)
}

// VerdictTrend shapes one verdict's tier spans, oldest first, into its
// trend over the window that begins at start. Only a tier held now has
// one: a closed verdict is gone from the board.
func VerdictTrend(spans []Span, start time.Time) (trend.Verdict, bool) {
	if len(spans) == 0 || !spans[len(spans)-1].Open() {
		return trend.Verdict{}, false
	}
	v := trend.Verdict{Since: spans[len(spans)-1].From}
	for i, s := range spans {
		// A gap between spans means the subject was not judged: the line
		// drops to none there rather than drawing the old tier on.
		if i > 0 && spans[i-1].To.Before(s.From) && spans[i-1].To.After(start) {
			v.Points = append(v.Points, trend.Point{At: spans[i-1].To, Value: 0})
		}
		at := s.From
		if at.Before(start) {
			at = start
		}
		v.Points = append(v.Points, trend.Point{At: at, Value: float64(s.Rank)})
		if s.Rank > v.Best {
			v.Best = s.Rank
		}
	}
	v.Direction = trend.DirectionOf(v.Points)
	return v, true
}

// FindingTrend shapes one finding's spans, oldest first, into its past.
// Only a finding open now has one.
func FindingTrend(spans []Span) (trend.Finding, bool) {
	if len(spans) == 0 || !spans[len(spans)-1].Open() {
		return trend.Finding{}, false
	}
	last := spans[len(spans)-1]
	return trend.Finding{Opened: spans[0].From, Since: last.From, Reopened: len(spans) - 1, Cause: last.Cause}, true
}

// PackTrend shapes one pack's days into its share of conformant subjects,
// a point a day, and how that share changed.
func PackTrend(days []Day) trend.Pack {
	p := trend.Pack{Points: make([]trend.Point, 0, len(days))}
	for _, d := range days {
		at, err := time.Parse(time.DateOnly, d.Date)
		if err != nil {
			continue
		}
		p.Points = append(p.Points, trend.Point{At: at, Value: round1(d.Share())})
	}
	if n := len(p.Points); n > 0 {
		latest := p.Points[n-1]
		p.Change7 = changeSince(p.Points, latest, 7)
		p.Change30 = changeSince(p.Points, latest, 30)
	}
	return p
}

// changeSince is the latest share less the share on the newest day at least
// days before it; nil when no day is that old.
func changeSince(points []trend.Point, latest trend.Point, days int) *float64 {
	cut := latest.At.AddDate(0, 0, -days)
	for i := len(points) - 1; i >= 0; i-- {
		if !points[i].At.After(cut) {
			d := round1(latest.Value - points[i].Value)
			return &d
		}
	}
	return nil
}

func round1(v float64) float64 {
	if v < 0 {
		return -float64(int64(-v*10+0.5)) / 10
	}
	return float64(int64(v*10+0.5)) / 10
}
