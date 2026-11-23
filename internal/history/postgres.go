package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TokenFunc returns a password for a new connection, such as an Entra
// access token, which expires; nil keeps the URL's own.
type TokenFunc func(ctx context.Context) (string, error)

// Postgres keeps history in a PostgreSQL database trellis owns.
type Postgres struct {
	pool *pgxpool.Pool
}

const schema = `
CREATE TABLE IF NOT EXISTS spans (
  id           bigserial PRIMARY KEY,
  what         text NOT NULL,
  pack         text NOT NULL,
  rule         text NOT NULL DEFAULT '',
  subject_kind text NOT NULL,
  subject_key  text NOT NULL,
  value        text NOT NULL,
  rank         int  NOT NULL DEFAULT 0,
  opened_at    timestamptz NOT NULL,
  closed_at    timestamptz
);
ALTER TABLE spans ADD COLUMN IF NOT EXISTS cause text NOT NULL DEFAULT 'subject';
CREATE UNIQUE INDEX IF NOT EXISTS spans_open ON spans (what, pack, rule, subject_kind, subject_key) WHERE closed_at IS NULL;
CREATE INDEX IF NOT EXISTS spans_subject ON spans (subject_kind, subject_key, opened_at);
CREATE INDEX IF NOT EXISTS spans_rule ON spans (rule, opened_at) WHERE what = 'finding';
CREATE INDEX IF NOT EXISTS spans_closed ON spans (closed_at) WHERE closed_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS days (
  pack        text NOT NULL,
  day         date NOT NULL,
  subjects    int  NOT NULL,
  conformant  int  NOT NULL,
  findings    int  NOT NULL,
  waived      int  NOT NULL,
  by_severity jsonb NOT NULL,
  tiers       jsonb NOT NULL,
  PRIMARY KEY (pack, day)
);
ALTER TABLE days ADD COLUMN IF NOT EXISTS overdue int NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS pack_versions (
  pack    text NOT NULL,
  version text NOT NULL,
  since   timestamptz NOT NULL,
  PRIMARY KEY (pack, since)
);`

// Open connects and makes the tables it lacks. token, when set, is asked
// for a password before every new connection.
func Open(ctx context.Context, url string, token TokenFunc) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	cfg.MaxConns = 4
	if token != nil {
		cfg.BeforeConnect = func(ctx context.Context, c *pgx.ConnConfig) error {
			pw, err := token(ctx)
			if err != nil {
				return fmt.Errorf("history sign-in: %w", err)
			}
			c.Password = pw
			return nil
		}
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("history schema: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

// Close ends the pool.
func (p *Postgres) Close() { p.pool.Close() }

// Record closes the spans that ended, opens the ones that began, and
// writes each pack's totals for the day, in one transaction.
func (p *Postgres) Record(ctx context.Context, pass Pass) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	changed, err := recordVersions(ctx, tx, pass)
	if err != nil {
		return err
	}
	for _, set := range []struct {
		what    string
		now     []Span
		unknown map[string]bool
	}{{"tier", pass.Tiers, nil}, {"finding", pass.Findings, pass.Unknown}} {
		open, err := openSpans(ctx, tx, set.what)
		if err != nil {
			return err
		}
		opened, closed := Diff(open, set.now, set.unknown)
		for _, s := range closed {
			if _, err := tx.Exec(ctx, `UPDATE spans SET closed_at = $1 WHERE what = $2 AND pack = $3 AND rule = $4 AND subject_kind = $5 AND subject_key = $6 AND closed_at IS NULL`,
				pass.At, set.what, s.Pack, s.Rule, s.Kind, s.Key); err != nil {
				return err
			}
		}
		if len(opened) > 0 {
			rows := make([][]any, 0, len(opened))
			for _, s := range opened {
				cause := "subject"
				if set.what == "finding" && changed[s.Pack] {
					cause = "rule"
				}
				rows = append(rows, []any{set.what, s.Pack, s.Rule, s.Kind, s.Key, s.Value, s.Rank, pass.At, cause})
			}
			if _, err := tx.CopyFrom(ctx, pgx.Identifier{"spans"}, []string{"what", "pack", "rule", "subject_kind", "subject_key", "value", "rank", "opened_at", "cause"}, pgx.CopyFromRows(rows)); err != nil {
				return err
			}
		}
	}
	for _, d := range pass.Days {
		sev, _ := json.Marshal(d.BySeverity)
		tiers, _ := json.Marshal(d.Tiers)
		if _, err := tx.Exec(ctx, `INSERT INTO days (pack, day, subjects, conformant, findings, waived, overdue, by_severity, tiers) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (pack, day) DO UPDATE SET subjects = EXCLUDED.subjects, conformant = EXCLUDED.conformant, findings = EXCLUDED.findings, waived = EXCLUDED.waived, overdue = EXCLUDED.overdue, by_severity = EXCLUDED.by_severity, tiers = EXCLUDED.tiers`,
			d.Pack, d.Date, d.Subjects, d.Conformant, d.Findings, d.Waived, d.Overdue, sev, tiers); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// recordVersions notes each pack whose version differs from the last one
// recorded, and says which changed in this pass. A pack seen for the first
// time is recorded but not called changed.
func recordVersions(ctx context.Context, tx pgx.Tx, pass Pass) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (pack) pack, version FROM pack_versions ORDER BY pack, since DESC`)
	if err != nil {
		return nil, err
	}
	last := map[string]string{}
	for rows.Next() {
		var p, v string
		if err := rows.Scan(&p, &v); err != nil {
			rows.Close()
			return nil, err
		}
		last[p] = v
	}
	rows.Close()
	changed := map[string]bool{}
	for p, v := range pass.Versions {
		was, seen := last[p]
		if seen && was == v {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO pack_versions (pack, version, since) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, p, v, pass.At); err != nil {
			return nil, err
		}
		changed[p] = seen
	}
	return changed, nil
}

// Opened is when each open finding began, keyed rule/subject.
func (p *Postgres) Opened(ctx context.Context) (map[string]time.Time, error) {
	rows, err := p.pool.Query(ctx, `SELECT rule, subject_key, opened_at FROM spans WHERE what = 'finding' AND closed_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var rule, key string
		var at time.Time
		if err := rows.Scan(&rule, &key, &at); err != nil {
			return nil, err
		}
		out[rule+"/"+key] = at
	}
	return out, rows.Err()
}

func openSpans(ctx context.Context, tx pgx.Tx, what string) ([]Span, error) {
	rows, err := tx.Query(ctx, `SELECT pack, rule, subject_kind, subject_key, value, rank, opened_at FROM spans WHERE what = $1 AND closed_at IS NULL`, what)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Span, error) {
		var s Span
		err := r.Scan(&s.Pack, &s.Rule, &s.Kind, &s.Key, &s.Value, &s.Rank, &s.From)
		return s, err
	})
}

// spans reads spans of one kind that were open at any time since.
func (p *Postgres) spans(ctx context.Context, what, where string, since time.Time, args ...any) ([]Span, error) {
	q := `SELECT pack, rule, subject_kind, subject_key, value, rank, opened_at, closed_at, cause FROM spans WHERE what = $1 AND (closed_at IS NULL OR closed_at >= $2)`
	if where != "" {
		q += " AND " + where
	}
	q += " ORDER BY subject_kind, subject_key, pack, rule, opened_at"
	rows, err := p.pool.Query(ctx, q, append([]any{what, since}, args...)...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanSpan)
}

func scanSpan(r pgx.CollectableRow) (Span, error) {
	var s Span
	var to *time.Time
	err := r.Scan(&s.Pack, &s.Rule, &s.Kind, &s.Key, &s.Value, &s.Rank, &s.From, &to, &s.Cause)
	if to != nil {
		s.To = *to
	}
	return s, err
}

// trails hands each the past of every key that holds a span of the kind
// now: that key's spans that held at any time since, oldest first. It holds
// one key's spans at a time, so a pass reads what is open and never the
// whole table.
func (p *Postgres) trails(ctx context.Context, what string, since time.Time, each func([]Span)) error {
	rows, err := p.pool.Query(ctx, `SELECT s.pack, s.rule, s.subject_kind, s.subject_key, s.value, s.rank, s.opened_at, s.closed_at, s.cause
		FROM spans s
		WHERE s.what = $1 AND (s.closed_at IS NULL OR s.closed_at >= $2)
		  AND EXISTS (SELECT 1 FROM spans o WHERE o.what = s.what AND o.pack = s.pack AND o.rule = s.rule
		    AND o.subject_kind = s.subject_kind AND o.subject_key = s.subject_key AND o.closed_at IS NULL)
		ORDER BY s.pack, s.rule, s.subject_kind, s.subject_key, s.opened_at`, what, since)
	if err != nil {
		return err
	}
	defer rows.Close()
	var trail []Span
	for rows.Next() {
		s, err := scanSpan(rows)
		if err != nil {
			return err
		}
		if len(trail) > 0 && spanKey(trail[0]) != spanKey(s) {
			each(trail)
			trail = trail[:0]
		}
		trail = append(trail, s)
	}
	if len(trail) > 0 {
		each(trail)
	}
	return rows.Err()
}

// Tiers hands each the tier spans, since, of one verdict held now.
func (p *Postgres) Tiers(ctx context.Context, since time.Time, each func([]Span)) error {
	return p.trails(ctx, "tier", since, each)
}

// Findings hands each the spans, since, of one finding open now.
func (p *Postgres) Findings(ctx context.Context, since time.Time, each func([]Span)) error {
	return p.trails(ctx, "finding", since, each)
}

// Subject is one subject's spans since.
func (p *Postgres) Subject(ctx context.Context, kind, key string, since time.Time) (SubjectHistory, error) {
	tiers, err := p.spans(ctx, "tier", "subject_kind = $3 AND subject_key = $4", since, kind, key)
	if err != nil {
		return SubjectHistory{}, err
	}
	findings, err := p.spans(ctx, "finding", "subject_kind = $3 AND subject_key = $4", since, kind, key)
	if err != nil {
		return SubjectHistory{}, err
	}
	return SubjectHistory{Tiers: nonNil(tiers), Findings: nonNil(findings)}, nil
}

func nonNil(s []Span) []Span {
	if s == nil {
		return []Span{}
	}
	return s
}

// Rule is how many subjects the rule was open on at the end of each day
// since, and now.
func (p *Postgres) Rule(ctx context.Context, rule string, since time.Time) ([]Point, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT d::date::text, count(s.id)::float8
		FROM generate_series($2::date, current_date, interval '1 day') AS d
		LEFT JOIN spans s ON s.what = 'finding' AND s.rule = $1
		  AND s.opened_at < LEAST(d + interval '1 day', now())
		  AND (s.closed_at IS NULL OR s.closed_at >= LEAST(d + interval '1 day', now()))
		GROUP BY d ORDER BY d`, rule, since)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Point, error) {
		var pt Point
		err := r.Scan(&pt.Date, &pt.Value)
		return pt, err
	})
}

// Pack is the pack's totals for each day since that a pass recorded.
func (p *Postgres) Pack(ctx context.Context, pack string, since time.Time) ([]Day, error) {
	rows, err := p.pool.Query(ctx, `SELECT pack, day::text, subjects, conformant, findings, waived, overdue, by_severity, tiers FROM days WHERE pack = $1 AND day >= $2::date ORDER BY day`, pack, since)
	if err != nil {
		return nil, err
	}
	days, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Day, error) {
		var d Day
		var sev, tiers []byte
		if err := r.Scan(&d.Pack, &d.Date, &d.Subjects, &d.Conformant, &d.Findings, &d.Waived, &d.Overdue, &sev, &tiers); err != nil {
			return d, err
		}
		return d, errors.Join(json.Unmarshal(sev, &d.BySeverity), json.Unmarshal(tiers, &d.Tiers))
	})
	if days == nil {
		days = []Day{}
	}
	return days, err
}

// Prune drops spans that closed, and days, before cutoff.
func (p *Postgres) Prune(ctx context.Context, cutoff time.Time) error {
	if _, err := p.pool.Exec(ctx, `DELETE FROM spans WHERE closed_at < $1`, cutoff); err != nil {
		return err
	}
	_, err := p.pool.Exec(ctx, `DELETE FROM days WHERE day < $1::date`, cutoff)
	return err
}
