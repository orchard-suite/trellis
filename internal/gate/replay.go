package gate

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/orchard-suite/platformkit/v3/annals"
	"github.com/orchard-suite/platformkit/v3/change"
)

// Past is one change as Annals keeps it: the statement its change.stated
// record holds, and what the change then did, from its change.ended and
// change.observed records.
type Past struct {
	Sender    string
	Key       string
	At        time.Time
	Statement change.Statement
	// Partial says the record was stored before Annals kept a statement's
	// own part, so it holds no unknown parts, writer version, artifact, or
	// claim rules: the unknown fence cannot fire on a part of it.
	Partial bool
	// Ended is the change.ended outcome (ok, failed, or refused), or empty
	// when no change.ended record came.
	Ended string
	// Saw is the worst the watch saw over every subject: harm, blind, or
	// clean; empty when no watch looked.
	Saw string
}

// Ran says the change was applied: it ended, and was not refused.
func (p *Past) Ran() bool { return p.Ended == change.OutcomeOK || p.Ended == change.OutcomeFailed }

// sighting is one change.observed record, by the key proof is kept under.
type sighting struct {
	at   time.Time
	keys map[annals.ProofKey]string
}

// History is past changes and what the watch saw, read from Annals' records,
// ready to replay.
type History struct {
	Changes []*Past
	sights  []sighting
}

// Join builds the history from Annals' records: every change.stated record,
// and the change.ended and change.observed records whose cause names one.
// A record of another verb is ignored.
func Join(records []annals.Event) *History {
	h := &History{}
	type id struct{ sender, key string }
	byID := map[id]*Past{}
	for _, ev := range records {
		if ev.Action.Verb == change.EventStated {
			p := &Past{Sender: ev.Source, Key: ev.EventKey, At: ev.At, Statement: ev.StatementOf(), Partial: ev.Statement == nil}
			if p.Statement.Key == "" {
				p.Statement.Key = ev.EventKey
			}
			byID[id{ev.Source, ev.EventKey}] = p
			h.Changes = append(h.Changes, p)
		}
	}
	for _, ev := range records {
		p := byID[id{ev.CauseFrom(), ev.Cause}]
		switch ev.Action.Verb {
		case change.EventEnded:
			if p != nil {
				p.Ended = ev.Action.Outcome
			}
		case change.EventObserved:
			s := sighting{at: ev.At, keys: map[annals.ProofKey]string{}}
			for _, ef := range ev.Effects {
				k := annals.ProofKey{Kind: ef.Kind, Blueprint: ef.Subject.Blueprint, Path: ef.Path}
				s.keys[k] = worse(s.keys[k], ef.Verdict)
				if p != nil {
					p.Saw = worse(p.Saw, ef.Verdict)
				}
			}
			h.sights = append(h.sights, s)
		}
	}
	sort.Slice(h.Changes, func(i, j int) bool { return h.Changes[i].At.Before(h.Changes[j].At) })
	return h
}

// worse is the worse of two verdicts: harm, then blind, then clean, as
// Annals counts a change once per key.
func worse(a, b string) string {
	rank := map[string]int{"": 0, change.VerdictClean: 1, change.VerdictBlind: 2, change.VerdictHarm: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// Tally is what a replay found: over how many past changes, how many the
// pack would have asked of people and passed, and of those it passed, how
// many the watch then saw do harm (misses) or could not see (blind).
type Tally struct {
	Pack    string    `json:"pack"`
	Since   time.Time `json:"since"`
	Until   time.Time `json:"until"`
	Changes int       `json:"changes"`
	Asked   int       `json:"asked"`
	Passed  int       `json:"passed"`
	// Misses are changes passed to no one that the watch saw do harm.
	Misses int `json:"misses"`
	// Blind are changes passed to no one that ran with no watch that
	// could see: never the same as clean.
	Blind int `json:"blind"`
	// Needless are changes asked of people that ran and the watch saw
	// clean.
	Needless int `json:"needless"`
	// Partial are changes judged whose record was stored before Annals
	// kept a statement's own part, so none of their unknown parts was
	// judged.
	Partial int `json:"partial"`
}

// Replay judges every past change in the history with one gate pack as a
// draft, the base pack's counted rules and its own, at the time each was
// stated, with proof as it stood then: what Annals counts now, less what
// the watch saw from that time on. now reads proof by key, each key once.
func (j *Judge) Replay(ctx context.Context, h *History, packID string, now Proofs, since, until time.Time) (Tally, error) {
	p, ok := j.set.Pack(packID)
	if !ok {
		return Tally{}, fmt.Errorf("replay: no gate pack %q; the judge's are %s", packID, j.Pack())
	}
	t := Tally{Pack: p.Ref(), Since: since, Until: until}
	memo := &memo{of: now, got: map[annals.ProofKey]memoed{}}
	for _, c := range h.Changes {
		if c.At.Before(since) || !c.At.Before(until) {
			continue
		}
		var proofs Proofs
		if now != nil {
			proofs = asOf{now: memo, sights: h.sights, at: c.At}
		}
		in := j.prepare(ctx, &c.Statement, proofs, c.At)
		if err := memo.failed(); err != nil {
			return t, fmt.Errorf("replay: annals gave no proof, so nothing can be replayed: %w", err)
		}
		route, _, err := j.route(in, j.draft(p, c.At))
		if err != nil {
			return t, fmt.Errorf("replay %s/%s: %w", c.Sender, c.Key, err)
		}
		t.Changes++
		if c.Partial {
			t.Partial++
		}
		if route.To == change.RoutePeople {
			t.Asked++
			if c.Ran() && c.Saw == change.VerdictClean {
				t.Needless++
			}
			continue
		}
		t.Passed++
		switch {
		case c.Saw == change.VerdictHarm:
			t.Misses++
		case c.Ran() && c.Saw != change.VerdictClean:
			t.Blind++
		}
	}
	return t, nil
}

// asOf is proof as it stood at a time: Annals' counts now, less each
// change.observed record from then on.
type asOf struct {
	now    Proofs
	sights []sighting
	at     time.Time
}

func (a asOf) Proof(ctx context.Context, k annals.ProofKey) (*annals.Proof, error) {
	c, err := a.now.Proof(ctx, k)
	if err != nil || c == nil {
		return c, err
	}
	out := *c
	out.LastSeen = nil
	for _, s := range a.sights {
		v, ok := s.keys[k]
		if !ok || s.at.Before(a.at) {
			continue
		}
		out.Watched--
		switch v {
		case change.VerdictClean:
			out.Clean--
		case change.VerdictHarm:
			out.Harm--
		case change.VerdictBlind:
			out.Blind--
		}
	}
	out.Watched, out.Clean, out.Harm, out.Blind = max(0, out.Watched), max(0, out.Clean), max(0, out.Harm), max(0, out.Blind)
	return &out, nil
}

// memo reads each key once for a whole replay, and keeps the first read
// that failed: a replay on no proof would count nothing true.
type memo struct {
	of  Proofs
	mu  sync.Mutex
	got map[annals.ProofKey]memoed
	err error
}

func (m *memo) failed() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

type memoed struct {
	p   *annals.Proof
	err error
}

func (m *memo) Proof(ctx context.Context, k annals.ProofKey) (*annals.Proof, error) {
	m.mu.Lock()
	r, ok := m.got[k]
	m.mu.Unlock()
	if ok {
		return r.p, r.err
	}
	p, err := m.of.Proof(ctx, k)
	m.mu.Lock()
	m.got[k] = memoed{p, err}
	if err != nil && m.err == nil {
		m.err = err
	}
	m.mu.Unlock()
	return p, err
}
