// Package buildfiles reads what a repository's build files say: the root
// Makefile's targets, each workflow's runners, steps, and actions, and the
// apps its project files build, by platform. It is pure: a source hands it
// the tree and a way to read one file, and records the result as the
// repository's build fact.
package buildfiles

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Build is a repository's build fact.
type Build struct {
	Make      Make       `json:"make"`
	Workflows []Workflow `json:"workflows"`
	Apps      []App      `json:"apps"`
}

// Make is the root Makefile. Deps holds, for each target, its
// prerequisites and the targets its recipe runs with $(MAKE).
type Make struct {
	Present bool                `json:"present"`
	Default string              `json:"default"`
	Targets []string            `json:"targets"`
	Deps    map[string][]string `json:"deps"`
}

// Workflow is one GitHub Actions workflow file, its jobs flattened: every
// runner a job asks for (with its matrix's values when the runner reads
// the matrix), every step's run script, and every action or reusable
// workflow it uses.
type Workflow struct {
	Path   string   `json:"path"`
	RunsOn []string `json:"runs_on"`
	Runs   []string `json:"runs"`
	Uses   []string `json:"uses"`
}

// App is one app a project file builds: its platform (mac, ios, android,
// windows) and the file that says so.
type App struct {
	Platform string `json:"platform"`
	Path     string `json:"path"`
}

// Platforms.
const (
	Mac     = "mac"
	IOS     = "ios"
	Android = "android"
	Windows = "windows"
)

var makefiles = []string{"GNUmakefile", "makefile", "Makefile"}

// Wanted says whether a path in the tree is a build file Read opens.
func Wanted(p string) bool {
	if p == "Makefile" || p == "makefile" || p == "GNUmakefile" {
		return true
	}
	if strings.HasPrefix(p, ".github/workflows/") && !strings.Contains(strings.TrimPrefix(p, ".github/workflows/"), "/") {
		return strings.HasSuffix(p, ".yml") || strings.HasSuffix(p, ".yaml")
	}
	if aside(p) {
		return false
	}
	base := path.Base(p)
	switch {
	case strings.HasSuffix(p, ".xcodeproj/project.pbxproj"),
		base == "build.gradle", base == "build.gradle.kts",
		base == "package.json",
		strings.HasSuffix(base, ".csproj"):
		return true
	}
	return false
}

// aside says a path is not the repository's own build: installed
// packages, and the fixtures tests read.
func aside(p string) bool {
	for _, seg := range strings.Split(path.Dir(p), "/") {
		switch seg {
		case "node_modules", "testdata", "fixtures", "__fixtures__":
			return true
		}
	}
	return false
}

// Read shapes the build fact from the tree, opening each wanted file with
// read. An error from read fails the whole fact, so it is never partial.
func Read(files []string, read func(path string) ([]byte, error)) (Build, error) {
	b := Build{Make: Make{Targets: []string{}, Deps: map[string][]string{}}, Workflows: []Workflow{}, Apps: []App{}}
	in := map[string]bool{}
	for _, f := range files {
		in[f] = true
	}
	for _, name := range makefiles {
		if !in[name] {
			continue
		}
		data, err := read(name)
		if err != nil {
			return Build{}, fmt.Errorf("%s: %w", name, err)
		}
		b.Make = ParseMakefile(data)
		break
	}
	seen := map[App]bool{}
	for _, f := range files {
		if !Wanted(f) || f == "Makefile" || f == "makefile" || f == "GNUmakefile" {
			continue
		}
		data, err := read(f)
		if err != nil {
			return Build{}, fmt.Errorf("%s: %w", f, err)
		}
		if strings.HasPrefix(f, ".github/workflows/") {
			w, err := ParseWorkflow(f, data)
			if err != nil {
				return Build{}, err
			}
			b.Workflows = append(b.Workflows, w)
			continue
		}
		for _, p := range Platforms(f, data) {
			seen[App{p, f}] = true
		}
	}
	for _, f := range files {
		if path.Base(f) == "tauri.conf.json" && !aside(f) {
			seen[App{Mac, f}] = true
		}
	}
	for a := range seen {
		b.Apps = append(b.Apps, a)
	}
	sort.Slice(b.Apps, func(i, j int) bool {
		if b.Apps[i].Path != b.Apps[j].Path {
			return b.Apps[i].Path < b.Apps[j].Path
		}
		return b.Apps[i].Platform < b.Apps[j].Platform
	})
	return b, nil
}

var (
	sdkroot  = regexp.MustCompile(`\bSDKROOT = "?([a-z]+)"?;`)
	android  = regexp.MustCompile(`com\.android\.application|id\s*\(?\s*["']com\.android\.application["']`)
	winTFM   = regexp.MustCompile(`<TargetFrameworks?>[^<]*-windows`)
	winUI    = regexp.MustCompile(`<(UseWPF|UseWindowsForms|UseWinUI)>\s*true\s*<`)
	appAlias = regexp.MustCompile(`alias\(libs\.plugins\.android\.application\)`)
)

// Platforms says which apps one project file builds.
func Platforms(p string, data []byte) []string {
	base := path.Base(p)
	switch {
	case strings.HasSuffix(p, ".xcodeproj/project.pbxproj"):
		var out []string
		have := map[string]bool{}
		for _, m := range sdkroot.FindAllSubmatch(data, -1) {
			switch string(m[1]) {
			case "iphoneos":
				have[IOS] = true
			case "macosx":
				have[Mac] = true
			}
		}
		for _, k := range []string{IOS, Mac} {
			if have[k] {
				out = append(out, k)
			}
		}
		return out
	case base == "build.gradle" || base == "build.gradle.kts":
		if android.Match(data) || appAlias.Match(data) {
			return []string{Android}
		}
	case base == "package.json":
		var pkg struct {
			Dependencies    map[string]any `json:"dependencies"`
			DevDependencies map[string]any `json:"devDependencies"`
		}
		if json.Unmarshal(data, &pkg) == nil {
			if _, ok := pkg.DevDependencies["electron"]; ok {
				return []string{Mac}
			}
			if _, ok := pkg.Dependencies["electron"]; ok {
				return []string{Mac}
			}
		}
	case strings.HasSuffix(base, ".csproj"):
		if winTFM.Match(data) || winUI.Match(data) {
			return []string{Windows}
		}
	}
	return nil
}

var (
	defaultGoal = regexp.MustCompile(`^\.DEFAULT_GOAL\s*:?=\s*(\S+)`)
	makeCall    = regexp.MustCompile(`\$[({]MAKE[)}]`)
)

// ParseMakefile reads a Makefile's explicit targets, its default goal, and
// what each target depends on or runs with $(MAKE). Targets named by a
// variable or a pattern, and special targets, are left out; an include is
// not followed.
func ParseMakefile(data []byte) Make {
	m := Make{Present: true, Targets: []string{}, Deps: map[string][]string{}}
	seen := map[string]bool{}
	var current []string
	goal := ""
	lines := joinContinued(string(data))
	for _, line := range lines {
		if strings.HasPrefix(line, "\t") {
			for _, t := range calls(line) {
				for _, c := range current {
					m.Deps[c] = appendNew(m.Deps[c], t)
				}
			}
			continue
		}
		code := strings.TrimSpace(stripComment(line))
		if code == "" {
			continue
		}
		if g := defaultGoal.FindStringSubmatch(code); g != nil {
			goal = g[1]
			continue
		}
		targets, prereqs, recipe, ok := splitRule(code)
		if !ok {
			current = nil
			continue
		}
		current = nil
		for _, t := range targets {
			if strings.ContainsAny(t, "$%") || strings.HasPrefix(t, ".") {
				continue
			}
			current = append(current, t)
			if !seen[t] {
				seen[t] = true
				m.Targets = append(m.Targets, t)
			}
			if _, ok := m.Deps[t]; !ok {
				m.Deps[t] = []string{}
			}
			for _, p := range prereqs {
				m.Deps[t] = appendNew(m.Deps[t], p)
			}
			for _, c := range calls(recipe) {
				m.Deps[t] = appendNew(m.Deps[t], c)
			}
		}
	}
	m.Default = goal
	if m.Default == "" && len(m.Targets) > 0 {
		m.Default = m.Targets[0]
	}
	return m
}

func joinContinued(s string) []string {
	var out []string
	var b strings.Builder
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if strings.HasSuffix(l, "\\") {
			b.WriteString(strings.TrimSuffix(l, "\\"))
			b.WriteString(" ")
			continue
		}
		b.WriteString(l)
		out = append(out, b.String())
		b.Reset()
	}
	return out
}

func stripComment(l string) string {
	if i := strings.Index(l, "#"); i >= 0 {
		return l[:i]
	}
	return l
}

// splitRule reads "a b: c d ; recipe". A variable assignment (=, :=, ::=,
// ?=, +=, !=) is not a rule.
func splitRule(code string) (targets, prereqs []string, recipe string, ok bool) {
	i := strings.Index(code, ":")
	if i <= 0 {
		return nil, nil, "", false
	}
	if j := strings.IndexAny(code, "=?+!"); j >= 0 && j < i {
		return nil, nil, "", false
	}
	rest := code[i+1:]
	rest = strings.TrimPrefix(rest, ":")
	if strings.HasPrefix(rest, "=") {
		return nil, nil, "", false
	}
	if strings.HasPrefix(code[:i], "export ") || strings.HasPrefix(code[:i], "define ") {
		return nil, nil, "", false
	}
	if k := strings.Index(rest, ";"); k >= 0 {
		recipe = rest[k+1:]
		rest = rest[:k]
	}
	if strings.Contains(rest, "=") {
		// A target-specific variable: target: VAR = value.
		return nil, nil, "", false
	}
	for _, w := range strings.Fields(strings.ReplaceAll(rest, "|", " ")) {
		if !strings.ContainsAny(w, "$%") {
			prereqs = append(prereqs, w)
		}
	}
	return strings.Fields(code[:i]), prereqs, recipe, true
}

// calls reads the targets a recipe line runs with $(MAKE): the first word
// after the call that is neither a flag, a flag's value, nor an assignment.
func calls(line string) []string {
	var out []string
	locs := makeCall.FindAllStringIndex(line, -1)
	for _, loc := range locs {
		words := strings.Fields(line[loc[1]:])
		for i := 0; i < len(words); i++ {
			w := words[i]
			if w == "-C" || w == "-f" || w == "--directory" || w == "--file" {
				i++
				continue
			}
			if strings.HasPrefix(w, "-") || strings.Contains(w, "=") {
				continue
			}
			if strings.ContainsAny(w, "$;&|") {
				break
			}
			out = append(out, w)
			break
		}
	}
	return out
}

func appendNew(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// ParseWorkflow reads one workflow file's jobs.
func ParseWorkflow(p string, data []byte) (Workflow, error) {
	w := Workflow{Path: p, RunsOn: []string{}, Runs: []string{}, Uses: []string{}}
	var doc struct {
		Jobs map[string]struct {
			RunsOn   yaml.Node `yaml:"runs-on"`
			Uses     string    `yaml:"uses"`
			Strategy struct {
				Matrix yaml.Node `yaml:"matrix"`
			} `yaml:"strategy"`
			Steps []struct {
				Run  string `yaml:"run"`
				Uses string `yaml:"uses"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), &doc); err != nil {
		return Workflow{}, fmt.Errorf("%s: %w", p, err)
	}
	ids := make([]string, 0, len(doc.Jobs))
	for id := range doc.Jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		j := doc.Jobs[id]
		runsOn := scalars(&j.RunsOn)
		for _, r := range runsOn {
			w.RunsOn = appendNew(w.RunsOn, r)
			if strings.Contains(r, "matrix") {
				for _, v := range scalars(&j.Strategy.Matrix) {
					w.RunsOn = appendNew(w.RunsOn, v)
				}
			}
		}
		if j.Uses != "" {
			w.Uses = appendNew(w.Uses, j.Uses)
		}
		for _, s := range j.Steps {
			if s.Run != "" {
				w.Runs = append(w.Runs, s.Run)
			}
			if s.Uses != "" {
				w.Uses = appendNew(w.Uses, s.Uses)
			}
		}
	}
	return w, nil
}

// scalars is every scalar value under a node, in order.
func scalars(n *yaml.Node) []string {
	var out []string
	var walk func(*yaml.Node, bool)
	walk = func(n *yaml.Node, isKey bool) {
		switch n.Kind {
		case yaml.ScalarNode:
			if !isKey && n.Value != "" {
				out = append(out, n.Value)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				walk(n.Content[i+1], false)
			}
		case yaml.SequenceNode, yaml.DocumentNode:
			for _, c := range n.Content {
				walk(c, false)
			}
		}
	}
	walk(n, false)
	return out
}
