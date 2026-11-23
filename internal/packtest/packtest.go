// Package packtest holds pack rules to their fixtures: for every rule, a
// subject it fails, one it passes, and one it can say nothing about. The
// built-in packs and an estate's own packs are tested the same way, by
// `go test` and by `trellis test`.
package packtest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/pack"
)

// Fixtures is a fixture file: the time rules judge at, params laid over
// the estate's by pack, and per rule the facts of a failing, a passing,
// and a no-data subject. A case's linked key holds the subjects linked to
// it, by kind, each a map of its facts.
type Fixtures struct {
	Now time.Time `yaml:"now"`
	// Params set what a rule needs that is off or empty by default, such
	// as the roles an estate names, so its cases can fail and pass.
	Params map[string]map[string]any            `yaml:"params"`
	Rules  map[string]map[string]map[string]any `yaml:"rules"`
}

// ReadDir reads every .yaml fixture file in dir, one per pack by habit,
// into one set. A rule with fixtures in two files, or a pack with params
// in two, is refused, and the files must agree on now.
func ReadDir(dir string) (*Fixtures, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("%s holds no fixture file", dir)
	}
	sort.Strings(paths)
	out := &Fixtures{Params: map[string]map[string]any{}, Rules: map[string]map[string]map[string]any{}}
	for _, path := range paths {
		f, err := Read(path)
		if err != nil {
			return nil, err
		}
		if !out.Now.IsZero() && !f.Now.Equal(out.Now) {
			return nil, fmt.Errorf("%s: now %s differs from the other files' %s", path, f.Now, out.Now)
		}
		out.Now = f.Now
		for id, params := range f.Params {
			if _, dup := out.Params[id]; dup {
				return nil, fmt.Errorf("%s: pack %s has params in another file too", path, id)
			}
			out.Params[id] = params
		}
		for id, cases := range f.Rules {
			if _, dup := out.Rules[id]; dup {
				return nil, fmt.Errorf("%s: rule %s has fixtures in another file too", path, id)
			}
			out.Rules[id] = cases
		}
	}
	return out, nil
}

// Read parses a fixture file. Anchors and merge keys are welcome.
func Read(path string) (*Fixtures, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f Fixtures
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.Now.IsZero() {
		f.Now = time.Now().UTC()
	}
	return &f, nil
}

// Run holds every rule of the set to its three fixtures and lists what
// went wrong: a rule with no fixtures, a fixture that names no rule, and a
// rule that answered otherwise than its fixture says.
func Run(set *pack.Set, f *Fixtures) []string {
	var problems []string
	params := judge.Merge(set)
	for id, over := range f.Params {
		if _, ok := set.Pack(id); !ok {
			problems = append(problems, fmt.Sprintf("fixture params name pack %s, which is not switched on", id))
			continue
		}
		merged := map[string]any{}
		for k, v := range params[id] {
			merged[k] = v
		}
		for k, v := range over {
			merged[k] = v
		}
		plain, err := estate.Plain(merged)
		if err != nil {
			problems = append(problems, fmt.Sprintf("fixture params for %s: %v", id, err))
			continue
		}
		params[id], _ = plain.(map[string]any)
	}
	want := []struct {
		name   string
		status pack.Status
	}{{"fail", pack.Fail}, {"pass", pack.Pass}, {"nodata", pack.NoData}}
	for _, r := range set.Rules() {
		cases, ok := f.Rules[r.ID]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s has no fixtures", r.ID))
			continue
		}
		for _, w := range want {
			facts, ok := cases[w.name]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s has no %s fixture", r.ID, w.name))
				continue
			}
			in, err := input(r.Kind, facts, f.Now)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s %s fixture: %v", r.ID, w.name, err))
				continue
			}
			res := r.Judge(in, params[r.Pack])
			if res.Status != w.status {
				problems = append(problems, fmt.Sprintf("%s on its %s fixture answered %s: %s", r.ID, w.name, res.Status, res.Detail))
			}
			if w.status == pack.Fail && res.Status == pack.Fail && (res.Detail == "" || strings.HasPrefix(res.Detail, "cannot judge")) {
				problems = append(problems, fmt.Sprintf("%s fails without a detail", r.ID))
			}
		}
	}
	for id := range f.Rules {
		if _, ok := set.Rule(id); !ok {
			problems = append(problems, fmt.Sprintf("fixture %s names no rule", id))
		}
	}
	sort.Strings(problems)
	return problems
}

// input builds the subject a case describes, with its linked subjects.
func input(kind string, facts map[string]any, now time.Time) (*pack.Prepared, error) {
	e := estate.New(now)
	w := e.Writer("fixture")
	s := w.Subject(kind, "example", "example")
	linked := map[string][]*estate.Subject{}
	for k, v := range facts {
		if k != "linked" {
			if err := s.Set(k, v); err != nil {
				return nil, err
			}
			continue
		}
		byKind, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("linked is a map of kind to a list of subjects' facts")
		}
		for lk, list := range byKind {
			items, _ := list.([]any)
			for i, item := range items {
				m, _ := item.(map[string]any)
				t := w.Subject(lk, fmt.Sprintf("linked-%d", i), "")
				for name, fv := range m {
					if err := t.Set(name, fv); err != nil {
						return nil, err
					}
				}
				linked[lk] = append(linked[lk], t.Subject)
			}
		}
	}
	return pack.Prepare(s.Subject, now, linked), nil
}
