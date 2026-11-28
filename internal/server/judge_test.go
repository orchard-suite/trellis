package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/orchard-suite/platformkit/v3/change"

	"github.com/orchard-suite/trellis"
	"github.com/orchard-suite/trellis/internal/gate"
	"github.com/orchard-suite/trellis/internal/meter"
	"github.com/orchard-suite/trellis/internal/pack"
)

const goodStatement = `{"statement": {
  "key": "run-1001",
  "writer": {"name": "infra", "version": "3.1.0"},
  "actor": {"kind": "person", "id": "ada@example.com"},
  "things": [{"blueprint": "app", "key": "beacon"}],
  "artifact": {"kind": "plan", "digest": "sha256:` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `"},
  "applier": "asker",
  "effects": [{"kind": "opens", "subject": {"blueprint": "app", "key": "beacon"}, "path": "in-place", "undo": "yes", "claim": {"from": "writer"}}]
}}`

func (h *harness) judge(body string) *httptest.ResponseRecorder {
	h.t.Helper()
	rec := httptest.NewRecorder()
	h.control.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/judge", strings.NewReader(body)))
	return rec
}

func withJudge(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	set, err := pack.LoadGate(trellis.Packs(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	j, err := gate.New(set, gate.Settings{Approvers: []change.Grant{{Field: "team", Values: []string{"platform"}}}, Version: "6.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	metrics, _ := meter.New()
	h.srv.UseJudge(j, metrics)
	return h
}

// The control port answers a route in the wire shape the runner reads.
func TestJudgeAnswersARoute(t *testing.T) {
	h := withJudge(t)
	rec := h.judge(goodStatement)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"judge", "route", "reasons", "drafts"} {
		if _, ok := raw[k]; !ok {
			t.Fatalf("the answer has no %s: %s", k, rec.Body)
		}
	}
	if string(raw["drafts"]) != "[]" {
		t.Fatalf("drafts is a list, empty with no draft: %s", raw["drafts"])
	}
	var ans gate.Answer
	if err := json.Unmarshal(rec.Body.Bytes(), &ans); err != nil {
		t.Fatal(err)
	}
	if ans.Judge != (change.Judge{Name: "trellis", Version: "6.2.0", Pack: "gate@1.0.0"}) {
		t.Fatalf("judge %+v", ans.Judge)
	}
	if ans.Route.To != change.RoutePeople || ans.Route.Quorum != 1 || ans.Route.Grants[0].Values[0] != "platform" || len(ans.Reasons) != 1 || ans.Reasons[0].Fence != change.FenceAsker {
		t.Fatalf("an asker's change goes to the default approvers: %s", rec.Body)
	}
}

// A statement the kit's validator refuses answers 400 with its reason; a
// body that is no judge request answers 400 too, and an off judge 503.
func TestJudgeRefusals(t *testing.T) {
	h := withJudge(t)
	for _, c := range []struct {
		body, code string
	}{
		{strings.Replace(goodStatement, `"opens"`, `"teleports"`, 1), "unknown_kind"},
		{strings.Replace(goodStatement, `"asker"`, `"laptop"`, 1), "bad_applier"},
		{`{"statement": {"key": "x"}, "extra": 1}`, "bad_json"},
		{`{}`, "missing_statement"},
		{`not json`, "bad_json"},
	} {
		rec := h.judge(c.body)
		var e JudgeError
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		if rec.Code != http.StatusBadRequest || e.Error != c.code || e.Reason == "" {
			t.Errorf("want 400 %s, got %d %s", c.code, rec.Code, rec.Body)
		}
	}
	off := newHarness(t)
	rec := off.judge(goodStatement)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "judge_off") {
		t.Fatalf("an off judge: %d %s", rec.Code, rec.Body)
	}
}

// The judge is on the control port alone: the health port and the session
// port have no such route.
func TestJudgeIsOnTheControlPortAlone(t *testing.T) {
	h := withJudge(t)
	for name, handler := range map[string]http.Handler{"health": h.health, "session": h.session} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/judge", strings.NewReader(goodStatement))
		req.Header.Set("X-Forwarded-Email", "ada@example.com")
		req.Header.Set("X-Forwarded-Groups", "Viewer")
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			t.Errorf("the %s port judged a change", name)
		}
	}
}
