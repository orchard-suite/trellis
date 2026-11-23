package graft

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/trellis/internal/estate"
)

// The environment tiers a runner is named for.
const (
	Production    = "production"
	NonProduction = "nonproduction"
)

// repoFile is the repository file a request desk always protects, so it
// says nothing about what else a repository protects.
const repoFile = "graft.yaml"

// Program is what trellis reads of a program file. Keys it does not name
// are the desk's own and are left alone.
type Program struct {
	Name         string `yaml:"program"`
	Trusted      bool   `yaml:"trusted"`
	Repositories []struct {
		Path      string   `yaml:"path"`
		Entity    string   `yaml:"entity"`
		Protected []string `yaml:"protected"`
	} `yaml:"repositories"`
	Lanes []struct {
		ID        string `yaml:"id"`
		Protected bool   `yaml:"protected"`
		Review    bool   `yaml:"review"`
		SmallFix  bool   `yaml:"small_fix"`
		Outside   bool   `yaml:"outside"`
	} `yaml:"lanes"`
	Limits struct {
		SmallFixLines int     `yaml:"small_fix_lines"`
		SpendUSD      float64 `yaml:"spend_usd"`
	} `yaml:"limits"`
	Runners map[string]struct {
		Production    string `yaml:"production"`
		NonProduction string `yaml:"nonproduction"`
	} `yaml:"runners"`
	Hours struct {
		Schedule string `yaml:"schedule"`
	} `yaml:"hours"`
}

// Parse reads a program file. One without a program name is refused: every
// subject is keyed by it.
func Parse(raw []byte) (*Program, error) {
	var p Program
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, fmt.Errorf("the file names no program")
	}
	return &p, nil
}

// LaneAction is the id of the hub action a lane is: graft_<program>_<lane>,
// in lower case with every other character an underscore.
func LaneAction(program, lane string) string {
	return "graft_" + under(program) + "_" + under(lane)
}

func under(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Lane is a lane's subject with the hub action it is.
type Lane struct {
	Handle *estate.Handle
	Action string
}

// Runner is one runner's subject with the hub source it names.
type Runner struct {
	Handle *estate.Handle
	ID     string
}

// Shape is a pure function from a program file to its subjects: one
// graft_program, one graft_repository for each repository, one
// graft_lane for each lane, and one graft_runner for each runner a
// name has in an environment tier. It returns the lanes and runners, for
// what the hub says of them.
func Shape(w *estate.Writer, p *Program) ([]Lane, []Runner) {
	lanes := []string{}
	review, unreviewed, protected := []string{}, []string{}, []string{}
	for _, l := range p.Lanes {
		lanes = append(lanes, l.ID)
		if l.Review {
			review = append(review, l.ID)
		} else {
			unreviewed = append(unreviewed, l.ID)
		}
		if l.Protected {
			protected = append(protected, l.ID)
		}
	}
	_ = w.Subject("graft_program", p.Name, p.Name).Set("graft_program", map[string]any{
		"name":             p.Name,
		"trusted":          p.Trusted,
		"repositories":     len(p.Repositories),
		"lanes":            lanes,
		"review_lanes":     review,
		"unreviewed_lanes": unreviewed,
		"protected_lanes":  protected,
		// What the file says; zero when it leaves the limit out.
		"small_fix_lines": p.Limits.SmallFixLines,
		"spend_usd":       p.Limits.SpendUSD,
		"hours_schedule":  p.Hours.Schedule,
	})
	for _, r := range p.Repositories {
		repo := strings.Trim(r.Path, "/")
		paths := []string{}
		for _, pp := range r.Protected {
			clean := path.Clean(pp)
			if strings.TrimSpace(pp) == "" || clean == repoFile {
				continue
			}
			if strings.HasSuffix(pp, "/") {
				clean += "/"
			}
			paths = append(paths, clean)
		}
		_ = w.Subject("graft_repository", p.Name+"/"+repo, repo).Set("graft_repository", map[string]any{
			"program":   p.Name,
			"path":      repo,
			"entity":    r.Entity,
			"protected": paths,
		})
	}
	var outLanes []Lane
	for i, l := range p.Lanes {
		action := LaneAction(p.Name, l.ID)
		h := w.Subject("graft_lane", p.Name+"/"+l.ID, p.Name+" lane "+l.ID)
		_ = h.Set("graft_lane", map[string]any{
			"program":   p.Name,
			"id":        l.ID,
			"rank":      i,
			"lowest":    i == 0,
			"protected": l.Protected,
			"review":    l.Review,
			"small_fix": l.SmallFix,
			"outside":   l.Outside,
			"action":    action,
		})
		outLanes = append(outLanes, Lane{Handle: h, Action: action})
	}
	names := make([]string, 0, len(p.Runners))
	for name := range p.Runners {
		names = append(names, name)
	}
	sort.Strings(names)
	var outRunners []Runner
	for _, name := range names {
		r := p.Runners[name]
		for _, t := range []struct{ tier, id, twin string }{
			{Production, r.Production, r.NonProduction},
			{NonProduction, r.NonProduction, r.Production},
		} {
			if t.id == "" {
				continue
			}
			h := w.Subject("graft_runner", p.Name+"/"+name+"/"+t.tier, p.Name+" runner "+name+" ("+t.tier+")")
			_ = h.Set("graft_runner", map[string]any{
				"program": p.Name,
				"name":    name,
				"tier":    t.tier,
				"id":      t.id,
				// The runner the same name has in the other tier; empty when it names none.
				"twin": t.twin,
			})
			outRunners = append(outRunners, Runner{Handle: h, ID: t.id})
		}
	}
	return outLanes, outRunners
}
