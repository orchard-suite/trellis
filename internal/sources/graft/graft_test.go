package graft

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/sources/orrery"
)

const programFile = `
program: acme
trusted: true
forge: { kind: github }
committer: { email: desk@example.com }
repositories:
- path: acme/api
  entity: repository:acme-api
  protected: [infrastructure/, graft.yaml, .github/workflows/]
- path: acme/web
  entity: repository:acme-web
  protected: [graft.yaml]
lanes:
- id: user
  outside: true
- id: developer
  small_fix: true
- id: lead-architect
  protected: true
  review: true
limits:
  small_fix_lines: 150
  spend_usd: 7.5
runners:
  reader: { production: acme-reader-prod, nonproduction: acme-reader-nonprod }
  operator: { production: acme-operator-prod }
hours:
  schedule: platform
`

// fakeHub answers from what a test gives it.
type fakeHub struct {
	posture    string
	postureErr error
	actions    map[string]string
	sources    string
	sourcesErr error
}

func (f fakeHub) Posture(context.Context) (orrery.Posture, error) {
	var p orrery.Posture
	if f.postureErr != nil {
		return p, f.postureErr
	}
	return p, json.Unmarshal([]byte(f.posture), &p)
}

func (f fakeHub) Action(_ context.Context, id string) (orrery.Action, error) {
	var a orrery.Action
	raw, ok := f.actions[id]
	if !ok {
		return a, errors.New("orrery answered 404: Unknown action")
	}
	return a, json.Unmarshal([]byte(raw), &a)
}

func (f fakeHub) Sources(context.Context) ([]orrery.HubSource, error) {
	var out struct {
		Sources []orrery.HubSource `json:"sources"`
	}
	if f.sourcesErr != nil {
		return nil, f.sourcesErr
	}
	return out.Sources, json.Unmarshal([]byte(f.sources), &out)
}

var hub = fakeHub{
	// The posture lists two of the three lane actions; the hub lets the token read only one.
	posture: `{"items":[
		{"target":{"kind":"action","id":"graft_acme_user"}},
		{"target":{"kind":"action","id":"graft_acme_lead_architect"}},
		{"target":{"kind":"blueprint","id":"repository"}}]}`,
	actions: map[string]string{
		"graft_acme_lead_architect": `{"id":"graft_acme_lead_architect","approval":false,"access":{
			"grants":[{"values":["user@example.com"]}],"home":["lead"],
			"rules":[{"owns":["architect"],"approval":"self"},{"audiences":["platform"],"approval":true},{"where":{"field":"env","op":"eq","value":"test"}}],
			"tiers":["read","act"]}}`,
	},
	sources: `{"sources":[
		{"id":"acme-reader-prod","class":"runner","serves":{"tiers":["read"],"sources":["desk-tools"],"on":{"blueprint":"repository"}}},
		{"id":"acme-reader-nonprod","class":"runner"}]}`,
}

func collect(t *testing.T, h Hub) *estate.Estate {
	t.Helper()
	path := filepath.Join(t.TempDir(), "program.yaml")
	if err := os.WriteFile(path, []byte(programFile), 0o600); err != nil {
		t.Fatal(err)
	}
	e := estate.New(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC))
	s := New(path, "", h, "hub.example.test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := s.Collect(context.Background(), e.Writer("graft")); err != nil {
		t.Fatal(err)
	}
	return e
}

func fact(t *testing.T, e *estate.Estate, kind, key, name string) map[string]any {
	t.Helper()
	s, ok := e.Find(kind, key)
	if !ok {
		t.Fatalf("no %s %s", kind, key)
	}
	f, ok := s.Facts[name]
	if !ok {
		t.Fatalf("%s %s has no %s; missed: %+v", kind, key, name, s.Missed)
	}
	return f.Value.(map[string]any)
}

func TestCollectWritesWhatTheProgramFileSays(t *testing.T) {
	e := collect(t, hub)
	p := fact(t, e, "graft_program", "acme", "graft_program")
	if p["trusted"] != true || p["small_fix_lines"] != float64(150) || p["spend_usd"] != 7.5 || p["hours_schedule"] != "platform" {
		t.Errorf("program: %+v", p)
	}
	if !reflect.DeepEqual(p["review_lanes"], []any{"lead-architect"}) || !reflect.DeepEqual(p["unreviewed_lanes"], []any{"user", "developer"}) || !reflect.DeepEqual(p["protected_lanes"], []any{"lead-architect"}) {
		t.Errorf("program lanes: %+v", p)
	}
	// The repository file is always protected, so it is not counted.
	if api := fact(t, e, "graft_repository", "acme/acme/api", "graft_repository"); !reflect.DeepEqual(api["protected"], []any{"infrastructure/", ".github/workflows/"}) {
		t.Errorf("api protected: %+v", api["protected"])
	}
	if web := fact(t, e, "graft_repository", "acme/acme/web", "graft_repository"); !reflect.DeepEqual(web["protected"], []any{}) {
		t.Errorf("web protected: %+v", web["protected"])
	}
	user := fact(t, e, "graft_lane", "acme/user", "graft_lane")
	if user["lowest"] != true || user["outside"] != true || user["action"] != "graft_acme_user" {
		t.Errorf("user lane: %+v", user)
	}
	arch := fact(t, e, "graft_lane", "acme/lead-architect", "graft_lane")
	if arch["lowest"] != false || arch["protected"] != true || arch["review"] != true || arch["action"] != "graft_acme_lead_architect" {
		t.Errorf("architect lane: %+v", arch)
	}
	r := fact(t, e, "graft_runner", "acme/reader/production", "graft_runner")
	if r["id"] != "acme-reader-prod" || r["twin"] != "acme-reader-nonprod" {
		t.Errorf("reader: %+v", r)
	}
	if op := fact(t, e, "graft_runner", "acme/operator/production", "graft_runner"); op["twin"] != "" {
		t.Errorf("operator: %+v", op)
	}
	if _, ok := e.Find("graft_runner", "acme/operator/nonproduction"); ok {
		t.Error("a runner the file does not name has a subject")
	}
}

func TestCollectWritesWhatTheHubGrants(t *testing.T) {
	e := collect(t, hub)
	// Not in the posture: the action is missing.
	if dev := fact(t, e, "graft_lane", "acme/developer", "lane_action"); dev["exists"] != false || dev["id"] != "graft_acme_developer" {
		t.Errorf("developer action: %+v", dev)
	}
	// In the posture, but not readable: unknown, never missing.
	user, _ := e.Find("graft_lane", "acme/user")
	if _, ok := user.Facts["lane_action"]; ok || user.Missed["lane_action"].Reason == "" {
		t.Errorf("user action: facts %+v, missed %+v", user.Facts["lane_action"], user.Missed)
	}
	a := fact(t, e, "graft_lane", "acme/lead-architect", "lane_action")
	want := map[string]any{
		"hub": "hub.example.test", "id": "graft_acme_lead_architect", "exists": true,
		"everyone": false, "grants": float64(1), "audiences": float64(0), "home": true, "approval": "none",
		"rules": []any{
			map[string]any{"who": false, "owns": true, "where": false, "approval": "self"},
			map[string]any{"who": true, "owns": false, "where": false, "approval": "other"},
			map[string]any{"who": false, "owns": false, "where": true, "approval": "none"},
		},
		"tiers": []any{"read", "act"},
	}
	if !reflect.DeepEqual(a, want) {
		t.Errorf("architect action:\n got %+v\nwant %+v", a, want)
	}
	prod := fact(t, e, "graft_runner", "acme/reader/production", "runner_source")
	if prod["exists"] != true || prod["serves"] != true || prod["on"] != true || prod["on_blueprint"] != "repository" || !reflect.DeepEqual(prod["tiers"], []any{"read"}) || !reflect.DeepEqual(prod["sources"], []any{"desk-tools"}) {
		t.Errorf("reader prod: %+v", prod)
	}
	if non := fact(t, e, "graft_runner", "acme/reader/nonproduction", "runner_source"); non["exists"] != true || non["serves"] != false || non["on"] != false {
		t.Errorf("reader nonprod: %+v", non)
	}
	if op := fact(t, e, "graft_runner", "acme/operator/production", "runner_source"); op["exists"] != false {
		t.Errorf("operator: %+v", op)
	}
}

func TestAHubThatCannotBeReadIsAMissNotAMissingAction(t *testing.T) {
	e := collect(t, fakeHub{postureErr: errors.New("orrery answered 403: missing posture.read"), sourcesErr: errors.New("orrery answered 503")})
	lane, _ := e.Find("graft_lane", "acme/developer")
	if _, ok := lane.Facts["lane_action"]; ok || lane.Missed["lane_action"].Reason == "" {
		t.Errorf("lane: facts %+v, missed %+v", lane.Facts, lane.Missed)
	}
	if _, ok := lane.Facts["graft_lane"]; !ok {
		t.Error("the program file's fact is gone with the hub")
	}
	runner, _ := e.Find("graft_runner", "acme/reader/production")
	if _, ok := runner.Facts["runner_source"]; ok || runner.Missed["runner_source"].Reason == "" {
		t.Errorf("runner: facts %+v, missed %+v", runner.Facts, runner.Missed)
	}
}

func TestProgramFileReadFromAnAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer file-token" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, programFile)
	}))
	defer srv.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := estate.New(time.Now())
	if err := New(srv.URL, "file-token", hub, "hub.example.test", log).Collect(context.Background(), e.Writer("graft")); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Find("graft_program", "acme"); !ok {
		t.Error("no program")
	}
	if err := New(srv.URL, "", hub, "hub.example.test", log).Collect(context.Background(), estate.New(time.Now()).Writer("graft")); err == nil {
		t.Error("a refused read did not fail the run")
	}
}

func TestLaneActionID(t *testing.T) {
	for _, c := range []struct{ program, lane, want string }{
		{"acme", "user", "graft_acme_user"},
		{"acme-shop", "Lead-Architect", "graft_acme_shop_lead_architect"},
	} {
		if got := LaneAction(c.program, c.lane); got != c.want {
			t.Errorf("%s %s: %s, want %s", c.program, c.lane, got, c.want)
		}
	}
}

func TestParseRefusesAFileWithNoProgram(t *testing.T) {
	if _, err := Parse([]byte("lanes: []\n")); err == nil {
		t.Error("a file that names no program was read")
	}
}
