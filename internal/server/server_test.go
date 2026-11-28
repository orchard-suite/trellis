package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/orchard-suite/trellis"
	"github.com/orchard-suite/trellis/internal/board"
	"github.com/orchard-suite/trellis/internal/door"
	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/history"
	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/meter"
	"github.com/orchard-suite/trellis/internal/pack"
	"github.com/orchard-suite/trellis/internal/relay"
	"github.com/orchard-suite/trellis/internal/sources"
)

const publicURL = "https://trellis.test"

// orreryToken is the machine door's token for the caller orrery.
const orreryToken = "test-token-0000000000000000000000000000"

// fixtureSource is a small estate with one failing image and one clean
// repository.
type fixtureSource struct{}

func (fixtureSource) Collect(_ context.Context, e *estate.Writer) error {
	_ = e.Subject("cluster", "test", "test").Set("cluster", map[string]any{"name": "test", "policy_engine": false})
	pushed := e.Now().AddDate(0, 0, -3).Format(time.RFC3339)
	clean := map[string]any{"archived": false, "fork": false, "last_push": pushed, "branch_protected": true, "has_ci": true, "alerts_critical": 0, "alerts_high": 0}
	_ = e.Subject("repository", "github/example/app", "app").Set("repo", clean)
	mirror := map[string]any{"archived": false, "fork": false, "last_push": pushed, "branch_protected": true, "has_ci": false, "alerts_critical": 0, "alerts_high": 0}
	_ = e.Subject("repository", "github/example/mirror", "mirror").Set("repo", mirror)
	_ = e.Subject("image", "ghcr.io/example/app:1.0", "ghcr.io/example/app:1.0").Set("scan", map[string]any{"cve_kev": 1, "cve_critical": 2, "cve_fixable": 0, "epss_max": 0.01})
	e.AddWaivers([]estate.Waiver{{Rule: "repo.no-ci", Subject: "github/example/mirror", Reason: "a mirror", Owner: "The owner", ExpiresAt: e.Now().Add(time.Hour)}})
	return nil
}

type failingSource struct{}

func (failingSource) Collect(context.Context, *estate.Writer) error {
	return context.DeadlineExceeded
}

var (
	fixture = sources.Named{Name: "fixture", Type: "fixture", Source: fixtureSource{}, MaxAge: time.Hour}
	broken  = sources.Named{Name: "broken", Type: "broken", Source: failingSource{}, MaxAge: time.Hour}
)

func testPacks(t *testing.T) *pack.Set {
	t.Helper()
	set, err := pack.Load(trellis.Packs(), "", []string{"security", "build"})
	if err != nil {
		t.Fatal(err)
	}
	return set
}

type harness struct {
	t       *testing.T
	srv     *Server
	session http.Handler
	machine http.Handler
	control http.Handler
	// asked has one value for each pass the server asked for.
	asked chan struct{}
}

func newHarness(t *testing.T, srcs ...sources.Named) *harness {
	t.Helper()
	return newHarnessRelaying(t, relay.Config{Service: meter.Service, Version: "test"}, srcs...)
}

// newHarnessRelaying is a harness whose browser telemetry goes where faroCfg
// says.
func newHarnessRelaying(t *testing.T, faroCfg relay.Config, srcs ...sources.Named) *harness {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics, err := meter.New()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := relay.NewRelay(faroCfg, log)
	if err != nil {
		t.Fatal(err)
	}
	set := testPacks(t)
	b := board.New(srcs, set, judge.Options{Params: judge.Merge(set)}, metrics, log)
	h := &harness{t: t, asked: make(chan struct{}, 8)}
	check := func(ctx context.Context) (*board.Snapshot, error) {
		h.asked <- struct{}{}
		return b.Check(ctx)
	}
	srv, err := New(Config{PublicURL: publicURL, Estate: "test", SignIn: door.Config{Roles: []string{"Viewer"}}, Version: "test", Callers: map[string][]byte{"orrery": []byte(orreryToken)}}, b, rel, log, check, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.srv, h.session, h.machine, h.control = srv, srv.Session(), srv.Machine(), srv.Control()
	if len(srcs) > 0 {
		b.Refresh(context.Background())
	}
	return h
}

func (h *harness) do(handler http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func signedIn(req *http.Request) *http.Request {
	req.Header.Set("X-Forwarded-Email", "person@example.com")
	req.Header.Set("X-Forwarded-Groups", "Viewer, Other")
	return req
}

func TestFrontDoor(t *testing.T) {
	h := newHarness(t, fixture)
	if rec := h.do(h.session, httptest.NewRequest("GET", "/v1/board", nil)); rec.Code != http.StatusUnauthorized {
		t.Errorf("no identity: %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/v1/board", nil)
	req.Header.Set("X-Forwarded-Email", "person@example.com")
	req.Header.Set("X-Forwarded-Groups", "Other")
	if rec := h.do(h.session, req); rec.Code != http.StatusForbidden {
		t.Errorf("wrong role: %d", rec.Code)
	}
	if rec := h.do(h.session, signedIn(httptest.NewRequest("GET", "/v1/board", nil))); rec.Code != http.StatusOK {
		t.Errorf("signed in: %d %s", rec.Code, rec.Body.String())
	}
	if rec := h.do(h.session, signedIn(httptest.NewRequest("GET", "/", nil))); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>trellis</title>") || rec.Header().Get("Content-Security-Policy") == "" {
		t.Errorf("page: %d", rec.Code)
	}
	// The page loads its scripts as modules, which a browser runs only when
	// they are served as JavaScript; every module the page imports is here.
	// They sit under the release, so a browser that kept an older release's
	// script never runs it against this page.
	page := h.do(h.session, signedIn(httptest.NewRequest("GET", "/", nil))).Body.String()
	for _, asset := range []string{`"/static/test/app.js"`, `"/static/test/style.css"`, `"/static/test/vendor/faro-web-sdk.iife.js"`} {
		if !strings.Contains(page, asset) {
			t.Errorf("page does not load %s", asset)
		}
	}
	if strings.Contains(page, `"/static/app.js"`) {
		t.Error("page loads an asset outside the release")
	}
	for _, file := range []string{"app.js", "ui.js", "views.js", "lists.js", "inspector.js"} {
		rec := h.do(h.session, signedIn(httptest.NewRequest("GET", "/static/test/"+file, nil)))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "javascript") || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
			t.Errorf("static %s: %d %q %q", file, rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
		}
	}
	if rec := h.do(h.session, signedIn(httptest.NewRequest("GET", "/static/app.js", nil))); rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("unversioned static: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if rec := h.do(h.session, signedIn(httptest.NewRequest("GET", "/static/../web.go", nil))); rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "package web") {
		t.Error("static served a source file")
	}
}

func TestBoardAndFindings(t *testing.T) {
	h := newHarness(t, fixture)
	rec := h.do(h.session, signedIn(httptest.NewRequest("GET", "/v1/board", nil)))
	var p Board
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Level != estate.Red || p.Estate != "test" || p.BySeverity[estate.Critical] != 1 || p.BySeverity[estate.High] != 1 || p.BySeverity[estate.Medium] != 1 || p.ByState[judge.Open] != 3 || p.ByState[judge.Waived] != 1 || p.RefreshedAt == nil || p.Person != "person@example.com" {
		t.Errorf("board: %+v", p)
	}
	if len(p.Rules) != len(testPacks(t).Rules()) || len(p.Packs) != 2 || len(p.Pillars) == 0 || len(p.Waivers) != 1 || p.Waivers[0].Covers != 1 || !p.WaiversRead {
		t.Errorf("rules %d, packs %d, pillars %d, waivers %+v", len(p.Rules), len(p.Packs), len(p.Pillars), p.Waivers)
	}
	for _, r := range p.Rules {
		if r.ID == "repo.no-ci" && (r.Stat.Fail != 0 || r.Stat.Waived != 1 || r.Stat.Pass != 1) {
			t.Errorf("repo.no-ci: %+v", r.Stat)
		}
		if r.ID == "image.cve-kev" && r.Stat.Fail != 1 {
			t.Errorf("image.cve-kev: %+v", r.Stat)
		}
	}

	rec = h.do(h.session, signedIn(httptest.NewRequest("GET", "/v1/findings?kind=image&severity=critical", nil)))
	var f Findings
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Findings) != 1 || f.Findings[0].Rule != "image.cve-kev" || len(f.Subjects) != 1 || len(f.Subjects[0].Findings) != 1 || f.Subjects[0].Facts != nil {
		t.Errorf("filtered findings, the list without facts: %+v", f)
	}
	rec = h.do(h.session, signedIn(httptest.NewRequest("GET", "/v1/findings?kind=repository", nil)))
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Findings) != 1 || f.Findings[0].State != judge.Waived || len(f.Subjects) != 2 || f.Subjects[0].Level != estate.Green {
		t.Errorf("a clean kind still lists its subjects, and the waived finding: %+v", f)
	}
	rec = h.do(h.session, signedIn(httptest.NewRequest("GET", "/v1/findings?state=open&owner=", nil)))
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Findings) != 3 {
		t.Errorf("the open findings nobody owns: %+v", f.Findings)
	}
	rec = h.do(h.session, signedIn(httptest.NewRequest("GET", "/v1/findings?pack=build", nil)))
	if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Findings) != 0 || len(f.Subjects) != 0 {
		t.Errorf("the build pack found nothing here: %+v", f)
	}

	rec = h.do(h.session, signedIn(httptest.NewRequest("GET", "/v1/subject?kind=image&key=ghcr.io%2Fexample%2Fapp%3A1.0", nil)))
	var sv judge.SubjectVerdict
	if err := json.Unmarshal(rec.Body.Bytes(), &sv); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || sv.Facts["scan"].Source != "fixture" || sv.Facts["scan"].ObservedAt.IsZero() || len(sv.Findings) != 2 {
		t.Errorf("one subject with every fact's source and time: %d %+v", rec.Code, sv)
	}
	if rec := h.do(h.session, signedIn(httptest.NewRequest("GET", "/v1/subject?kind=image&key=nope", nil))); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown subject: %d", rec.Code)
	}
}

func TestFailedSourceIsReported(t *testing.T) {
	h := newHarness(t, fixture, broken)
	rec := h.do(h.session, signedIn(httptest.NewRequest("GET", "/v1/board", nil)))
	var p Board
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if len(p.Sources) != 2 || p.Sources[1].Outcome != sources.Failed || p.Sources[1].Error == "" {
		t.Errorf("sources: %+v", p.Sources)
	}
	if p.Level != estate.Red {
		t.Errorf("a failed source does not hide what the others found: %s", p.Level)
	}
}

func TestHealth(t *testing.T) {
	h := newHarness(t)
	if rec := h.do(h.machine, httptest.NewRequest("GET", "/healthz", nil)); rec.Code != http.StatusOK {
		t.Errorf("healthz: %d", rec.Code)
	}
	if rec := h.do(h.machine, httptest.NewRequest("GET", "/readyz", nil)); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz before a snapshot: %d", rec.Code)
	}
	if rec := h.do(h.machine, httptest.NewRequest("GET", "/v1/board", nil)); rec.Code != http.StatusNotFound {
		t.Errorf("the health port serves nothing else: %d", rec.Code)
	}
	ready := newHarness(t, fixture)
	if rec := ready.do(ready.machine, httptest.NewRequest("GET", "/readyz", nil)); rec.Code != http.StatusOK {
		t.Errorf("readyz after a snapshot: %d", rec.Code)
	}
}

func TestRefreshNeedsOwnOrigin(t *testing.T) {
	h := newHarness(t, fixture)
	if rec := h.do(h.session, signedIn(httptest.NewRequest("POST", "/v1/refresh", nil))); rec.Code != http.StatusForbidden {
		t.Errorf("no origin: %d", rec.Code)
	}
	req := signedIn(httptest.NewRequest("POST", "/v1/refresh", nil))
	req.Header.Set("Origin", publicURL)
	if rec := h.do(h.session, req); rec.Code != http.StatusAccepted {
		t.Errorf("own origin: %d", rec.Code)
	}
	select {
	case <-h.asked:
	case <-time.After(5 * time.Second):
		t.Error("the own-origin ask started no pass")
	}
}

func TestControlRunsAPassAndAnswersWhenItIsDone(t *testing.T) {
	h := newHarness(t, fixture)
	before := h.srv.board.Latest().RefreshedAt
	rec := h.do(h.control, httptest.NewRequest("POST", "/v1/refresh", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("control refresh: %d %s", rec.Code, rec.Body.String())
	}
	var pass Pass
	if err := json.Unmarshal(rec.Body.Bytes(), &pass); err != nil {
		t.Fatal(err)
	}
	if !pass.RefreshedAt.After(before) || len(pass.Sources) != 1 || pass.Sent == nil {
		t.Errorf("a new pass with its sources and sends: %+v", pass)
	}
	if rec := h.do(h.machine, httptest.NewRequest("POST", "/v1/refresh", nil)); rec.Code == http.StatusOK {
		t.Error("the health port runs no pass")
	}
}

func TestAListOfRolesIsRefusedAsOne(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	set := testPacks(t)
	b := board.New(nil, set, judge.Options{Params: judge.Merge(set)}, nil, log)
	if _, err := New(Config{PublicURL: publicURL, SignIn: door.Config{Roles: []string{"Viewer,Admin"}}}, b, http.NotFoundHandler(), log, nil, nil); err == nil {
		t.Error("Viewer,Admin was taken as one role")
	}
	if _, err := New(Config{PublicURL: publicURL, SignIn: door.Config{Mode: "magic"}}, b, http.NotFoundHandler(), log, nil, nil); err == nil {
		t.Error("an unknown sign-in mode was taken")
	}
}

func TestTelemetryRelay(t *testing.T) {
	var got map[string]any
	var key string
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("X-API-Key")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer collector.Close()
	h := newHarnessRelaying(t, relay.Config{Service: meter.Service, Version: "test", URL: collector.URL, Key: "k"}, fixture)

	batch := `{"meta":{"app":{"name":"evil"},"user":{"email":"someone-else@example.com"}},"events":[]}`
	if rec := h.do(h.session, httptest.NewRequest("POST", "/api/telemetry", strings.NewReader(batch))); rec.Code != http.StatusUnauthorized {
		t.Errorf("no identity: %d", rec.Code)
	}
	if rec := h.do(h.session, signedIn(httptest.NewRequest("POST", "/api/telemetry", strings.NewReader(batch)))); rec.Code != http.StatusForbidden {
		t.Errorf("no origin: %d", rec.Code)
	}
	req := signedIn(httptest.NewRequest("POST", "/api/telemetry", strings.NewReader(batch)))
	req.Header.Set("Origin", publicURL)
	if rec := h.do(h.session, req); rec.Code != http.StatusAccepted {
		t.Fatalf("relay: %d", rec.Code)
	}
	meta := got["meta"].(map[string]any)
	if key != "k" || meta["app"].(map[string]any)["name"] != "trellis-web" || meta["user"].(map[string]any)["email"] != "person@example.com" {
		t.Errorf("relayed meta %v key %q", meta, key)
	}
}

type fakeHistory struct{ asked string }

func (f *fakeHistory) Subject(_ context.Context, kind, key string, _ time.Time) (history.SubjectHistory, error) {
	f.asked = "subject " + kind + " " + key
	return history.SubjectHistory{Tiers: []history.Span{{Pack: "p", Kind: kind, Key: key, Value: "gold", Rank: 3}}, Findings: []history.Span{}}, nil
}
func (f *fakeHistory) Rule(_ context.Context, rule string, _ time.Time) ([]history.Point, error) {
	f.asked = "rule " + rule
	return []history.Point{{Date: "2026-11-06", Value: 2}}, nil
}
func (f *fakeHistory) Pack(_ context.Context, pack string, _ time.Time) ([]history.Day, error) {
	f.asked = "pack " + pack
	return []history.Day{}, nil
}

func TestHistoryIsBehindTheGateAndAnswersWhatIsAsked(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics, _ := meter.New()
	set := testPacks(t)
	b := board.New(nil, set, judge.Options{Params: judge.Merge(set)}, metrics, log)
	past := &fakeHistory{}
	srv, err := New(Config{PublicURL: publicURL, SignIn: door.Config{Roles: []string{"Viewer"}}}, b, http.NotFoundHandler(), log, nil, past)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, srv: srv, session: srv.Session()}
	if rec := h.do(h.session, httptest.NewRequest("GET", "/v1/history?rule=r", nil)); rec.Code != http.StatusUnauthorized {
		t.Errorf("history without sign-in: %d", rec.Code)
	}
	rec := h.do(h.session, signedIn(httptest.NewRequest("GET", "/v1/history?kind=storage&subject=a%2Fb&days=30", nil)))
	if rec.Code != http.StatusOK || past.asked != "subject storage a/b" || !strings.Contains(rec.Body.String(), `"value":"gold"`) {
		t.Errorf("subject: %d %s asked %q", rec.Code, rec.Body.String(), past.asked)
	}
	if rec := h.do(h.session, signedIn(httptest.NewRequest("GET", "/v1/history?days=5000&rule=r", nil))); rec.Code != http.StatusBadRequest {
		t.Errorf("days past three years: %d", rec.Code)
	}
	if rec := h.do(h.session, signedIn(httptest.NewRequest("GET", "/v1/history", nil))); rec.Code != http.StatusBadRequest {
		t.Errorf("nothing named: %d", rec.Code)
	}
	if rec := h.do(h.session, signedIn(httptest.NewRequest("GET", "/v1/board", nil))); !strings.Contains(rec.Body.String(), `"history":true`) {
		t.Errorf("the board says it keeps history: %s", rec.Body.String())
	}
	none := newHarness(t)
	if rec := none.do(none.session, signedIn(httptest.NewRequest("GET", "/v1/history?rule=r", nil))); rec.Code != http.StatusNotFound {
		t.Errorf("a board with no history: %d", rec.Code)
	}
}

// A device adding the board to a Home Screen, or installing it on a desktop,
// fetches the icons and the manifest the page names without a session.
func TestInstallFilesNeedNoSession(t *testing.T) {
	h := newHarness(t, fixture)
	page := h.do(h.session, signedIn(httptest.NewRequest("GET", "/", nil))).Body.String()
	for _, link := range []string{`<link rel="apple-touch-icon" href="/apple-touch-icon.png">`, `<link rel="manifest" href="/manifest.webmanifest">`, `<meta name="apple-mobile-web-app-title" content="Trellis">`, `<meta name="theme-color"`} {
		if !strings.Contains(page, link) {
			t.Errorf("page head lacks %s", link)
		}
	}
	get := func(path, contentType string) []byte {
		t.Helper()
		rec := h.do(h.session, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != contentType {
			t.Fatalf("%s without a session: %d %q", path, rec.Code, rec.Header().Get("Content-Type"))
		}
		return rec.Body.Bytes()
	}
	size := func(png []byte) string {
		if len(png) < 24 || string(png[1:4]) != "PNG" {
			return "not a PNG"
		}
		return fmt.Sprintf("%dx%d", binary.BigEndian.Uint32(png[16:20]), binary.BigEndian.Uint32(png[20:24]))
	}
	if got := size(get("/apple-touch-icon.png", "image/png")); got != "180x180" {
		t.Errorf("apple-touch-icon is %s", got)
	}
	var manifest struct {
		Name    string `json:"name"`
		Display string `json:"display"`
		Start   string `json:"start_url"`
		Icons   []struct {
			Src     string `json:"src"`
			Sizes   string `json:"sizes"`
			Purpose string `json:"purpose"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(get("/manifest.webmanifest", "application/manifest+json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Name != "Trellis" || manifest.Display != "standalone" || manifest.Start != "/" {
		t.Errorf("manifest: %+v", manifest)
	}
	purposes := map[string]bool{}
	for _, icon := range manifest.Icons {
		purposes[icon.Sizes+" "+icon.Purpose] = true
		if got := size(get(icon.Src, "image/png")); got != icon.Sizes {
			t.Errorf("%s is %s, the manifest says %s", icon.Src, got, icon.Sizes)
		}
	}
	for _, want := range []string{"192x192 any", "512x512 any", "512x512 maskable"} {
		if !purposes[want] {
			t.Errorf("manifest has no %s icon", want)
		}
	}
	if rec := h.do(h.session, httptest.NewRequest("GET", "/", nil)); rec.Code != http.StatusUnauthorized {
		t.Errorf("the page itself still needs a session: %d", rec.Code)
	}
}
