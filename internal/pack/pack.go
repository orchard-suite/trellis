// Package pack reads rulebooks. A pack is one YAML file: what standard it
// is, its version, its default parameters, its scopes, and its rules. A rule
// is data: the kind of subject it judges, the scope it holds in, the facts
// it needs, when it does not apply, the check, and the words a person reads.
// Checks are CEL expressions over the subject's facts, so a new rule or a
// new pack never needs a new trellis.
package pack

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"cel.dev/cel-go/cel"
	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/trellis/internal/estate"
)

// State is whether a pack or a rule counts. A draft is judged and shown,
// but sets no level, writes no finding to the catalog, and fires no alert:
// it is how a new standard is tried on the real estate first.
type State string

const (
	Active State = "active"
	Draft  State = "draft"
)

// Stage is where a rule runs: on the board against the live estate, in
// check against a repository in its pipeline, or both.
type Stage string

const (
	Board Stage = "board"
	Check Stage = "check"
)

// Pack is one rulebook.
type Pack struct {
	ID      string `yaml:"pack" json:"id"`
	Title   string `yaml:"title" json:"title"`
	About   string `yaml:"about" json:"about"`
	Version string `yaml:"version" json:"version"`
	// Family is the kind of standard: security, or platform. The catalog
	// keeps each family's findings in a list of its own.
	Family string `yaml:"family" json:"family"`
	State  State  `yaml:"status" json:"status"`
	// Params are the pack's defaults; an estate overrides them.
	Params map[string]any `yaml:"params" json:"params,omitempty"`
	// Scopes are named filters, CEL over a subject's facts, that a rule
	// holds in. A subject outside a rule's scope is out of its count, so
	// a pass rate divides only by what the standard means to judge. An
	// estate may redefine a scope.
	Scopes map[string]string `yaml:"scopes" json:"scopes,omitempty"`
	// Tiers are the pack's levels, lowest first, such as baseline and
	// hardened. A subject reaches a tier when no counted rule of that tier
	// or a lower one fails or lacks data.
	Tiers []string `yaml:"tiers" json:"tiers,omitempty"`
	Rules []*Rule  `yaml:"rules" json:"-"`
	// Source is the file the pack came from, or "built in".
	Source string `yaml:"-" json:"source"`

	scopes map[string]cel.Program
}

// Condition is a CEL test and the words a person reads when it holds:
// Because as written, or Explain, a CEL string, when the words come from
// the facts.
type Condition struct {
	If      string `yaml:"if" json:"if"`
	Because string `yaml:"because" json:"because,omitempty"`
	Explain string `yaml:"explain" json:"explain,omitempty"`
	prog    cel.Program
	explain cel.Program
}

// Rule is one reason a subject is not green.
type Rule struct {
	ID    string `yaml:"id" json:"id"`
	Title string `yaml:"title" json:"title"`
	// Aim is the requirement as met, in positive words, for a check mark
	// beside it, such as "Virtual machine has Azure Backup" for the title
	// "Virtual machine has no Azure Backup". Optional: a consumer falls
	// back to the title.
	Aim      string          `yaml:"aim" json:"aim,omitempty"`
	Kind     string          `yaml:"kind" json:"kind"`
	Severity estate.Severity `yaml:"severity" json:"severity"`
	// Standard is the id of the rule in the platform standard this rule
	// enforces, so a finding links back to its reason.
	Standard string `yaml:"standard" json:"standard"`
	// Pillar is the catalog key of the platform pillar the rule serves.
	Pillar string `yaml:"pillar" json:"pillar"`
	// Controls are the controls of outside frameworks the rule covers, as
	// framework:control, such as soc2:CC6.1 or iso27001:A.8.9.
	Controls []string `yaml:"controls" json:"controls,omitempty"`
	// Tier is the pack tier the rule belongs to, when the pack has tiers.
	Tier string `yaml:"tier" json:"tier,omitempty"`
	// Scope names the pack's scope the rule holds in; every subject of its
	// kind when empty.
	Scope string `yaml:"scope" json:"scope,omitempty"`
	// State is draft for a rule tried before it counts; its pack's state
	// when empty.
	State State `yaml:"status" json:"status,omitempty"`
	// From is the day the rule starts to count. Before it the rule is
	// announced: judged and shown with the day it counts from, never
	// counted, so a team sees what is coming.
	From *time.Time `yaml:"from" json:"from,omitempty"`
	// Needs maps a fact path to why nothing is known without it. A path
	// missing on a subject answers no data.
	Needs map[string]string `yaml:"needs" json:"needs,omitempty"`
	// Unknown answers no data when one holds; Skip answers n/a.
	Unknown []*Condition `yaml:"unknown" json:"unknown,omitempty"`
	Skip    []*Condition `yaml:"skip" json:"skip,omitempty"`
	// Check passes when true.
	Check string `yaml:"check" json:"check"`
	// Detail is a CEL string read when the check fails.
	Detail string `yaml:"detail" json:"detail"`
	// Evidence is an optional CEL list of {source, query, got}.
	Evidence string `yaml:"evidence" json:"evidence,omitempty"`
	Fix      string `yaml:"fix" json:"fix"`
	// Stages default to both.
	Stages []Stage `yaml:"stages" json:"stages,omitempty"`

	Pack string `yaml:"-" json:"pack"`
	// Reads are the facts the rule's expressions read, by name.
	Reads []string `yaml:"-" json:"reads"`

	scope                   cel.Program
	check, detail, evidence cel.Program
}

// RunsIn says whether the rule runs in stage.
func (r *Rule) RunsIn(stage Stage) bool {
	if len(r.Stages) == 0 {
		return true
	}
	for _, s := range r.Stages {
		if s == stage {
			return true
		}
	}
	return false
}

// Phase is whether a rule counts at a time.
type Phase string

const (
	// Counted rules set levels, write findings, and fail a gate.
	Counted Phase = "counted"
	// Announced rules count from a later day.
	Announced Phase = "announced"
	// Drafted rules are tried and never counted.
	Drafted Phase = "draft"
)

// PhaseAt is the rule's phase at now.
func (r *Rule) PhaseAt(now time.Time) Phase {
	switch {
	case r.State == Draft:
		return Drafted
	case r.From != nil && now.Before(*r.From):
		return Announced
	}
	return Counted
}

var (
	idRE      = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	ruleRE    = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z0-9][a-z0-9-]*$`)
	kindRE    = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	pathRE    = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[A-Za-z0-9_]+)*$`)
	controlRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*:\S+$`)
)

// Parse reads one pack file and compiles its rules.
func Parse(data []byte, source string) (*Pack, error) {
	var p Pack
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	p.Source = source
	if err := p.Compile(); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return &p, nil
}

// Compile holds a pack to the rules a pack file is held to and compiles
// its scopes and rules. Parse calls it; a source that brings a pack it made
// from another engine's rules calls it too, so a generated pack is judged
// exactly as a written one.
func (p *Pack) Compile() error {
	if p.State == "" {
		p.State = Active
	}
	if err := p.validate(); err != nil {
		return err
	}
	if err := p.compileScopes(nil); err != nil {
		return err
	}
	for _, r := range p.Rules {
		r.Pack = p.ID
		if p.State == Draft {
			r.State = Draft
		}
		if r.State == "" {
			r.State = Active
		}
		if err := r.compile(p); err != nil {
			return fmt.Errorf("rule %s: %w", r.ID, err)
		}
	}
	return nil
}

func (p *Pack) validate() error {
	switch {
	case !idRE.MatchString(p.ID):
		return fmt.Errorf("pack %q is not a lowercase id", p.ID)
	case p.Title == "" || p.Version == "":
		return fmt.Errorf("pack %s needs a title and a version", p.ID)
	case p.Family != "security" && p.Family != "platform":
		return fmt.Errorf("pack %s: family %q is not security or platform", p.ID, p.Family)
	case p.State != Active && p.State != Draft:
		return fmt.Errorf("pack %s: status %q is not active or draft", p.ID, p.State)
	case len(p.Rules) == 0:
		return fmt.Errorf("pack %s has no rules", p.ID)
	}
	for name := range p.Scopes {
		if !kindRE.MatchString(name) {
			return fmt.Errorf("pack %s: scope %q is not a lowercase name", p.ID, name)
		}
	}
	seen := map[string]bool{}
	for _, r := range p.Rules {
		switch {
		case !ruleRE.MatchString(r.ID):
			return fmt.Errorf("rule id %q is not kind.name", r.ID)
		case seen[r.ID]:
			return fmt.Errorf("rule %s is listed twice", r.ID)
		case !kindRE.MatchString(r.Kind):
			return fmt.Errorf("rule %s: kind %q is not a lowercase name", r.ID, r.Kind)
		case !r.Severity.Valid():
			return fmt.Errorf("rule %s: severity %q is not critical, high, medium, or low", r.ID, r.Severity)
		case r.Title == "" || r.Fix == "" || r.Check == "" || r.Detail == "":
			return fmt.Errorf("rule %s needs a title, a check, a detail, and a fix", r.ID)
		case r.Standard == "" || r.Pillar == "":
			return fmt.Errorf("rule %s needs the standard rule it enforces and its pillar", r.ID)
		case r.State != "" && r.State != Active && r.State != Draft:
			return fmt.Errorf("rule %s: status %q is not active or draft", r.ID, r.State)
		}
		if r.Scope != "" {
			if _, ok := p.Scopes[r.Scope]; !ok {
				return fmt.Errorf("rule %s: scope %q is not one of the pack's scopes", r.ID, r.Scope)
			}
		}
		if r.Tier != "" && !contains(p.Tiers, r.Tier) {
			return fmt.Errorf("rule %s: tier %q is not one of the pack's tiers %v", r.ID, r.Tier, p.Tiers)
		}
		if len(p.Tiers) > 0 && r.Tier == "" {
			return fmt.Errorf("rule %s: the pack has tiers %v, so every rule names one", r.ID, p.Tiers)
		}
		for _, c := range r.Controls {
			if !controlRE.MatchString(c) {
				return fmt.Errorf("rule %s: control %q is not framework:control", r.ID, c)
			}
		}
		for path := range r.Needs {
			if !pathRE.MatchString(path) {
				return fmt.Errorf("rule %s: need %q is not a fact path", r.ID, path)
			}
		}
		for _, s := range r.Stages {
			if s != Board && s != Check {
				return fmt.Errorf("rule %s: stage %q is not board or check", r.ID, s)
			}
		}
		seen[r.ID] = true
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// compileScopes compiles the pack's scopes, with an estate's own
// definitions laid over them, and points each rule at its scope.
func (p *Pack) compileScopes(over map[string]string) error {
	for name := range over {
		if _, ok := p.Scopes[name]; !ok {
			return fmt.Errorf("pack %s has no scope %q to redefine", p.ID, name)
		}
	}
	p.scopes = map[string]cel.Program{}
	for name, expr := range p.Scopes {
		if o, ok := over[name]; ok {
			expr = o
			p.Scopes[name] = o
		}
		prog, _, err := program(expr)
		if err != nil {
			return fmt.Errorf("pack %s scope %s: %w", p.ID, name, err)
		}
		p.scopes[name] = prog
	}
	for _, r := range p.Rules {
		r.scope = p.scopes[r.Scope]
	}
	return nil
}

func (r *Rule) compile(p *Pack) error {
	reads := map[string]bool{}
	compile := func(at *cel.Program, expr, what string) error {
		prog, names, err := program(expr)
		if err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		*at = prog
		for n := range names {
			reads[n] = true
		}
		return nil
	}
	if err := compile(&r.check, r.Check, "check"); err != nil {
		return err
	}
	if err := compile(&r.detail, r.Detail, "detail"); err != nil {
		return err
	}
	if r.Evidence != "" {
		if err := compile(&r.evidence, r.Evidence, "evidence"); err != nil {
			return err
		}
	}
	for _, c := range append(append([]*Condition{}, r.Unknown...), r.Skip...) {
		if c.If == "" || (c.Because == "") == (c.Explain == "") {
			return fmt.Errorf("a condition needs if, and one of because or explain")
		}
		if err := compile(&c.prog, c.If, fmt.Sprintf("condition %q", c.If)); err != nil {
			return err
		}
		if c.Explain != "" {
			if err := compile(&c.explain, c.Explain, fmt.Sprintf("condition %q: explain", c.If)); err != nil {
				return err
			}
		}
	}
	if r.Scope != "" {
		_, names, _ := program(p.Scopes[r.Scope])
		for n := range names {
			reads[n] = true
		}
	}
	for path := range r.Needs {
		reads[strings.SplitN(path, ".", 2)[0]] = true
	}
	for n := range reads {
		r.Reads = append(r.Reads, n)
	}
	sort.Strings(r.Reads)
	return nil
}

// Set is the packs an estate judges with, by id, and the estate's tuning.
type Set struct {
	Packs []*Pack
	// Params are the estate's overrides of each pack's parameters, by pack.
	Params map[string]map[string]any
}

// TuningFile is the file in an estate's packs folder that tunes the packs
// switched on: params and scopes, by pack. The board and a pipeline's check
// both read the folder, so both judge with the same tuning.
const TuningFile = "tuning.yaml"

// Tuning is what TuningFile holds.
type Tuning struct {
	Params map[string]map[string]any    `yaml:"params"`
	Scopes map[string]map[string]string `yaml:"scopes"`
}

// Load reads the built-in packs, then every .yaml in dir; a pack in dir
// replaces a built-in one of the same id. on names the packs switched on, in
// order; every other pack is left out. The folder's tuning, when it has
// one, is laid over the packs switched on.
func Load(builtin fs.FS, dir string, on []string) (*Set, error) {
	all := map[string]*Pack{}
	// Packs sit at the top of the folder; testdata and the like below it
	// are not packs.
	err := fs.WalkDir(builtin, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "." {
				return fs.SkipDir
			}
			return nil
		}
		if !isYAML(path) {
			return nil
		}
		data, err := fs.ReadFile(builtin, path)
		if err != nil {
			return err
		}
		p, err := Parse(data, "built in: "+path)
		if err != nil {
			return err
		}
		all[p.ID] = p
		return nil
	})
	if err != nil {
		return nil, err
	}
	if dir != "" {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("packs: %w", err)
		}
		for _, e := range entries {
			if e.IsDir() || !isYAML(e.Name()) || e.Name() == TuningFile {
				continue
			}
			path := filepath.Join(dir, e.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			p, err := Parse(data, path)
			if err != nil {
				return nil, err
			}
			all[p.ID] = p
		}
	}
	if len(on) == 0 {
		for id := range all {
			on = append(on, id)
		}
		sort.Strings(on)
	}
	set := &Set{}
	rules := map[string]string{}
	for _, id := range on {
		p, ok := all[id]
		if !ok {
			known := make([]string, 0, len(all))
			for k := range all {
				known = append(known, k)
			}
			sort.Strings(known)
			return nil, fmt.Errorf("no pack %q; the packs are %s", id, strings.Join(known, ", "))
		}
		for _, r := range p.Rules {
			if other, dup := rules[r.ID]; dup {
				return nil, fmt.Errorf("rule %s is in both %s and %s", r.ID, other, p.ID)
			}
			rules[r.ID] = p.ID
		}
		set.Packs = append(set.Packs, p)
	}
	if dir != "" {
		if err := set.tune(filepath.Join(dir, TuningFile)); err != nil {
			return nil, err
		}
	}
	return set, nil
}

// tune reads the tuning file at path, when there is one.
func (s *Set) tune(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var t Tuning
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&t); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for id := range t.Params {
		if _, ok := s.Pack(id); !ok {
			return fmt.Errorf("%s: params for pack %q, which is not switched on", path, id)
		}
	}
	s.Params = t.Params
	if err := s.Scope(t.Scopes); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Scope lays an estate's own scope definitions over the packs', by pack.
// Scope is exported for a trial that tunes the packs it adds.
func (s *Set) Scope(over map[string]map[string]string) error {
	for id, scopes := range over {
		p, ok := s.Pack(id)
		if !ok {
			return fmt.Errorf("scopes for pack %q, which is not switched on", id)
		}
		if err := p.compileScopes(scopes); err != nil {
			return err
		}
	}
	return nil
}

// Add puts packs into the set, each replacing one of the same id, as a
// trial of drafts on the real estate does.
func (s *Set) Add(more *Set) {
	for _, t := range more.Packs {
		replaced := false
		for i, have := range s.Packs {
			if have.ID == t.ID {
				s.Packs[i], replaced = t, true
			}
		}
		if !replaced {
			s.Packs = append(s.Packs, t)
		}
	}
}

// With is the set and more, the packs a source brought this pass, as a new
// set; the set itself is left as it is. A brought pack whose id or one of
// whose rule ids the set already has is left out, with why, so a source
// can never replace or shadow an estate's own pack.
func (s *Set) With(more []*Pack) (*Set, []error) {
	out := &Set{Packs: append([]*Pack{}, s.Packs...), Params: s.Params}
	rules := map[string]string{}
	for _, r := range s.Rules() {
		rules[r.ID] = r.Pack
	}
	var refused []error
next:
	for _, p := range more {
		if _, dup := out.Pack(p.ID); dup {
			refused = append(refused, fmt.Errorf("pack %s (%s): the set already has a pack of that id", p.ID, p.Source))
			continue
		}
		for _, r := range p.Rules {
			if other, dup := rules[r.ID]; dup {
				refused = append(refused, fmt.Errorf("pack %s (%s): rule %s is already in %s", p.ID, p.Source, r.ID, other))
				continue next
			}
		}
		for _, r := range p.Rules {
			rules[r.ID] = p.ID
		}
		out.Packs = append(out.Packs, p)
	}
	return out, refused
}

func isYAML(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

// Rules lists every rule of the set, pack by pack.
func (s *Set) Rules() []*Rule {
	var out []*Rule
	for _, p := range s.Packs {
		out = append(out, p.Rules...)
	}
	return out
}

// Pack finds a pack of the set.
func (s *Set) Pack(id string) (*Pack, bool) {
	for _, p := range s.Packs {
		if p.ID == id {
			return p, true
		}
	}
	return nil, false
}

// Rule finds a rule of the set.
func (s *Set) Rule(id string) (*Rule, bool) {
	for _, r := range s.Rules() {
		if r.ID == id {
			return r, true
		}
	}
	return nil, false
}
