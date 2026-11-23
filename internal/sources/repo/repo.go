// Package repo reads a repository checkout, for `trellis check` in its
// pipeline: the repository itself with its tree and its build fact, and each API its
// orrery.yaml offers, with the definition read from the checkout and what
// changed since the latest release tag. The packs then judge it exactly as
// the board judges the live estate.
package repo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/trellis/internal/buildfiles"
	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/openapi"
	"github.com/orchard-suite/trellis/internal/sources/definitions"
)

// Source reads one checkout.
type Source struct {
	// Dir is the folder of the app: the repository root, or the folder of
	// one app in a repository of several. Its orrery.yaml, if any, names
	// the APIs.
	Dir string
	// Base is the git ref to compare definitions with; "" is the latest
	// release tag (among those Match selects), "none" compares nothing.
	Base, Match string
	Log         *slog.Logger
}

// Facts are what the source writes. It is built by `trellis check`, never
// from settings.
func (s *Source) Facts() []string {
	return []string{"repo", "forge", "build", "api", "definition", "breaking"}
}

// Collect adds the repository and its APIs.
func (s *Source) Collect(ctx context.Context, e *estate.Writer) error {
	top, err := s.git(ctx, s.Dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("%s is not in a git repository: %w", s.Dir, err)
	}
	root := strings.TrimSpace(top)
	owner, name := remote(ctx, s, root)
	key := "local/" + filepath.Base(root)
	if owner != "" {
		key = "github/" + owner + "/" + name
	}
	files, err := s.git(ctx, root, "ls-files")
	if err != nil {
		return err
	}
	r := e.Subject("repository", key, key)
	_ = r.Set("repo", map[string]any{"owner": owner, "name": name, "archived": false, "fork": false})
	list := strings.Split(strings.TrimSpace(files), "\n")
	_ = r.Set("forge", map[string]any{"empty": len(list) == 0 || list[0] == "", "files": list})
	build, err := buildfiles.Read(list, func(p string) ([]byte, error) { return os.ReadFile(filepath.Join(root, filepath.FromSlash(p))) })
	if err != nil {
		r.Miss("build", "a build file could not be read: "+err.Error())
	} else {
		_ = r.Set("build", build)
	}

	data, err := os.ReadFile(filepath.Join(s.Dir, "orrery.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var d struct {
		Service  string `yaml:"service"`
		Provides []struct {
			ID    string `yaml:"id"`
			Title string `yaml:"title"`
			Kind  string `yaml:"kind"`
			Spec  string `yaml:"spec"`
		} `yaml:"provides"`
	}
	if err := yaml.Unmarshal(data, &d); err != nil {
		return fmt.Errorf("orrery.yaml: %w", err)
	}
	base := s.Base
	if base == "" {
		args := []string{"describe", "--tags", "--abbrev=0"}
		if s.Match != "" {
			args = append(args, "--match", s.Match)
		}
		if tag, err := s.git(ctx, s.Dir, args...); err == nil {
			base = strings.TrimSpace(tag)
		}
	}
	for _, a := range d.Provides {
		api := e.Subject("api", d.Service+"/"+a.ID, a.Title)
		_ = api.Set("api", map[string]any{"kind": a.Kind, "spec": a.Spec, "service": d.Service})
		if a.Spec == "" || strings.Contains(a.Spec, "://") {
			continue
		}
		// A spec is a path from the repository's root, wherever orrery.yaml is.
		file := filepath.Join(root, filepath.FromSlash(a.Spec))
		body, err := os.ReadFile(file)
		if err != nil {
			_ = api.Set("definition", definitions.Definition{Error: err.Error()})
			continue
		}
		def := definitions.Parse(a.Spec, body)
		_ = api.Set("definition", def)
		if def.OpenAPI == nil || base == "" || base == "none" {
			continue
		}
		old, err := s.git(ctx, root, "show", base+":"+path.Clean(filepath.ToSlash(a.Spec)))
		if err != nil {
			s.Log.InfoContext(ctx, "definition is new since the release", "api", api.Key, "base", base)
			continue
		}
		was, err := openapi.Parse([]byte(old))
		if err != nil {
			continue
		}
		now, _ := openapi.Parse(body)
		changes := openapi.Breaking(was, now)
		if changes == nil {
			changes = []string{}
		}
		_ = api.Set("breaking", map[string]any{"base": base, "changes": changes})
	}
	return nil
}

func (s *Source) git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	return string(out), err
}

var githubRemote = regexp.MustCompile(`github\.com[:/]([^/]+)/([^/.]+?)(\.git)?$`)

// remote is the repository's owner and name on GitHub, from origin.
func remote(ctx context.Context, s *Source, root string) (string, string) {
	u, err := s.git(ctx, root, "remote", "get-url", "origin")
	if err != nil {
		return "", ""
	}
	m := githubRemote.FindStringSubmatch(strings.TrimSpace(u))
	if m == nil {
		return "", ""
	}
	return m[1], m[2]
}
