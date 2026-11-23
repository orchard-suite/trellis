// Package graft reads how a request desk is set up: the program file its
// admin owns, and what the catalog hub holds for the lanes and runners
// that file names. A lane is a hub action, and a runner is a hub source
// with a rule for the tools it serves. The source writes what each says;
// the graft pack judges it.
package graft

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/obs"
	"github.com/orchard-suite/trellis/internal/sources"
	"github.com/orchard-suite/trellis/internal/sources/orrery"
)

// maxFile is the largest program file read.
const maxFile = 1 << 20

// Hub is what the source asks the catalog hub.
type Hub interface {
	Posture(ctx context.Context) (orrery.Posture, error)
	Action(ctx context.Context, id string) (orrery.Action, error)
	Sources(ctx context.Context) ([]orrery.HubSource, error)
}

// Source reads one program file and the hub it names its lanes and
// runners in.
type Source struct {
	file, token string
	hub         Hub
	hubName     string
	http        *http.Client
	log         *slog.Logger
}

// New makes the source. file is a path or an http address; token, when
// set, is sent as a bearer token to that address.
func New(file, token string, hub Hub, hubName string, log *slog.Logger) *Source {
	return &Source{file: file, token: token, hub: hub, hubName: hubName, http: obs.Client(30 * time.Second), log: log}
}

func init() {
	sources.Register("graft", func(node *yaml.Node, env sources.Env) (sources.Source, error) {
		var c struct {
			// ProgramFile is where the program file is: a path, relative to
			// the settings file, or an http address.
			ProgramFile string `yaml:"program_file"`
			// ProgramFileToken is sent as a bearer token when the file is
			// read from an address.
			ProgramFileToken string `yaml:"program_file_token"`
			// Hub is the catalog hub the lanes and runners are set in, and
			// Token a token that holds posture.read and sees the blueprint
			// the lane actions run on.
			Hub   string `yaml:"hub"`
			Token string `yaml:"token"`
		}
		if err := sources.Decode(node, &c); err != nil {
			return nil, err
		}
		if c.ProgramFile == "" {
			return nil, errors.New("the graft source needs program_file")
		}
		if c.Hub == "" {
			return nil, errors.New("the graft source needs hub, the catalog hub's address")
		}
		if !isURL(c.ProgramFile) && !filepath.IsAbs(c.ProgramFile) {
			c.ProgramFile = filepath.Join(env.Dir, c.ProgramFile)
		}
		return New(c.ProgramFile, c.ProgramFileToken, orrery.NewClient(c.Hub, c.Token, env.Log), orrery.HubName(c.Hub), env.Log), nil
	})
}

// Facts are what the source writes.
func (s *Source) Facts() []string {
	return []string{"graft_program", "graft_repository", "graft_lane", "lane_action", "graft_runner", "runner_source"}
}

func isURL(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

// Collect reads the program file, then asks the hub about each lane and
// runner it names. A program file that cannot be read fails the run. A
// hub that cannot be read is a miss on each lane's or runner's hub fact,
// so what the file says is still judged.
func (s *Source) Collect(ctx context.Context, w *estate.Writer) error {
	raw, err := s.read(ctx)
	if err != nil {
		return fmt.Errorf("program file: %w", err)
	}
	p, err := Parse(raw)
	if err != nil {
		return fmt.Errorf("program file %s: %w", s.file, err)
	}
	lanes, runners := Shape(w, p)
	s.lanes(ctx, lanes)
	s.runners(ctx, runners)
	s.log.InfoContext(ctx, "graft read", "program", p.Name, "lanes", len(lanes), "runners", len(runners), "repositories", len(p.Repositories))
	return nil
}

func (s *Source) read(ctx context.Context) ([]byte, error) {
	if !isURL(s.file) {
		return os.ReadFile(s.file)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.file, nil)
	if err != nil {
		return nil, err
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	res, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxFile))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %d", s.file, res.StatusCode)
	}
	return body, nil
}

// lanes writes each lane's hub action. The posture lists every action
// whoever may run it, so an id it lacks is a missing action; the action
// itself holds the access rules and the tiers.
func (s *Source) lanes(ctx context.Context, lanes []Lane) {
	if len(lanes) == 0 {
		return
	}
	posture, err := s.hub.Posture(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "graft: hub posture unread; the lane actions stay unknown", "error", err)
		for _, l := range lanes {
			l.Handle.Miss("lane_action", "the hub's posture was not read: "+err.Error())
		}
		return
	}
	known := posture.ActionIDs()
	for _, l := range lanes {
		if !known[l.Action] {
			_ = l.Handle.Set("lane_action", map[string]any{"hub": s.hubName, "id": l.Action, "exists": false})
			continue
		}
		a, err := s.hub.Action(ctx, l.Action)
		if err != nil {
			s.log.WarnContext(ctx, "graft: lane action unread", "action", l.Action, "error", err)
			l.Handle.Miss("lane_action", "the hub lists the action but it was not read; the token must see the blueprint it runs on: "+err.Error())
			continue
		}
		_ = l.Handle.Set("lane_action", ShapeAction(s.hubName, l.Action, a))
	}
}

// runners writes the hub source each runner names.
func (s *Source) runners(ctx context.Context, runners []Runner) {
	if len(runners) == 0 {
		return
	}
	list, err := s.hub.Sources(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "graft: hub sources unread; the runners stay unknown", "error", err)
		for _, r := range runners {
			r.Handle.Miss("runner_source", "the hub's sources were not read: "+err.Error())
		}
		return
	}
	byID := map[string]orrery.HubSource{}
	for _, src := range list {
		byID[src.ID] = src
	}
	for _, r := range runners {
		src, ok := byID[r.ID]
		_ = r.Handle.Set("runner_source", ShapeRunner(s.hubName, r.ID, src, ok))
	}
}

// ShapeAction is a pure function from a hub action to a lane's lane_action
// fact: whether it is open to everyone, how many grants and audiences let
// people in on everything, whether its home's owners are let in, whether
// a run they ask for waits, each rule, and the tiers that come with it.
func ShapeAction(hub, id string, a orrery.Action) map[string]any {
	out := map[string]any{
		"hub": hub, "id": id, "exists": true,
		"everyone": false, "grants": 0, "audiences": 0, "home": false,
		"approval": approval(a.Approval),
		"rules":    []any{}, "tiers": []string{},
	}
	if a.Access == nil {
		return out
	}
	out["everyone"] = a.Access.Everyone
	out["grants"] = len(a.Access.Grants)
	out["audiences"] = len(a.Access.Audiences)
	out["home"] = set(a.Access.Home)
	rules := make([]any, 0, len(a.Access.Rules))
	for _, r := range a.Access.Rules {
		rules = append(rules, map[string]any{
			// Who names people: an audience or a grant. Without it, and without owns, the rule takes anyone signed in.
			"who":      len(r.Audiences) > 0 || len(r.Grants) > 0,
			"owns":     set(r.Owns),
			"where":    set(r.Where),
			"approval": approval(r.Approval),
		})
	}
	out["rules"] = rules
	if a.Access.Tiers != nil {
		out["tiers"] = a.Access.Tiers
	}
	return out
}

// approval reads false, true, or "self" as none, other, or self: whether a
// run waits, and for whom.
func approval(raw []byte) string {
	switch strings.TrimSpace(string(raw)) {
	case "true":
		return "other"
	case `"self"`:
		return "self"
	}
	return "none"
}

// set says whether a JSON value is there and is not null, false, or empty.
func set(raw []byte) bool {
	switch string(bytes.TrimSpace(raw)) {
	case "", "null", "false", "[]", "{}":
		return false
	}
	return true
}

// ShapeRunner is a pure function from a hub source to a runner's
// runner_source fact: whether the hub has it, its class, and what its
// serves rule says.
func ShapeRunner(hub, id string, src orrery.HubSource, found bool) map[string]any {
	out := map[string]any{"hub": hub, "id": id, "exists": found}
	if !found {
		return out
	}
	out["class"] = src.Class
	out["serves"] = src.Serves != nil
	tiers, from := []string{}, []string{}
	on, blueprint := false, ""
	if sv := src.Serves; sv != nil {
		if sv.Tiers != nil {
			tiers = sv.Tiers
		}
		if sv.Sources != nil {
			from = sv.Sources
		}
		if sv.On != nil {
			on, blueprint = true, sv.On.Blueprint
		}
	}
	out["tiers"] = tiers
	out["sources"] = from
	out["on"] = on
	out["on_blueprint"] = blueprint
	return out
}
