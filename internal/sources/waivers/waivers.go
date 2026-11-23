// Package waivers reads waivers from a YAML file kept in git, for an estate
// whose catalog keeps none, and for `trellis check` in a pipeline, where an
// acknowledged break is waived in the repository it is made in. A waiver
// here is held to the same rules as one from a catalog: a rule, a subject,
// a reason, an owner, and an end.
package waivers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/sources"
)

// File is a waiver file: one list of waivers.
type File struct {
	Waivers []Entry `yaml:"waivers"`
}

// Entry is one waiver as the file writes it.
type Entry struct {
	Rule      string    `yaml:"rule"`
	Subject   string    `yaml:"subject"`
	Reason    string    `yaml:"reason"`
	Owner     string    `yaml:"owner"`
	Category  string    `yaml:"category"`
	GrantedAt time.Time `yaml:"granted_at"`
	ExpiresAt time.Time `yaml:"expires_at"`
	ReviewAt  time.Time `yaml:"review_at"`
	Control   string    `yaml:"control"`
	Match     string    `yaml:"match"`
}

// Source reads one file.
type Source struct{ Path string }

func init() {
	sources.Register("waivers", func(node *yaml.Node, env sources.Env) (sources.Source, error) {
		var c struct {
			File string `yaml:"file"`
		}
		if err := sources.Decode(node, &c); err != nil {
			return nil, err
		}
		if c.File == "" {
			return nil, errors.New("the waivers source needs file")
		}
		if !filepath.IsAbs(c.File) {
			c.File = filepath.Join(env.Dir, c.File)
		}
		return &Source{Path: c.File}, nil
	})
}

// Collect adds the file's waivers to the estate.
func (s *Source) Collect(_ context.Context, w *estate.Writer) error {
	ws, err := Read(s.Path)
	if err != nil {
		return err
	}
	w.AddWaivers(ws)
	return nil
}

// Read parses a waiver file strictly.
func Read(path string) ([]estate.Waiver, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := make([]estate.Waiver, 0, len(f.Waivers))
	for _, w := range f.Waivers {
		out = append(out, estate.Waiver{Rule: w.Rule, Subject: w.Subject, Reason: w.Reason, Owner: w.Owner, Category: estate.Category(w.Category), GrantedAt: w.GrantedAt, ExpiresAt: w.ExpiresAt, ReviewAt: w.ReviewAt, Control: w.Control, Match: w.Match})
	}
	return out, nil
}
