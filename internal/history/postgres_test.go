package history

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestPostgresKeepsSpansAcrossPasses runs against a real database when
// TRELLIS_TEST_POSTGRES names an empty one it may use.
func TestPostgresKeepsSpansAcrossPasses(t *testing.T) {
	url := os.Getenv("TRELLIS_TEST_POSTGRES")
	if url == "" {
		t.Skip("TRELLIS_TEST_POSTGRES is not set")
	}
	ctx := context.Background()
	// A database a release before this one made: no cause, no overdue,
	// no pack versions. Open must bring it up to date.
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `DROP TABLE IF EXISTS spans, days, pack_versions;
CREATE TABLE spans (id bigserial PRIMARY KEY, what text NOT NULL, pack text NOT NULL, rule text NOT NULL DEFAULT '', subject_kind text NOT NULL, subject_key text NOT NULL, value text NOT NULL, rank int NOT NULL DEFAULT 0, opened_at timestamptz NOT NULL, closed_at timestamptz);
CREATE TABLE days (pack text NOT NULL, day date NOT NULL, subjects int NOT NULL, conformant int NOT NULL, findings int NOT NULL, waived int NOT NULL, by_severity jsonb NOT NULL, tiers jsonb NOT NULL, PRIMARY KEY (pack, day));
INSERT INTO spans (what, pack, rule, subject_kind, subject_key, value, opened_at) VALUES ('finding', 'secure', 'storage.old', 'storage', 'a', 'high', now() - interval '20 days');`); err != nil {
		t.Fatal(err)
	}
	conn.Close(ctx)

	p, err := Open(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	tls := Span{Pack: "secure", Rule: "storage.tls", Kind: "storage", Key: "a", Value: "high"}
	old := Span{Pack: "secure", Rule: "storage.old", Kind: "storage", Key: "a", Value: "high"}
	pass := func(n int, version string, findings []Span, unknown ...Span) {
		t.Helper()
		u := map[string]bool{}
		for _, s := range unknown {
			u[spanKey(s)] = true
		}
		for i := range findings {
			findings[i].From = day(n)
		}
		if err := p.Record(ctx, Pass{At: day(n), Findings: findings, Unknown: u, Versions: map[string]string{"secure": version}, Days: []Day{{Pack: "secure", Date: day(n).Format("2006-01-02"), Subjects: 2, Conformant: 1, Overdue: 1, BySeverity: map[string]int{}, Tiers: map[string]int{}}}}); err != nil {
			t.Fatal(err)
		}
	}
	pass(1, "1.0.0", []Span{old})
	pass(2, "1.1.0", []Span{old, tls})
	// The old finding's facts could not be read: it stays open.
	pass(3, "1.1.0", []Span{tls}, old)
	opened, err := p.Opened(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened) != 2 || !opened["storage.tls/a"].Equal(day(2)) {
		t.Errorf("both stay open, the new one since the pass it opened in: %v", opened)
	}
	causes := map[string]string{}
	if err := p.Findings(ctx, day(0), func(spans []Span) {
		causes[spans[0].Rule] = spans[len(spans)-1].Cause
	}); err != nil {
		t.Fatal(err)
	}
	if causes["storage.tls"] != "rule" || causes["storage.old"] != "subject" {
		t.Errorf("the finding that opened as the pack changed is the rule's doing: %v", causes)
	}
	pass(4, "1.1.0", nil)
	if opened, _ = p.Opened(ctx); len(opened) != 0 {
		t.Errorf("a pass that judges both and finds neither closes both: %v", opened)
	}
	// A finding that opens and closes pass after pass leaves a long past.
	// A pass reads it only while the finding is open, whole and oldest
	// first, and reads nothing of a finding that is closed.
	flap := Span{Pack: "secure", Rule: "storage.flap", Kind: "storage", Key: "b", Value: "high"}
	for n := 5; n < 25; n += 2 {
		pass(n, "1.1.0", []Span{flap, tls})
		pass(n+1, "1.1.0", []Span{tls})
	}
	trails := map[string]int{}
	read := func() {
		t.Helper()
		clear(trails)
		if err := p.Findings(ctx, day(0), func(spans []Span) {
			for i, s := range spans {
				if spanKey(s) != spanKey(spans[0]) || (i > 0 && s.From.Before(spans[i-1].From)) {
					t.Errorf("one call held two keys or was out of order: %+v", spans)
				}
			}
			trails[spans[0].Rule] += len(spans)
		}); err != nil {
			t.Fatal(err)
		}
	}
	read()
	// storage.tls closed in pass 4 and opened again in pass 5: two spans.
	if len(trails) != 1 || trails["storage.tls"] != 2 {
		t.Errorf("only the open finding's past is read, not the ten closed spans of one that is closed: %v", trails)
	}
	pass(25, "1.1.0", []Span{flap, tls})
	read()
	if trails["storage.flap"] != 11 || trails["storage.tls"] != 2 {
		t.Errorf("an open finding's whole past is read in one call: %v", trails)
	}
	days, err := p.Pack(ctx, "secure", day(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 25 || days[3].Overdue != 1 {
		t.Errorf("days with overdue counts: %+v", days)
	}
}
