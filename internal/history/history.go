// Package history keeps what the board judged over time, so a finding, a
// subject's tier, and a pack's share of conformant subjects each have a
// trend. It records changes, never copies: a finding is one span from when
// it opened to when it closed, a tier is one span per value it held, and a
// pack has one row of totals per day. Everything is keyed by pack, rule,
// subject kind, and subject key, the board's own names, so a new pack or a
// new source has history with no change here.
package history

import (
	"context"
	"sort"
	"time"

	"github.com/orchard-suite/trellis/internal/board"
	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/pack"
)

// Store is where history lives.
type Store interface {
	// Record writes what changed in one pass.
	Record(ctx context.Context, p Pass) error
	// Subject is one subject's tier spans, finding spans, and the rule
	// titles it was found by, over the last window.
	Subject(ctx context.Context, kind, key string, since time.Time) (SubjectHistory, error)
	// Rule is one rule's daily open count over the last window.
	Rule(ctx context.Context, rule string, since time.Time) ([]Point, error)
	// Pack is one pack's daily totals over the last window.
	Pack(ctx context.Context, pack string, since time.Time) ([]Day, error)
	// Opened is when each open finding, keyed rule/subject, began.
	Opened(ctx context.Context) (map[string]time.Time, error)
	// Prune drops spans that closed, and days, before cutoff.
	Prune(ctx context.Context, cutoff time.Time) error
	Close()
}

// Span is one value held from From until To; To is zero while it still
// holds.
type Span struct {
	Pack  string    `json:"pack"`
	Rule  string    `json:"rule,omitempty"`
	Kind  string    `json:"kind"`
	Key   string    `json:"subject"`
	Value string    `json:"value"`
	Rank  int       `json:"rank"`
	From  time.Time `json:"from"`
	To    time.Time `json:"to,omitzero"`
	// Cause is why a finding opened: rule when its pack's version changed
	// in the same pass, subject otherwise.
	Cause string `json:"cause,omitempty"`
}

// Open says whether the span still holds.
func (s Span) Open() bool { return s.To.IsZero() }

// Day is one pack's totals at the end of one day.
type Day struct {
	Pack       string         `json:"pack"`
	Date       string         `json:"date"`
	Subjects   int            `json:"subjects"`
	Conformant int            `json:"conformant"`
	Findings   int            `json:"findings"`
	Waived     int            `json:"waived"`
	Overdue    int            `json:"overdue"`
	BySeverity map[string]int `json:"by_severity"`
	Tiers      map[string]int `json:"tiers"`
}

// Share is the percent of judged subjects that were conformant.
func (d Day) Share() float64 {
	if d.Subjects == 0 {
		return 0
	}
	return float64(d.Conformant) * 100 / float64(d.Subjects)
}

// Point is one value on one day.
type Point struct {
	Date  string  `json:"date"`
	Value float64 `json:"value"`
}

// SubjectHistory is everything kept about one subject.
type SubjectHistory struct {
	Tiers    []Span `json:"tiers"`
	Findings []Span `json:"findings"`
}

// Pass is what one pass judged, in the shape history keeps: the tier each
// subject holds in each tiered pack, the findings that count, the ones
// that had no data, each counted pack's totals, and each pack's version.
type Pass struct {
	At       time.Time
	Tiers    []Span
	Findings []Span
	// Unknown are the rule/subject pairs that had no data: a finding open
	// on one stays open, since nobody can say it was fixed.
	Unknown  map[string]bool
	Days     []Day
	Versions map[string]string
}

// FromSnapshot shapes one snapshot, by the packs the pass judged with. Only
// what counts keeps history: a draft or an announced rule's findings never
// open a span.
func FromSnapshot(snap *board.Snapshot) Pass {
	at := snap.RefreshedAt.UTC()
	p := Pass{At: at, Unknown: map[string]bool{}, Versions: map[string]string{}}
	ev := snap.Evaluation
	ranks := map[string][]string{}
	var packs []*pack.Pack
	if snap.Packs != nil {
		packs = snap.Packs.Packs
	}
	for _, pk := range packs {
		ranks[pk.ID] = pk.Tiers
		p.Versions[pk.ID] = pk.Version
	}
	for _, sv := range ev.Subjects {
		for id, tier := range sv.Tiers {
			p.Tiers = append(p.Tiers, Span{Pack: id, Kind: sv.Kind, Key: sv.Key, Value: tier, Rank: Rank(ranks[id], tier), From: at})
		}
		for _, u := range sv.Unchecked {
			p.Unknown[spanKey(Span{Pack: u.Pack, Rule: u.Rule, Kind: sv.Kind, Key: sv.Key})] = true
		}
	}
	for _, f := range ev.Open() {
		p.Findings = append(p.Findings, Span{Pack: f.Pack, Rule: f.Rule, Kind: f.Kind, Key: f.Subject, Value: string(f.Severity), From: at})
	}
	date := at.Format(time.DateOnly)
	for _, pv := range ev.Packs {
		if pv.State == pack.Draft {
			continue
		}
		p.Days = append(p.Days, dayOf(pv, ev.Open(), date))
	}
	sort.Slice(p.Tiers, func(i, j int) bool { return spanKey(p.Tiers[i]) < spanKey(p.Tiers[j]) })
	sort.Slice(p.Findings, func(i, j int) bool { return spanKey(p.Findings[i]) < spanKey(p.Findings[j]) })
	return p
}

func dayOf(pv judge.PackVerdict, open []judge.Finding, date string) Day {
	d := Day{Pack: pv.Pack, Date: date, Subjects: pv.Red + pv.Amber + pv.Green, Conformant: pv.Green, Findings: pv.Findings, Waived: pv.Waived, Overdue: pv.Overdue, BySeverity: map[string]int{}, Tiers: map[string]int{}}
	for _, f := range open {
		if f.Pack == pv.Pack {
			d.BySeverity[string(f.Severity)]++
		}
	}
	for t, n := range pv.Tiers {
		d.Tiers[t] = n
	}
	return d
}

// Rank is a tier's place, 1 for the lowest; 0 for none.
func Rank(tiers []string, tier string) int {
	for i, t := range tiers {
		if t == tier {
			return i + 1
		}
	}
	return 0
}

func spanKey(s Span) string { return s.Pack + "\x00" + s.Rule + "\x00" + s.Kind + "\x00" + s.Key }

// Diff is what Record must change: spans to open, and the keys of open
// spans to close, given the spans open now and the pass. A span whose
// value changed is closed and a new one opened. An open span whose key is
// unknown this pass is left open: no data is not a fix.
func Diff(open []Span, now []Span, unknown map[string]bool) (opened []Span, closed []Span) {
	have := make(map[string]Span, len(open))
	for _, s := range open {
		have[spanKey(s)] = s
	}
	seen := make(map[string]bool, len(now))
	for _, s := range now {
		k := spanKey(s)
		seen[k] = true
		old, ok := have[k]
		if ok && old.Value == s.Value {
			continue
		}
		if ok {
			closed = append(closed, old)
		}
		opened = append(opened, s)
	}
	for k, s := range have {
		if !seen[k] && !unknown[k] {
			closed = append(closed, s)
		}
	}
	sort.Slice(closed, func(i, j int) bool { return spanKey(closed[i]) < spanKey(closed[j]) })
	return opened, closed
}

// Window is how far back the trends sent with each pass reach. The full
// history stays in the store for the board's own pages.
const Window = 90 * 24 * time.Hour
