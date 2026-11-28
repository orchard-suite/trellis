package gate

import (
	"context"
	"sync"
	"time"

	"github.com/orchard-suite/platformkit/v3/annals"
	"github.com/orchard-suite/platformkit/v3/change"
)

// Proofs reads what the watch saw for one key, by key, never by scanning
// history. The platform kit's Annals client is one.
type Proofs interface {
	Proof(ctx context.Context, k annals.ProofKey) (*annals.Proof, error)
}

// Proof is the proof the judge holds for one effect: its level, and what it
// was worked out from.
type Proof struct {
	Level string
	// Counts are what the watch saw for the effect's key; nil when Annals
	// was not asked.
	Counts *annals.Proof
	// Read says Annals was asked for the key.
	Read bool
	// Error is why the read gave nothing, when it did not.
	Error string
}

func (p Proof) view() map[string]any {
	v := map[string]any{"level": p.Level, "read": p.Read, "watched": nil, "clean": nil, "harm": nil, "blind": nil, "last_seen": nil, "error": nil}
	if c := p.Counts; c != nil {
		v["watched"], v["clean"], v["harm"], v["blind"] = float64(c.Watched), float64(c.Clean), float64(c.Harm), float64(c.Blind)
		if c.LastSeen != nil {
			v["last_seen"] = c.LastSeen.UTC().Format(time.RFC3339)
		}
	}
	if p.Error != "" {
		v["error"] = p.Error
	}
	return v
}

// Level is how far the watch's counts for a disruption's key prove it does
// no harm. Nothing seen clean is claimed: the writer's table says it, and no
// watch has shown it. Harm seen on the key is claimed too, unless the
// thing's own safe path by the same path was proven after it last did harm:
// a path absorbs an effect only once watched to. Otherwise one clean watch
// is rehearsed, and measured from measuredAfter on. The key read cannot
// tell which artifact a clean watch saw, so rehearsed means the key was
// seen clean, not that this artifact was.
func Level(c *annals.Proof, absorbed bool, measuredAfter int64) string {
	switch {
	case c == nil || c.Clean == 0:
		return change.ProofClaimed
	case c.Harm > 0 && !absorbed:
		return change.ProofClaimed
	case c.Clean >= measuredAfter:
		return change.ProofMeasured
	}
	return change.ProofRehearsed
}

// readers is how many proof reads one judgment runs at once.
const readers = 16

// proofs gives each effect its proof. An exposure is proven by the
// statement itself, so it is claimed and never read. A disruption is read
// by its key, each key once, at most readers at a time, within the limit:
// with no Annals it is claimed, and when Annals does not answer in time, or
// at all, it is unknown, which routes to people.
func (j *Judge) proofs(ctx context.Context, st *change.Statement, proofs Proofs, cat *index) []Proof {
	out := make([]Proof, len(st.Effects))
	keys := map[annals.ProofKey][]int{}
	for i, e := range st.Effects {
		k, ok := j.kinds.Get(e.Kind)
		switch {
		case !ok:
			out[i] = Proof{Level: change.ProofUnknown, Error: "the effect kind " + e.Kind + " is not registered"}
		case k.Family == change.FamilyExposure:
			out[i] = Proof{Level: change.ProofClaimed}
		case proofs == nil:
			out[i] = Proof{Level: change.ProofClaimed, Error: "the estate keeps no watch"}
		default:
			key := annals.ProofKey{Kind: e.Kind, Blueprint: e.Subject.Blueprint, Path: e.Path}
			keys[key] = append(keys[key], i)
		}
	}
	if len(keys) == 0 {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, j.s.Limit)
	defer cancel()
	type read struct {
		counts *annals.Proof
		err    error
	}
	got := make(map[annals.ProofKey]read, len(keys))
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, readers)
	for key := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				mu.Lock()
				got[key] = read{err: ctx.Err()}
				mu.Unlock()
				return
			}
			c, err := proofs.Proof(ctx, key)
			mu.Lock()
			got[key] = read{counts: c, err: err}
			mu.Unlock()
		}()
	}
	wg.Wait()
	for key, at := range keys {
		r := got[key]
		for _, i := range at {
			if r.err != nil || r.counts == nil {
				why := "annals gave no proof"
				if r.err != nil {
					why = r.err.Error()
				}
				out[i] = Proof{Level: change.ProofUnknown, Read: true, Error: why}
				continue
			}
			_, absorbed := cat.safe(st.Effects[i].Subject, st.Effects[i].Path)
			out[i] = Proof{Level: Level(r.counts, absorbed, j.measured), Counts: r.counts, Read: true}
		}
	}
	if failed := countUnknown(out); failed > 0 {
		j.s.Log.Warn("proof could not be read; those effects go to people", "key", st.Key, "effects", failed, "keys", len(keys))
	}
	return out
}

func countUnknown(ps []Proof) int {
	n := 0
	for _, p := range ps {
		if p.Level == change.ProofUnknown {
			n++
		}
	}
	return n
}
