// Package orrery is trellis's adapter to an Orrery hub. It is three things
// behind one client: a source of subjects and waivers, the sink the
// findings and verdicts are written to, and trellis's own description. It
// names no blueprint and no field: every list it reads, every fact it
// makes, and every blueprint it writes comes from the estate's settings.
// The judge and the packs never see a blueprint.
package orrery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/sources"
)

// Source reads subjects and waivers from the hub through the mappings.
type Source struct {
	client   *Client
	mappings []*compiled
	posture  bool
	log      *slog.Logger
	// have is the hub's lists as the last read found them; nil before the
	// first.
	mu   sync.Mutex
	have map[string]bool
}

// NewSource reads the hub with a token that may read every list the
// mappings name. With posture, it reads the hub's access posture too, which
// the token needs posture.read for.
func NewSource(client *Client, mappings []Mapping, posture bool, log *slog.Logger) (*Source, error) {
	s := &Source{client: client, posture: posture, log: log}
	for _, m := range mappings {
		c, err := Compile(m)
		if err != nil {
			return nil, err
		}
		s.mappings = append(s.mappings, c)
	}
	return s, nil
}

func init() {
	sources.Register("orrery", func(node *yaml.Node, env sources.Env) (sources.Source, error) {
		var c struct {
			URL      string    `yaml:"url"`
			Token    string    `yaml:"token"`
			Mappings []Mapping `yaml:"mappings"`
			// Include adds the mappings of files: standard, the built-in
			// ones for a catalog on the blueprint standard, or a path
			// relative to the settings file.
			Include []string `yaml:"include"`
			Posture bool     `yaml:"posture"`
		}
		if err := sources.Decode(node, &c); err != nil {
			return nil, err
		}
		for _, inc := range c.Include {
			raw := Standard
			if inc != "standard" {
				path := inc
				if !filepath.IsAbs(path) {
					path = filepath.Join(env.Dir, path)
				}
				var err error
				if raw, err = os.ReadFile(path); err != nil {
					return nil, fmt.Errorf("include: %w", err)
				}
			}
			maps, err := ParseMappings(raw)
			if err != nil {
				return nil, fmt.Errorf("include %s: %w", inc, err)
			}
			c.Mappings = append(c.Mappings, maps...)
		}
		if len(c.Mappings) == 0 && !c.Posture {
			return nil, errors.New("the orrery source needs mappings (or include: [standard]) or posture")
		}
		return NewSource(NewClient(c.URL, c.Token, env.Log), c.Mappings, c.Posture, env.Log)
	})
}

// Facts are every fact the mappings and the posture write.
func (s *Source) Facts() []string {
	var out []string
	for _, m := range s.mappings {
		if m.Kind == WaiverKind {
			continue
		}
		out = append(out, m.fact())
	}
	if s.posture {
		out = append(out, "posture", "access", "audience")
	}
	return out
}

// Linked is every list a finding on a mapping's subjects links to, the
// entity read or its ref's, that the hub had at the last read: none before
// the first, and never a list a mapping names that the hub lacks. The sink
// relates a finding to each.
func (s *Source) Linked() []string {
	s.mu.Lock()
	have := s.have
	s.mu.Unlock()
	var out []string
	for _, m := range s.mappings {
		bp := m.Blueprint
		switch {
		case m.Kind == WaiverKind:
			continue
		case m.Ref != nil:
			bp = m.Ref.Blueprint
		case m.Link != nil && !*m.Link:
			continue
		}
		if have[bp] {
			out = append(out, bp)
		}
	}
	return out
}

// Records is what Collect reads, by blueprint.
type Records map[string][]Record

// Unread is why each list that was not read was not, by blueprint.
type Unread map[string]string

func (u Unread) why(blueprint string) string {
	if r := u[blueprint]; r != "" {
		return r
	}
	return "the list " + blueprint + " was not read"
}

// needed is every list the mappings read, and whether each may fail alone.
func needed(maps []*compiled) map[string]bool {
	optional := map[string]bool{}
	for _, m := range maps {
		opt := m.Optional || m.Kind == WaiverKind
		if have, ok := optional[m.Blueprint]; !ok || have {
			optional[m.Blueprint] = opt
		}
		for _, j := range m.Join {
			if _, ok := optional[j.Blueprint]; !ok {
				optional[j.Blueprint] = true
			}
		}
	}
	return optional
}

// Collect asks the hub which lists it has, reads each list the mappings
// name once, then applies them. A list the hub does not have is a gap:
// what it would give is missed, with why, and every other list is read and
// judged as usual. A required list the hub has and cannot be read fails
// the read, so a partial read never judges less than is there; an optional
// one is a gap.
func (s *Source) Collect(ctx context.Context, e *estate.Writer) error {
	have, err := s.client.Blueprints(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.have = have
	s.mu.Unlock()
	recs := Records{}
	unread := Unread{}
	lists := needed(s.mappings)
	reads := asked(s.mappings)
	names := make([]string, 0, len(lists))
	for bp := range lists {
		names = append(names, bp)
	}
	sort.Strings(names)
	for _, bp := range names {
		if !have[bp] {
			unread[bp] = "the hub has no list " + bp + ", or the token may not read it"
			s.log.WarnContext(ctx, "orrery list not in the hub; what it gives is missed", "blueprint", bp)
			continue
		}
		got, err := s.client.Active(ctx, bp, reads[bp])
		if err != nil {
			if !lists[bp] {
				return err
			}
			unread[bp] = "the list " + bp + " could not be read: " + err.Error()
			s.log.WarnContext(ctx, "orrery list unread; what it gives is missed", "blueprint", bp, "error", err)
			continue
		}
		recs[bp] = got
	}
	if err := Apply(ctx, e, s.mappings, recs, unread, s.log); err != nil {
		return err
	}
	if s.posture {
		// The posture is judged on its own: when it cannot be read, its subjects are not there, and everything else still is.
		p, err := s.client.Posture(ctx)
		if err != nil {
			s.log.WarnContext(ctx, "orrery posture unread; the access pack has nothing to judge", "error", err)
		} else {
			pc := ShapePosture(e, HubName(s.client.base), p)
			s.log.InfoContext(ctx, "orrery posture read", "blueprints", pc.Blueprints, "audiences", pc.Audiences)
		}
	} else {
		s.log.InfoContext(ctx, "orrery posture not read: the source does not ask for it")
	}
	return nil
}

// Apply runs every mapping over the lists read, in order, and sets the
// waivers when a waiver list was read. A mapping whose list, or a list it
// joins, was not read writes nothing and leaves a gap: its fact is missed
// on every subject of its kind, with why from unread. It is pure over recs,
// so tests feed it fixtures.
func Apply(ctx context.Context, e *estate.Writer, maps []*compiled, recs Records, unread Unread, log *slog.Logger) error {
	var waivers []estate.Waiver
	waiversRead := false
	gap := func(m *compiled, blueprint string) {
		why := unread.why(blueprint)
		if m.Kind != WaiverKind {
			e.Gap(m.Kind, m.fact(), why)
		}
		log.WarnContext(ctx, "orrery mapping not applied; what it gives is missed", "blueprint", m.Blueprint, "kind", m.Kind, "why", why)
	}
	for _, m := range maps {
		if _, ok := recs[m.Blueprint]; !ok {
			gap(m, m.Blueprint)
			continue
		}
		res, ws, err := m.Apply(e, recs)
		var join errJoinUnread
		if errors.As(err, &join) {
			gap(m, join.blueprint)
			continue
		}
		if err != nil {
			return fmt.Errorf("mapping %s/%s: %w", m.Blueprint, m.Kind, err)
		}
		if m.Kind == WaiverKind {
			waivers = append(waivers, ws...)
			waiversRead = true
		}
		if len(res.Skipped) > 0 {
			log.WarnContext(ctx, "orrery entities skipped", "blueprint", m.Blueprint, "kind", m.Kind, "skipped", len(res.Skipped), "first", res.Skipped[0])
		}
		log.InfoContext(ctx, "orrery mapped", "blueprint", m.Blueprint, "kind", m.Kind, "subjects", res.Subjects, "waivers", len(ws))
	}
	if waiversRead {
		e.AddWaivers(waivers)
	}
	return nil
}

// SplitRepoURL takes a forge URL apart into the repository's URL and the
// folder in it: https://github.com/o/r/tree/main/apps/x is
// https://github.com/o/r and apps/x.
func SplitRepoURL(u string) (base, folder string) {
	u = strings.TrimSuffix(strings.TrimSpace(u), "/")
	if i := strings.Index(u, "/tree/"); i >= 0 {
		rest := u[i+len("/tree/"):]
		_, folder, _ = strings.Cut(rest, "/")
		return u[:i], folder
	}
	return strings.TrimSuffix(u, ".git"), ""
}
