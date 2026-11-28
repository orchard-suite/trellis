package gate

import (
	"sync"
	"time"

	"github.com/orchard-suite/platformkit/v3/change"

	"github.com/orchard-suite/trellis/internal/estate"
)

// Catalog is what the judge knows of the things a change touches, from the
// board's last pass: each catalog entity's subjects, how many subjects are
// linked to it, and its safe paths. It is indexed once a pass, so a
// judgment looks each thing up by key and never scans the estate.
type Catalog struct {
	// safePaths is the kind of the subjects that are safe paths, whose
	// fact of the same name holds path, subject_blueprint, subject_key,
	// proven_at, and failed_at. Empty when the estate maps none.
	safePaths string

	mu  sync.RWMutex
	idx *index
}

// NewCatalog is a catalog that reads safe paths from subjects of the kind
// safePaths, or none when it is empty.
func NewCatalog(safePaths string) *Catalog { return &Catalog{safePaths: safePaths} }

// Update indexes a pass's estate. The board calls it after each pass.
func (c *Catalog) Update(e *estate.Estate) {
	idx := &index{at: e.Now, things: map[change.Ref]*thing{}}
	back := e.Backlinks()
	get := func(r change.Ref) *thing {
		t := idx.things[r]
		if t == nil {
			t = &thing{}
			idx.things[r] = t
		}
		return t
	}
	for _, kind := range e.Kinds() {
		for _, s := range e.Of(kind) {
			if kind == c.safePaths && c.safePaths != "" {
				if sp, of, ok := safePathOf(s, c.safePaths); ok {
					t := get(of)
					t.safe = append(t.safe, sp)
				}
				continue
			}
			if s.Ref == nil {
				continue
			}
			t := get(change.Ref{Blueprint: s.Ref.Blueprint, Key: s.Ref.Key})
			t.subjects = append(t.subjects, s)
			t.leaning += len(s.Links) + len(back[estate.Link{Kind: s.Kind, Key: s.Key}])
		}
	}
	c.mu.Lock()
	c.idx = idx
	c.mu.Unlock()
}

func (c *Catalog) current() *index {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.idx
}

// index is one pass's catalog, by entity.
type index struct {
	at     time.Time
	things map[change.Ref]*thing
}

type thing struct {
	subjects []*estate.Subject
	leaning  int
	safe     []safePath
}

// safePath is one way a change reaches a thing without changing it in
// place, and when the watch last proved it and saw it do harm.
type safePath struct {
	path     string
	proven   *time.Time
	failed   *time.Time
	absorbed bool
}

func safePathOf(s *estate.Subject, fact string) (safePath, change.Ref, bool) {
	v, ok := s.Value(fact)
	m, isMap := v.(map[string]any)
	if !ok || !isMap {
		return safePath{}, change.Ref{}, false
	}
	str := func(k string) string { x, _ := m[k].(string); return x }
	at := func(k string) *time.Time {
		t, err := time.Parse(time.RFC3339Nano, str(k))
		if err != nil {
			return nil
		}
		return &t
	}
	of := change.Ref{Blueprint: str("subject_blueprint"), Key: str("subject_key")}
	if of.Blueprint == "" || of.Key == "" || str("path") == "" {
		return safePath{}, change.Ref{}, false
	}
	sp := safePath{path: str("path"), proven: at("proven_at"), failed: at("failed_at")}
	sp.absorbed = sp.proven != nil && (sp.failed == nil || sp.proven.After(*sp.failed))
	return sp, of, true
}

// linked is the board's subjects for the things, by kind.
func (x *index) linked(refs ...change.Ref) map[string][]*estate.Subject {
	if x == nil {
		return nil
	}
	out := map[string][]*estate.Subject{}
	for _, r := range refs {
		if t := x.things[r]; t != nil {
			for _, s := range t.subjects {
				out[s.Kind] = append(out[s.Kind], s)
			}
		}
	}
	return out
}

// held says whether the catalog holds the thing, and how many subjects are
// linked to it; both null before the board has read it.
func (x *index) held(r change.Ref) (any, any) {
	if x == nil {
		return nil, nil
	}
	t := x.things[r]
	if t == nil || len(t.subjects) == 0 {
		return false, float64(0)
	}
	return true, float64(t.leaning)
}

// safe is the thing's safe paths as a rule reads them, and whether path is
// one the watch proved after it last did harm. The paths are null before
// the board has read the catalog.
func (x *index) safe(r change.Ref, path string) (any, bool) {
	if x == nil {
		return nil, false
	}
	t := x.things[r]
	if t == nil {
		return []any{}, false
	}
	out := make([]any, 0, len(t.safe))
	absorbed := false
	stamp := func(t *time.Time) any {
		if t == nil {
			return nil
		}
		return t.UTC().Format(time.RFC3339)
	}
	for _, sp := range t.safe {
		out = append(out, map[string]any{"path": sp.path, "proven": sp.absorbed, "proven_at": stamp(sp.proven), "failed_at": stamp(sp.failed)})
		if sp.path == path && sp.absorbed {
			absorbed = true
		}
	}
	return out, absorbed
}
