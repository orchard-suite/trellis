// Package sources is the one interface every input to the board meets, and
// the registry that builds them from settings. A source reads one place (a
// catalog, a cluster, a registry, a forge, a vault, the telemetry stores)
// and adds subjects, facts, links, or waivers to the estate. Each source
// package registers its own type and settings, so a new source touches
// nothing outside its package but the one import that brings it in.
package sources

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/pack"
)

// Source reads one place into the estate.
type Source interface {
	// Collect adds what the place knows through w. An error means the read
	// failed as a whole: the board carries the source's last facts while
	// they are young enough, and after that the rules that need them answer
	// no data. A read that fails for one subject is a Miss on its handle.
	Collect(ctx context.Context, w *estate.Writer) error
}

// Declares is a source that names the facts it writes, so the board can
// tell a rule that reads a fact no configured source writes.
type Declares interface {
	Facts() []string
}

// Brings is a source that brings its own rules, made from another engine's,
// such as a cloud's policy engine: the packs its last full read made, none
// before one. The board judges with them beside the estate's packs that
// pass, and keeps them while the source's facts are carried. A brought rule
// passes the engine's verdict through; it never judges the thing itself.
type Brings interface {
	Packs() []*pack.Pack
}

// Env is what a source gets beside its own settings.
type Env struct {
	// Name is the source's name, which stamps what it writes.
	Name string
	Log  *slog.Logger
	// Dir is the folder of the settings file, for relative paths.
	Dir string
}

// Factory builds a source from its settings node.
type Factory func(node *yaml.Node, env Env) (Source, error)

var registry = map[string]Factory{}

// Register makes a source type known. Each source package calls it from
// init.
func Register(typ string, f Factory) {
	if _, dup := registry[typ]; dup {
		panic("source type registered twice: " + typ)
	}
	registry[typ] = f
}

// Types lists the registered source types, sorted.
func Types() []string {
	out := make([]string, 0, len(registry))
	for t := range registry {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// DefaultMaxAge is how long a fact is carried when its source cannot read
// it and its settings name no max_age.
const DefaultMaxAge = 24 * time.Hour

// Named is a built source with the settings every source shares.
type Named struct {
	// Name stamps every fact and waiver the source writes; the type unless
	// the settings name it.
	Name   string
	Type   string
	MaxAge time.Duration
	Source Source
}

// Build makes the source a settings node describes. The node holds type,
// and optionally name and max_age, beside the type's own settings.
func Build(node *yaml.Node, env Env) (Named, error) {
	var head struct {
		Type   string `yaml:"type"`
		Name   string `yaml:"name"`
		MaxAge string `yaml:"max_age"`
	}
	if err := node.Decode(&head); err != nil {
		return Named{}, err
	}
	f, ok := registry[head.Type]
	if !ok {
		return Named{}, fmt.Errorf("line %d: source type %q is not one of %s", node.Line, head.Type, strings.Join(Types(), ", "))
	}
	n := Named{Name: head.Name, Type: head.Type, MaxAge: DefaultMaxAge}
	if n.Name == "" {
		n.Name = head.Type
	}
	if head.MaxAge != "" {
		d, err := time.ParseDuration(head.MaxAge)
		if err != nil {
			return Named{}, fmt.Errorf("source %s: max_age %q is not a duration such as 6h", n.Name, head.MaxAge)
		}
		n.MaxAge = d
	}
	env.Name = n.Name
	src, err := f(own(node), env)
	if err != nil {
		return Named{}, fmt.Errorf("source %s (line %d): %w", n.Name, node.Line, err)
	}
	n.Source = src
	return n, nil
}

// own is the node without the settings every source shares.
func own(n *yaml.Node) *yaml.Node {
	out := &yaml.Node{Kind: yaml.MappingNode, Line: n.Line, Column: n.Column}
	for i := 0; i+1 < len(n.Content); i += 2 {
		switch n.Content[i].Value {
		case "type", "name", "max_age":
		default:
			out.Content = append(out.Content, n.Content[i], n.Content[i+1])
		}
	}
	return out
}

// Decode reads a source's own settings into out, refusing a key out does
// not have.
func Decode(node *yaml.Node, out any) error {
	var buf bytes.Buffer
	if err := yaml.NewEncoder(&buf).Encode(node); err != nil {
		return err
	}
	dec := yaml.NewDecoder(&buf)
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil && err.Error() != "EOF" {
		return err
	}
	return nil
}

// OrDefault is n, or fallback when n is not positive.
func OrDefault(n, fallback int) int {
	if n <= 0 {
		return fallback
	}
	return n
}

// Outcome is how a source's run ended.
type Outcome string

const (
	OK     Outcome = "ok"
	Failed Outcome = "failed"
)

// Run is one source's run in one pass.
type Run struct {
	Source   string        `json:"source"`
	Type     string        `json:"type"`
	Outcome  Outcome       `json:"outcome"`
	Error    string        `json:"error,omitempty"`
	Started  time.Time     `json:"started"`
	Duration time.Duration `json:"duration"`
	// Missed counts the facts it could not read: one for each subject that
	// lacks one, and one for a gap no subject is there to show.
	Missed int `json:"missed"`
	// Gaps are the places it could not read, each with why.
	Gaps []estate.Gap `json:"gaps,omitempty"`
	// Carried counts its facts kept from an earlier pass.
	Carried int `json:"carried"`
}

// Collect runs one source into the estate and says how it went.
func Collect(ctx context.Context, n Named, e *estate.Estate) Run {
	run := Run{Source: n.Name, Type: n.Type, Outcome: OK, Started: time.Now().UTC()}
	if err := n.Source.Collect(ctx, e.Writer(n.Name)); err != nil {
		run.Outcome, run.Error = Failed, err.Error()
	}
	run.Duration = time.Since(run.Started)
	return run
}

// Tally counts each source's missed and carried facts, and names the
// places it could not read, once the pass's facts are settled. A gap no
// subject shows, as a miss or a carried fact, counts as one missed fact, so
// a list that was never read is never a clean run.
func Tally(runs []Run, e *estate.Estate) {
	type place struct{ source, kind, fact string }
	missed, carried := map[string]int{}, map[string]int{}
	shown := map[place]bool{}
	for _, kind := range e.Kinds() {
		for _, s := range e.Of(kind) {
			for name, m := range s.Missed {
				missed[m.Source]++
				shown[place{m.Source, kind, name}] = true
			}
			for name, f := range s.Facts {
				if f.Carried {
					carried[f.Source]++
					shown[place{f.Source, kind, name}] = true
				}
			}
		}
	}
	gaps := map[string][]estate.Gap{}
	for _, g := range e.Gaps() {
		gaps[g.Source] = append(gaps[g.Source], g)
		if !shown[place{g.Source, g.Kind, g.Fact}] {
			missed[g.Source]++
		}
	}
	for i := range runs {
		runs[i].Missed = missed[runs[i].Source]
		runs[i].Carried = carried[runs[i].Source]
		runs[i].Gaps = gaps[runs[i].Source]
	}
}
