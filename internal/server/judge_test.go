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
	return h.judgeAs("Bearer "+orreryToken, body)
}

// judgeAs posts to the machine door with an Authorization header, or none
// when it is empty.
func (h *harness) judgeAs(auth, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/judge", strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	h.machine.ServeHTTP(rec, req)
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

// The machine door answers a route in the wire shape the runner reads.
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

// The machine door admits a caller's bearer token alone, compared with
// every caller's: no token, a wrong one, another scheme, or an identity
// header is 401, and the probes need none.
func TestTheMachineDoorTakesTokensAlone(t *testing.T) {
	h := withJudge(t)
	for name, auth := range map[string]string{
		"no token":     "",
		"a wrong one":  "Bearer test-token-1111111111111111111111111111",
		"a prefix":     "Bearer " + orreryToken[:20],
		"basic":        "Basic " + orreryToken,
		"empty bearer": "Bearer ",
	} {
		rec := h.judgeAs(auth, goodStatement)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" || !strings.Contains(rec.Body.String(), "unauthorized") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/judge", strings.NewReader(goodStatement))
	req.Header.Set("X-Forwarded-Email", "ada@example.com")
	req.Header.Set("X-Forwarded-Groups", "Viewer")
	h.machine.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("an identity header proves nothing on the machine door: %d", rec.Code)
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		h.machine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("the probe %s needs no token", path)
		}
	}
}

// The judge is on the machine door alone: the control port and the session
// port have no such route.
func TestJudgeIsOnTheMachineDoorAlone(t *testing.T) {
	h := withJudge(t)
	for name, handler := range map[string]http.Handler{"control": h.control, "session": h.session} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/judge", strings.NewReader(goodStatement))
		req.Header.Set("X-Forwarded-Email", "ada@example.com")
		req.Header.Set("X-Forwarded-Groups", "Viewer")
		req.Header.Set("Authorization", "Bearer "+orreryToken)
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			t.Errorf("the %s port judged a change", name)
		}
	}
}
