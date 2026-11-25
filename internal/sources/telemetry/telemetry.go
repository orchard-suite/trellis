// Package telemetry measures services against the telemetry standard. It
// reads one declaration per service from a directory, runs each
// measurement's queries against the metric, log, and trace stores, and
// writes what the stores hold, with the queries and what came back, as the
// service's telemetry fact. It never judges: the telemetry pack's rules
// read the measurements.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/sources"
	"github.com/orchard-suite/trellis/internal/sources/telemetry/declaration"
	"github.com/orchard-suite/trellis/internal/sources/telemetry/probe"
	"github.com/orchard-suite/trellis/internal/sources/telemetry/stores"
)

// Source is the telemetry source.
type Source struct {
	dir string
	src probe.Sources
	opt probe.Options
	// catalog is the blueprint a service's workload is in the catalog, so a
	// finding links to it; "" links none.
	catalog string
	log     *slog.Logger
}

// New makes the source over a directory of declarations. catalog names the
// catalog blueprint a declared service is an entity of, keyed by its name.
func New(dir string, src probe.Sources, opt probe.Options, catalog string, log *slog.Logger) *Source {
	return &Source{dir: dir, src: src, opt: opt, catalog: catalog, log: log}
}

func init() {
	sources.Register("telemetry", func(node *yaml.Node, env sources.Env) (sources.Source, error) {
		var c struct {
			Declarations string       `yaml:"declarations"`
			Prometheus   string       `yaml:"prometheus"`
			Loki         string       `yaml:"loki"`
			Tempo        string       `yaml:"tempo"`
			Window       string       `yaml:"window"`
			Sample       int          `yaml:"sample"`
			Lookback     string       `yaml:"lookback"`
			Labels       probe.Labels `yaml:"labels"`
			// Catalog is the blueprint a declared service is an entity of.
			Catalog string `yaml:"catalog"`
		}
		if err := sources.Decode(node, &c); err != nil {
			return nil, err
		}
		if c.Declarations == "" || c.Prometheus == "" || c.Loki == "" || c.Tempo == "" {
			return nil, errors.New("the telemetry source needs declarations, prometheus, loki, and tempo")
		}
		if !filepath.IsAbs(c.Declarations) {
			c.Declarations = filepath.Join(env.Dir, c.Declarations)
		}
		opt := probe.Options{Sample: c.Sample, Labels: c.Labels}
		if c.Window != "" {
			d, err := time.ParseDuration(c.Window)
			if err != nil {
				return nil, fmt.Errorf("window %q is not a duration", c.Window)
			}
			opt.Window = d
		}
		if c.Lookback != "" {
			d, err := time.ParseDuration(c.Lookback)
			if err != nil {
				return nil, fmt.Errorf("lookback %q is not a duration", c.Lookback)
			}
			opt.Lookback = d
		}
		return New(c.Declarations, Stores(c.Prometheus, c.Loki, c.Tempo), opt, c.Catalog, env.Log), nil
	})
}

// Stores are the three stores at their URLs.
func Stores(prometheus, loki, tempo string) probe.Sources {
	return probe.Sources{Prometheus: stores.NewPrometheus(prometheus), Loki: stores.NewLoki(loki), Tempo: stores.NewTempo(tempo)}
}

// Facts are what the source writes.
func (s *Source) Facts() []string { return []string{"telemetry"} }

// Linked is the catalog blueprint a declared service is an entity of, so
// the sink can relate a finding or a verdict to it; none when unset.
func (s *Source) Linked() []string {
	if s.catalog == "" {
		return nil
	}
	return []string{s.catalog}
}

// Collect adds every declared service with its measurements. A
// declaration that does not parse is skipped and named; an unreadable
// directory fails the run, and so does a pass where no store answered for
// any service.
func (s *Source) Collect(ctx context.Context, e *estate.Writer) error {
	decls, skipped, err := Load(s.dir)
	if err != nil {
		return err
	}
	for name, why := range skipped {
		s.log.WarnContext(ctx, "declaration skipped", "file", name, "error", why)
	}
	unread := 0
	for _, d := range decls {
		m := probe.Run(ctx, d, s.src, s.opt)
		if m.Unread() != "" {
			unread++
		}
		if err := Shape(e, d, m, s.catalog); err != nil {
			return err
		}
	}
	s.log.InfoContext(ctx, "telemetry measured", "services", len(decls), "skipped", len(skipped), "unread", unread)
	if unread > 0 && unread == len(decls) {
		return errors.New("no store could be read for any service")
	}
	return nil
}

// Shape writes one service's measurements as its subject and telemetry
// fact: the declaration, the window, and one entry per measurement. When
// no store answered for any measurement, it records a miss instead, so an
// earlier reading is carried.
func Shape(e *estate.Writer, d *declaration.Declaration, m probe.Measures, catalog string) error {
	s := e.Subject("service", d.Service, d.Title)
	if catalog != "" {
		s.SetRef(estate.Ref{Blueprint: catalog, Key: d.Service})
	}
	if why := m.Unread(); why != "" {
		s.Miss("telemetry", why)
		return nil
	}
	fact, err := estate.Plain(m)
	if err != nil {
		return err
	}
	out, _ := fact.(map[string]any)
	out["declaration"] = d
	out["window"] = m.Window.String()
	return s.Set("telemetry", out)
}

// Load reads every .yaml and .yml declaration in dir, or dir itself when it
// is a file. Files that do not parse are returned by name with the reason.
func Load(dir string) ([]*declaration.Declaration, map[string]string, error) {
	skipped := map[string]string{}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() {
		d, err := declaration.Load(dir)
		if err != nil {
			return nil, nil, err
		}
		return []*declaration.Declaration{d}, skipped, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var out []*declaration.Declaration
	for _, en := range entries {
		name := en.Name()
		if en.IsDir() || strings.HasPrefix(name, ".") || !(strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")) {
			continue
		}
		d, err := declaration.Load(filepath.Join(dir, name))
		if err != nil {
			skipped[name] = err.Error()
			continue
		}
		out = append(out, d)
	}
	return out, skipped, nil
}
