// Package command runs a program that knows something about the estate and
// prints it as facts, one JSON object per line:
//
//	{"kind": "repository", "key": "github/acme/app", "title": "app", "fact": "scorecard", "value": {...}}
//
// It is how an estate brings a scanner trellis has no source for, without
// changing trellis: the program reads its tool, and a pack's rules read the
// fact. A line that does not parse fails the run.
package command

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/sources"
)

// Source runs one program.
type Source struct {
	run     []string
	timeout time.Duration
	log     *slog.Logger
}

// New makes the source.
func New(run []string, timeout time.Duration, log *slog.Logger) (*Source, error) {
	if len(run) == 0 {
		return nil, errors.New("a command source needs a program to run")
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return &Source{run: run, timeout: timeout, log: log}, nil
}

func init() {
	sources.Register("command", func(node *yaml.Node, env sources.Env) (sources.Source, error) {
		var c struct {
			Run     []string `yaml:"run"`
			Timeout string   `yaml:"timeout"`
		}
		if err := sources.Decode(node, &c); err != nil {
			return nil, err
		}
		var timeout time.Duration
		if c.Timeout != "" {
			d, err := time.ParseDuration(c.Timeout)
			if err != nil {
				return nil, fmt.Errorf("timeout %q is not a duration", c.Timeout)
			}
			timeout = d
		}
		return New(c.Run, timeout, env.Log)
	})
}

// Line is one fact the program prints.
type Line struct {
	Kind  string          `json:"kind"`
	Key   string          `json:"key"`
	Title string          `json:"title"`
	Fact  string          `json:"fact"`
	Value json.RawMessage `json:"value"`
}

// Collect runs the program and records every fact it prints.
func (s *Source) Collect(ctx context.Context, w *estate.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, s.run[0], s.run[1:]...)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", s.run[0], err, bytes.TrimSpace(errOut.Bytes()))
	}
	n, err := Read(w, out.Bytes())
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "command read", "source", w.Source(), "facts", n)
	return nil
}

// Read records every fact in the program's output.
func Read(e *estate.Writer, output []byte) (int, error) {
	sc := bufio.NewScanner(bytes.NewReader(output))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	n := 0
	for line := 1; sc.Scan(); line++ {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var l Line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			return n, fmt.Errorf("line %d: %w", line, err)
		}
		if l.Kind == "" || l.Key == "" || l.Fact == "" {
			return n, fmt.Errorf("line %d: a fact needs a kind, a key, and a fact name", line)
		}
		var v any
		if err := json.Unmarshal(l.Value, &v); err != nil {
			return n, fmt.Errorf("line %d: value: %w", line, err)
		}
		if err := e.Subject(l.Kind, l.Key, l.Title).Set(l.Fact, v); err != nil {
			return n, err
		}
		n++
	}
	return n, sc.Err()
}
