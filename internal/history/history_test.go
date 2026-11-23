package history

import (
	"context"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/board"
	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/pack"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func day(n int) time.Time { return t0.AddDate(0, 0, n) }

func TestDiffOpensClosesAndReplacesOnlyWhatChanged(t *testing.T) {
	open := []Span{
		{Pack: "secure", Kind: "storage", Key: "a", Value: "gold", Rank: 3},
		{Pack: "secure", Kind: "storage", Key: "b", Value: "bronze", Rank: 1},
		{Pack: "secure", Kind: "storage", Key: "c", Value: "silver", Rank: 2},
	}
	now := []Span{
		{Pack: "secure", Kind: "storage", Key: "a", Value: "gold", Rank: 3},
		{Pack: "secure", Kind: "storage", Key: "b", Value: "silver", Rank: 2},
		{Pack: "secure", Kind: "storage", Key: "d", Value: "", Rank: 0},
	}
	opened, closed := Diff(open, now, nil)
	if len(opened) != 2 || opened[0].Key != "b" || opened[0].Value != "silver" || opened[1].Key != "d" {
		t.Errorf("opened: %+v", opened)
	}
	if len(closed) != 2 || closed[0].Key != "b" || closed[0].Value != "bronze" || closed[1].Key != "c" {
		t.Errorf("closed: %+v", closed)
	}
}

func TestDiffOfTheSamePassChangesNothing(t *testing.T) {
	s := []Span{{Pack: "p", Rule: "r", Kind: "k", Key: "a", Value: "high"}}
	if o, c := Diff(s, s, nil); len(o) != 0 || len(c) != 0 {
		t.Errorf("a pass that changed nothing wrote %d opens and %d closes", len(o), len(c))
	}
}

func TestDiffKeepsAFindingOpenWhileItHasNoData(t *testing.T) {
	open := []Span{{Pack: "p", Rule: "r.a", Kind: "k", Key: "a", Value: "high"}, {Pack: "p", Rule: "r.b", Kind: "k", Key: "b", Value: "high"}}
	unknown := map[string]bool{spanKey(Span{Pack: "p", Rule: "r.a", Kind: "k", Key: "a"}): true}
	o, c := Diff(open, nil, unknown)
	if len(o) != 0 || len(c) != 1 || c[0].Key != "b" {
		t.Errorf("no data is not a fix, so a stays open; b passed and closes: opened %+v closed %+v", o, c)
	}
}

func TestFromSnapshotKeepsOnlyWhatCounts(t *testing.T) {
	snap := &board.Snapshot{RefreshedAt: t0, Evaluation: judge.Evaluation{
		Subjects: []judge.SubjectVerdict{{Kind: "storage", Key: "a", Tiers: map[string]string{"secure": "silver"}, Unchecked: []judge.Unchecked{{Rule: "storage.key", Pack: "secure"}}}},
		Findings: []judge.Finding{
			{Pack: "secure", Rule: "storage.tls", Kind: "storage", Subject: "a", Severity: estate.High, State: judge.Open},
			{Pack: "secure", Rule: "storage.waived", Kind: "storage", Subject: "a", Severity: estate.High, State: judge.Waived},
			{Pack: "trial", Rule: "trial.x", Kind: "storage", Subject: "a", Severity: estate.Low, State: judge.Draft},
		},
		Packs: []judge.PackVerdict{
			{Pack: "secure", State: "active", Tally: judge.Tally{Green: 3, Amber: 1, Red: 0, Findings: 1, Overdue: 1}},
			{Pack: "trial", State: "draft"},
		},
	}}
	snap.Packs = &pack.Set{Packs: []*pack.Pack{{ID: "secure", Version: "2.0.0", Tiers: []string{"bronze", "silver", "gold"}}, {ID: "trial", Version: "0.1.0", State: pack.Draft}}}
	p := FromSnapshot(snap)
	if len(p.Tiers) != 1 || p.Tiers[0].Rank != 2 {
		t.Errorf("tiers: %+v", p.Tiers)
	}
	if len(p.Findings) != 1 || p.Findings[0].Rule != "storage.tls" || p.Findings[0].Value != "high" {
		t.Errorf("only the open finding opens a span: %+v", p.Findings)
	}
	if !p.Unknown[spanKey(Span{Pack: "secure", Rule: "storage.key", Kind: "storage", Key: "a"})] {
		t.Errorf("the rule with no data is known to be unknown: %+v", p.Unknown)
	}
	if len(p.Days) != 1 || p.Days[0].Subjects != 4 || p.Days[0].Conformant != 3 || p.Days[0].Overdue != 1 || p.Days[0].BySeverity["high"] != 1 || p.Days[0].Share() != 75 {
		t.Errorf("days: %+v", p.Days)
	}
	if p.Versions["secure"] != "2.0.0" || p.Versions["trial"] != "0.1.0" {
		t.Errorf("each pack's version is recorded, so a rule change can be told from a subject change: %+v", p.Versions)
	}
}

func TestVerdictTrendStepsGapsBestAndDirection(t *testing.T) {
	start := WindowStart(day(40), 30*24*time.Hour)
	v, ok := VerdictTrend([]Span{
		// Before the window: clipped to the start of its first day.
		{Pack: "secure", Kind: "storage", Key: "a", Rank: 3, From: day(-30), To: day(20)},
		// Not judged from day 20 to 25: the line drops to none.
		{Pack: "secure", Kind: "storage", Key: "a", Rank: 1, From: day(25)},
	}, start)
	if !ok {
		t.Fatal("no trend for an open verdict")
	}
	want := []float64{3, 0, 1}
	if len(v.Points) != 3 {
		t.Fatalf("points: %+v", v.Points)
	}
	for i, w := range want {
		if v.Points[i].Value != w {
			t.Errorf("point %d is %v, want %v", i, v.Points[i].Value, w)
		}
	}
	if !v.Points[0].At.Equal(day(10).Truncate(24 * time.Hour)) {
		t.Errorf("the first point is %v, want the start of the window's first day", v.Points[0].At)
	}
	if v.Best != 3 || v.Direction != "worse" || !v.Since.Equal(day(25)) {
		t.Errorf("verdict: %+v", v)
	}
	// Closed: gone from the board, so no trend.
	if _, ok := VerdictTrend([]Span{{Pack: "secure", Kind: "storage", Key: "gone", Rank: 2, From: day(1), To: day(5)}}, start); ok {
		t.Error("a closed verdict has a trend")
	}
}

func TestFindingTrendCountsReopenings(t *testing.T) {
	f, ok := FindingTrend([]Span{
		{Pack: "secure", Rule: "storage.tls", Kind: "storage", Key: "a", From: day(1), To: day(3)},
		{Pack: "secure", Rule: "storage.tls", Kind: "storage", Key: "a", From: day(6), To: day(9)},
		{Pack: "secure", Rule: "storage.tls", Kind: "storage", Key: "a", From: day(12), Cause: "rule"},
	})
	if !ok || !f.Opened.Equal(day(1)) || !f.Since.Equal(day(12)) || f.Reopened != 2 || f.Cause != "rule" {
		t.Errorf("finding: %+v", f)
	}
	if _, ok := FindingTrend([]Span{{Rule: "storage.tls", Key: "b", From: day(1), To: day(3)}}); ok {
		t.Error("a closed finding has a trend")
	}
}

func TestPackTrendChanges(t *testing.T) {
	var ds []Day
	for i := 0; i <= 40; i++ {
		ds = append(ds, Day{Pack: "secure", Date: day(i).Format(time.DateOnly), Subjects: 100, Conformant: 40 + i})
	}
	p := PackTrend(ds)
	if len(p.Points) != 41 || p.Points[40].Value != 80 {
		t.Fatalf("points: %d, last %+v", len(p.Points), p.Points[len(p.Points)-1])
	}
	if p.Change7 == nil || *p.Change7 != 7 || p.Change30 == nil || *p.Change30 != 30 {
		t.Errorf("changes: %v %v", p.Change7, p.Change30)
	}
	if short := PackTrend(ds[:4]); short.Change7 != nil {
		t.Error("a 7-day change with no day that old")
	}
}

// oneAtATime is a store that hands out each key's past the way the real
// one does, in one reused slice, and counts the most spans it ever held.
type oneAtATime struct {
	Store
	tiers, findings [][]Span
	most            int
}

func (o *oneAtATime) Record(context.Context, Pass) error { return nil }
func (o *oneAtATime) hand(trails [][]Span, each func([]Span)) error {
	var buf []Span
	for _, tr := range trails {
		buf = append(buf[:0], tr...)
		o.most = max(o.most, len(buf))
		each(buf)
	}
	return nil
}
func (o *oneAtATime) Tiers(_ context.Context, _ time.Time, each func([]Span)) error {
	return o.hand(o.tiers, each)
}
func (o *oneAtATime) Findings(_ context.Context, _ time.Time, each func([]Span)) error {
	return o.hand(o.findings, each)
}
func (o *oneAtATime) Pack(context.Context, string, time.Time) ([]Day, error) {
	return []Day{{Pack: "secure", Date: day(40).Format(time.DateOnly), Subjects: 4, Conformant: 3}}, nil
}

func TestRecorderShapesEachKeysPastAsItArrives(t *testing.T) {
	store := &oneAtATime{}
	for _, key := range []string{"a", "b", "c"} {
		store.tiers = append(store.tiers, []Span{
			{Pack: "secure", Kind: "storage", Key: key, Rank: 1, From: day(1), To: day(5)},
			{Pack: "secure", Kind: "storage", Key: key, Rank: 2, From: day(5)},
		})
		store.findings = append(store.findings, []Span{{Pack: "secure", Rule: "storage.tls", Kind: "storage", Key: key, From: day(2)}})
	}
	snap := &board.Snapshot{RefreshedAt: day(40), Evaluation: judge.Evaluation{Packs: []judge.PackVerdict{{Pack: "secure", State: "active"}}}}
	tr, err := Recorder(store, Window)(context.Background(), snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Verdicts) != 3 || len(tr.Findings) != 3 || len(tr.Packs["secure"].Points) != 1 {
		t.Fatalf("trends: %d verdicts, %d findings, %+v", len(tr.Verdicts), len(tr.Findings), tr.Packs)
	}
	for _, key := range []string{"a", "b", "c"} {
		v := tr.Verdicts["secure/storage/"+key]
		if len(v.Points) != 2 || v.Direction != "better" || !tr.Findings["storage.tls/"+key].Opened.Equal(day(2)) {
			t.Errorf("%s: verdict %+v, finding %+v; a key's trend must not read the slice after the next key reuses it", key, v, tr.Findings["storage.tls/"+key])
		}
	}
	if store.most != 2 {
		t.Errorf("held %d spans at once; a pass holds one key's past at a time", store.most)
	}
}
