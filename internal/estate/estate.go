// Package estate is everything trellis judges, as data: subjects of any
// kind, each carrying named facts that sources collected, the links between
// subjects, and the waivers people granted. Every fact keeps where it came
// from and when it was seen, so a verdict can say how old its evidence is.
// Sources fill it through a Writer; the judge reads it. Nothing here knows a
// kind, a source, or a rule by name.
package estate

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Fact is one thing a source learned about a subject: plain JSON data
// (maps, lists, strings, numbers, bools), the source that wrote it, and
// when that source read it.
type Fact struct {
	Value      any       `json:"value"`
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
	// Carried says the value is from an earlier pass: the source could not
	// read it this time, and it is still younger than the source's max age.
	Carried bool `json:"carried,omitempty"`
}

// Miss is a fact a source tried to read for a subject and could not.
type Miss struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}

// Gap is a fact a source could not read for any subject of a kind, because
// the place it reads them from was not read: a list its catalog does not
// have, or one that did not answer. Every subject of the kind misses the
// fact, with the reason.
type Gap struct {
	Source string `json:"source"`
	Kind   string `json:"kind"`
	Fact   string `json:"fact"`
	Reason string `json:"reason"`
}

// Link names another subject this one is tied to, such as the workload an
// image runs in. Links go both ways when a rule reads them.
type Link struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
}

// Subject is one thing a rule can judge: a repository, an image, an API, or
// any kind a source brings.
type Subject struct {
	Kind  string `json:"kind"`
	Key   string `json:"key"`
	Title string `json:"title"`
	// Ref is the catalog entity the subject is, when it is one, so a finding
	// can link to it.
	Ref   *Ref            `json:"ref,omitempty"`
	Facts map[string]Fact `json:"facts"`
	Links []Link          `json:"links,omitempty"`
	// Missed are the facts a source failed to read this pass, by name.
	Missed map[string]Miss `json:"missed,omitempty"`
}

// Ref names a catalog entity.
type Ref struct {
	Blueprint string `json:"blueprint"`
	Key       string `json:"key"`
}

// Value is one fact's data.
func (s *Subject) Value(name string) (any, bool) {
	f, ok := s.Facts[name]
	return f.Value, ok
}

// Values are the facts' data by name, as a rule reads them.
func (s *Subject) Values() map[string]any {
	out := make(map[string]any, len(s.Facts))
	for name, f := range s.Facts {
		out[name] = f.Value
	}
	return out
}

// Plain turns a value into the JSON data model: what json.Unmarshal into an
// any gives back.
func Plain(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Estate is one pass's subjects, by kind and key, and the waivers.
type Estate struct {
	Now time.Time
	mu  sync.Mutex
	// subjects is kind -> key -> subject.
	subjects map[string]map[string]*Subject
	// Waivers are what the waiver stores held; WaiversKnown is false when no
	// store could be read, so every finding stands.
	Waivers      []Waiver
	WaiversKnown bool
	// gaps are the places sources could not read this pass.
	gaps []Gap
}

// New is an empty estate at now.
func New(now time.Time) *Estate {
	return &Estate{Now: now, subjects: map[string]map[string]*Subject{}}
}

// Writer is one source's hand on the estate: what it writes is stamped
// with its name and the pass's time. It reads everything other sources
// wrote before it.
type Writer struct {
	e      *Estate
	source string
}

// Writer is the estate as the named source writes it.
func (e *Estate) Writer(source string) *Writer { return &Writer{e: e, source: source} }

// Source is the writer's source name.
func (w *Writer) Source() string { return w.source }

// Now is the pass's time.
func (w *Writer) Now() time.Time { return w.e.Now }

// Of lists the subjects of one kind, sorted by key.
func (w *Writer) Of(kind string) []*Subject { return w.e.Of(kind) }

// Find returns the subject of kind and key, if there is one.
func (w *Writer) Find(kind, key string) (*Subject, bool) { return w.e.Find(kind, key) }

// Subject returns a handle on the subject of kind and key, adding it with
// title when new.
func (w *Writer) Subject(kind, key, title string) *Handle {
	return &Handle{Subject: w.e.subject(kind, key, title), w: w}
}

// Gap says the source could not read the fact for any subject of the kind,
// with why. A kind and fact two places fill is one gap with both reasons.
func (w *Writer) Gap(kind, fact, reason string) {
	w.e.mu.Lock()
	defer w.e.mu.Unlock()
	for i, g := range w.e.gaps {
		if g.Source == w.source && g.Kind == kind && g.Fact == fact {
			if !strings.Contains(g.Reason, reason) {
				w.e.gaps[i].Reason += "; " + reason
			}
			return
		}
	}
	w.e.gaps = append(w.e.gaps, Gap{Source: w.source, Kind: kind, Fact: fact, Reason: reason})
}

// AddWaivers records what a waiver store held, beside what other stores
// added.
func (w *Writer) AddWaivers(all []Waiver) {
	w.e.mu.Lock()
	defer w.e.mu.Unlock()
	for _, wv := range all {
		wv.From = w.source
		w.e.Waivers = append(w.e.Waivers, wv)
	}
	w.e.WaiversKnown = true
}

// Handle is a subject as one source writes it.
type Handle struct {
	*Subject
	w *Writer
}

// Set records a fact on the subject. v is turned into plain JSON data, so
// a source may hand a struct.
func (h *Handle) Set(name string, v any) error {
	plain, err := Plain(v)
	if err != nil {
		return fmt.Errorf("fact %s on %s %s: %w", name, h.Kind, h.Key, err)
	}
	h.w.e.mu.Lock()
	defer h.w.e.mu.Unlock()
	h.Facts[name] = Fact{Value: plain, Source: h.w.source, ObservedAt: h.w.e.Now}
	delete(h.Missed, name)
	return nil
}

// Miss says the source tried to read the fact and could not, with why. An
// earlier value still young enough is carried in its place.
func (h *Handle) Miss(name, reason string) {
	h.w.e.mu.Lock()
	defer h.w.e.mu.Unlock()
	if _, ok := h.Facts[name]; ok {
		return
	}
	if h.Missed == nil {
		h.Missed = map[string]Miss{}
	}
	h.Missed[name] = Miss{Source: h.w.source, Reason: reason}
}

// Link ties the subject to another, once.
func (h *Handle) Link(kind, key string) {
	h.w.e.mu.Lock()
	defer h.w.e.mu.Unlock()
	for _, l := range h.Links {
		if l.Kind == kind && l.Key == key {
			return
		}
	}
	h.Links = append(h.Links, Link{Kind: kind, Key: key})
}

// SetRef names the catalog entity the subject is.
func (h *Handle) SetRef(r Ref) {
	h.w.e.mu.Lock()
	defer h.w.e.mu.Unlock()
	h.Ref = &r
}

func (e *Estate) subject(kind, key, title string) *Subject {
	e.mu.Lock()
	defer e.mu.Unlock()
	byKey := e.subjects[kind]
	if byKey == nil {
		byKey = map[string]*Subject{}
		e.subjects[kind] = byKey
	}
	s, ok := byKey[key]
	if !ok {
		s = &Subject{Kind: kind, Key: key, Title: title, Facts: map[string]Fact{}}
		byKey[key] = s
	}
	if s.Title == "" {
		s.Title = title
	}
	return s
}

// Find returns the subject of kind and key, if there is one.
func (e *Estate) Find(kind, key string) (*Subject, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.subjects[kind][key]
	return s, ok
}

// Of lists the subjects of one kind, sorted by key.
func (e *Estate) Of(kind string) []*Subject {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*Subject, 0, len(e.subjects[kind]))
	for _, s := range e.subjects[kind] {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Kinds lists every kind with a subject, sorted.
func (e *Estate) Kinds() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.subjects))
	for k := range e.subjects {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Linked is every subject tied to s, either way, by kind.
func (e *Estate) Linked(s *Subject, back map[Link][]Link) map[string][]*Subject {
	out := map[string][]*Subject{}
	seen := map[Link]bool{}
	add := func(l Link) {
		if seen[l] {
			return
		}
		seen[l] = true
		if t, ok := e.Find(l.Kind, l.Key); ok {
			out[l.Kind] = append(out[l.Kind], t)
		}
	}
	for _, l := range s.Links {
		add(l)
	}
	for _, l := range back[Link{Kind: s.Kind, Key: s.Key}] {
		add(l)
	}
	return out
}

// Backlinks indexes every link by its target, so a subject finds the ones
// that point at it.
func (e *Estate) Backlinks() map[Link][]Link {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[Link][]Link{}
	for kind, byKey := range e.subjects {
		for key, s := range byKey {
			for _, l := range s.Links {
				out[l] = append(out[l], Link{Kind: kind, Key: key})
			}
		}
	}
	return out
}

// Gaps are the places sources could not read this pass, sorted by source,
// kind, and fact.
func (e *Estate) Gaps() []Gap {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := append([]Gap(nil), e.gaps...)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Fact < b.Fact
	})
	return out
}

// Carry fills what this pass could not read from the pass before: every
// fact a source missed, every fact of a place a source could not read, and
// every fact of a source that failed outright, whose last reading is
// younger than the source's max age. Older ones stay missing, with why, so
// a rule that needs them says no data. A subject only a failed source or
// an unread place brought comes back with its carried facts. A gap is a
// miss on every subject of its kind that lacks the fact, so a rule that
// needs it says no data with the gap's reason, never a pass.
func (e *Estate) Carry(prev *Estate, failed map[string]bool, maxAge func(source string) time.Duration) {
	gaps := e.Gaps()
	for _, g := range gaps {
		for _, s := range e.Of(g.Kind) {
			e.Writer(g.Source).Subject(g.Kind, s.Key, s.Title).Miss(g.Fact, g.Reason)
		}
	}
	if prev == nil {
		return
	}
	gap := map[Gap]bool{}
	for _, g := range gaps {
		gap[Gap{Source: g.Source, Kind: g.Kind, Fact: g.Fact}] = true
	}
	for _, kind := range prev.Kinds() {
		for _, old := range prev.Of(kind) {
			for name, f := range old.Facts {
				now, exists := e.Find(kind, old.Key)
				var missed bool
				if exists {
					if _, has := now.Facts[name]; has {
						continue
					}
					_, missed = now.Missed[name]
				}
				if !missed && !failed[f.Source] && !gap[Gap{Source: f.Source, Kind: kind, Fact: name}] {
					continue
				}
				age := e.Now.Sub(f.ObservedAt)
				if age > maxAge(f.Source) {
					if exists && !missed {
						h := e.Writer(f.Source).Subject(kind, old.Key, old.Title)
						h.Miss(name, fmt.Sprintf("%s failed; its last reading is %s old", f.Source, age.Round(time.Minute)))
					}
					continue
				}
				s := e.subject(kind, old.Key, old.Title)
				e.mu.Lock()
				f.Carried = true
				s.Facts[name] = f
				delete(s.Missed, name)
				if s.Ref == nil {
					s.Ref = old.Ref
				}
				for _, l := range old.Links {
					if !hasLink(s.Links, l) {
						s.Links = append(s.Links, l)
					}
				}
				e.mu.Unlock()
			}
		}
	}
}

func hasLink(list []Link, l Link) bool {
	for _, x := range list {
		if x == l {
			return true
		}
	}
	return false
}

// Severity is how bad a failed rule is. Critical turns a verdict red; the
// rest turn it amber.
type Severity string

const (
	Critical Severity = "critical"
	High     Severity = "high"
	Medium   Severity = "medium"
	Low      Severity = "low"
)

// Severities is every severity, worst first.
var Severities = []Severity{Critical, High, Medium, Low}

// Valid says whether s is one of the four.
func (s Severity) Valid() bool {
	return s == Critical || s == High || s == Medium || s == Low
}

// Rank orders severities, worst first.
func (s Severity) Rank() int {
	switch s {
	case Critical:
		return 0
	case High:
		return 1
	case Medium:
		return 2
	default:
		return 3
	}
}

// AtLeast says whether s is as bad as floor or worse.
func (s Severity) AtLeast(floor Severity) bool { return s.Rank() <= floor.Rank() }

// Level is a verdict: red, amber, green, or grey when nothing was known.
type Level string

const (
	Red   Level = "red"
	Amber Level = "amber"
	Green Level = "green"
	Grey  Level = "grey"
)

// Levels is every level, worst first.
var Levels = []Level{Red, Amber, Green, Grey}

// Worst is the worse of two levels, where grey is no information.
func Worst(a, b Level) Level {
	rank := map[Level]int{Grey: 0, Green: 1, Amber: 2, Red: 3}
	if rank[a] >= rank[b] {
		return a
	}
	return b
}

// Evidence is one query a source ran and what came back, so a person can
// run it again.
type Evidence struct {
	Source string `json:"source"`
	Query  string `json:"query"`
	Got    string `json:"got"`
}
