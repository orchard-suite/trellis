// Package server answers on three ports, one per kind of caller:
//
//   - Session: the board, its JSON, and the browser telemetry relay. Trusts
//     the sign-in proxy's headers, so only that proxy may reach it.
//   - Machine: the machine door. Bearer tokens only, never identity
//     headers: /healthz and /readyz for the kubelet with no token, and the
//     judge of one change, POST /v1/judge, for the callers the settings
//     name.
//   - Control: a pass on demand for trellis refresh. It listens on loopback
//     only, so whoever may exec into the pod may ask, and nobody else.
package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/orchard-suite/platformkit/v3/change"

	"github.com/orchard-suite/trellis/internal/board"
	"github.com/orchard-suite/trellis/internal/door"
	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/gate"
	"github.com/orchard-suite/trellis/internal/history"
	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/meter"
	"github.com/orchard-suite/trellis/internal/obs"
	"github.com/orchard-suite/trellis/internal/pack"
	"github.com/orchard-suite/trellis/internal/sources"
	"github.com/orchard-suite/trellis/internal/web"
)

// Config is the server's settings.
type Config struct {
	// PublicURL is where browsers reach the board, such as https://trellis.example.org.
	PublicURL string
	// Estate names what the board judges.
	Estate  string
	SignIn  door.Config
	Version string
	// Callers are who may call the machine door's /v1 routes, by name,
	// each with its bearer token.
	Callers map[string][]byte
}

// Server is the two handlers over one board.
type Server struct {
	cfg     Config
	board   *board.Board
	gate    *door.Gate
	relay   http.Handler
	log     *slog.Logger
	origin  string
	check   Check
	history History
	judge   *gate.Judge
	metrics *meter.Metrics
	// index is the page with its asset URLs under /static/<release>/, so a
	// browser never pairs a new page with an old script.
	index []byte
}

// History is what the board's trend pages read; nil when none is kept.
type History interface {
	Subject(ctx context.Context, kind, key string, since time.Time) (history.SubjectHistory, error)
	Rule(ctx context.Context, rule string, since time.Time) ([]history.Point, error)
	Pack(ctx context.Context, pack string, since time.Time) ([]history.Day, error)
}

// Check runs a pass that starts after the ask and waits for it and its
// sends.
type Check func(context.Context) (*board.Snapshot, error)

// New makes the server. relay is the browser telemetry relay. check runs a
// pass when a person or an operator asks for one. past is the history the
// trend pages read, or nil.
func New(cfg Config, b *board.Board, relay http.Handler, log *slog.Logger, check Check, past History) (*Server, error) {
	u, err := url.Parse(cfg.PublicURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errPublicURL
	}
	gate, err := door.New(cfg.SignIn, log)
	if err != nil {
		return nil, err
	}
	index := bytes.ReplaceAll(web.Index, []byte(`"/static/`), []byte(`"/static/`+assetVersion(cfg.Version)+`/`))
	return &Server{cfg: cfg, board: b, gate: gate, relay: relay, log: log, origin: u.Scheme + "://" + u.Host, check: check, history: past, index: index}, nil
}

// assetVersion is the path segment the page's assets sit under: the
// release, or "dev" for a build without one.
func assetVersion(v string) string {
	if v == "" || strings.ContainsAny(v, "/?#") {
		return "dev"
	}
	return v
}

type configError string

func (e configError) Error() string { return string(e) }

const errPublicURL = configError("public_url must be an absolute URL")

// Session is the board.
func (s *Server) Session() http.Handler {
	mux := http.NewServeMux()
	handle := func(pattern string, h http.Handler) { mux.Handle(pattern, s.gate.Wrap(h)) }
	change := func(h http.Handler) http.Handler { return door.SameOrigin(s.origin, s.log, h) }
	handle("GET /{$}", http.HandlerFunc(s.page))
	handle("GET /static/{path...}", http.HandlerFunc(s.static))
	handle("GET /v1/board", http.HandlerFunc(s.boardJSON))
	handle("GET /v1/findings", http.HandlerFunc(s.findings))
	handle("GET /v1/subject", http.HandlerFunc(s.subject))
	handle("GET /v1/rules", http.HandlerFunc(s.rulesPage))
	handle("GET /v1/history", http.HandlerFunc(s.historyJSON))
	handle("POST /v1/refresh", change(http.HandlerFunc(s.refreshNow)))
	handle("POST /api/telemetry", change(s.relay))
	// A device fetches the icon and the manifest without a session when a
	// person adds the board to a Home Screen or installs it, so these four
	// files skip the gate. They say nothing about the estate.
	for file, contentType := range appFiles {
		mux.Handle("GET /"+file, appFile(file, contentType))
	}
	return obs.Handler(mux, "session")
}

// appFiles are the files a device reads to install the board as an app,
// each with its content type.
var appFiles = map[string]string{
	"apple-touch-icon.png": "image/png",
	"icon-192.png":         "image/png",
	"icon-512.png":         "image/png",
	"manifest.webmanifest": "application/manifest+json",
}

func appFile(name, contentType string) http.Handler {
	body, err := web.App.ReadFile("app/" + name)
	if err != nil {
		panic("web: app file " + name + " is not embedded")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(body)
	})
}

// Machine is the machine door: the kubelet's probes with no token, and the
// /v1 routes for the callers the settings name, by bearer token alone.
func (s *Server) Machine() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /v1/judge", s.bearer(http.HandlerFunc(s.judgeNow)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.cfg.Version})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if s.board.Latest() == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "no snapshot yet"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return obs.Handler(mux, "machine")
}

// bearer admits a call whose Authorization header carries a caller's token,
// compared in constant time with every caller's, and refuses any other with
// 401. It reads no other header: on the machine door an identity header
// proves nothing. It logs both, with the route and never the token.
func (s *Server) bearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := r.Method + " " + r.URL.Path
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		caller := ""
		if ok && tok != "" {
			for name, want := range s.cfg.Callers {
				if subtle.ConstantTimeCompare([]byte(tok), want) == 1 {
					caller = name
				}
			}
		}
		if caller == "" {
			why := "no bearer token"
			if ok && tok != "" {
				why = "a token no caller has"
			}
			s.log.WarnContext(r.Context(), "machine call refused", "route", route, "why", why)
			w.Header().Set("WWW-Authenticate", `Bearer realm="trellis"`)
			writeJSON(w, http.StatusUnauthorized, JudgeError{Error: "unauthorized", Reason: "the machine door takes a caller's bearer token"})
			return
		}
		s.log.InfoContext(r.Context(), "machine call admitted", "route", route, "caller", caller)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'")
	_, _ = w.Write(s.index)
}

// static serves the page's assets. Under /static/<release>/ they never
// change, so a browser keeps them; the page and its relative module imports
// all name the release. Any other asset URL is checked again on every use.
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	prefix := "/static/" + assetVersion(s.cfg.Version) + "/"
	if rest, ok := strings.CutPrefix(r.URL.Path, prefix); ok {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		r = r.Clone(r.Context())
		r.URL.Path = "/static/" + rest
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	// The embedded tree is rooted at static/, the same as the URL path.
	http.FileServerFS(web.Static).ServeHTTP(w, r)
}

// Board is what GET /v1/board answers.
type Board struct {
	Version     string                  `json:"version"`
	Estate      string                  `json:"estate"`
	Level       estate.Level            `json:"level"`
	Tally       judge.Tally             `json:"tally"`
	Rollup      judge.Rollup            `json:"rollup"`
	RefreshedAt *time.Time              `json:"refreshed_at"`
	TookMS      int64                   `json:"took_ms"`
	Sources     []sources.Run           `json:"sources"`
	Packs       []judge.PackVerdict     `json:"packs"`
	Kinds       []judge.KindVerdict     `json:"kinds"`
	Pillars     []judge.PillarVerdict   `json:"pillars"`
	Owners      []judge.OwnerVerdict    `json:"owners"`
	Rules       []Rule                  `json:"rules"`
	Waivers     []judge.WaiverView      `json:"waivers"`
	WaiversRead bool                    `json:"waivers_known"`
	ByState     map[judge.State]int     `json:"by_state"`
	BySeverity  map[estate.Severity]int `json:"by_severity"`
	Events      []board.Event           `json:"events"`
	Person      string                  `json:"person,omitempty"`
	// History says whether the board keeps history, so the page offers
	// trends.
	History bool `json:"history"`
	// PackTiers are each tiered pack's tiers, lowest first.
	PackTiers map[string][]string `json:"pack_tiers"`
}

// Rule is a rule with how it answered across the estate.
type Rule struct {
	*pack.Rule
	Stat judge.RuleStat `json:"stat"`
}

func (s *Server) rules(snap *board.Snapshot) []Rule {
	stats := map[string]judge.RuleStat{}
	if snap != nil {
		for _, st := range snap.Evaluation.Rules {
			stats[st.Rule] = st
		}
	}
	out := []Rule{}
	for _, rule := range s.board.Packs().Rules() {
		if rule.RunsIn(pack.Board) {
			out = append(out, Rule{Rule: rule, Stat: stats[rule.ID]})
		}
	}
	return out
}

func (s *Server) boardJSON(w http.ResponseWriter, r *http.Request) {
	out := Board{Version: s.cfg.Version, Estate: s.cfg.Estate, Level: estate.Grey, Rollup: s.board.Options().Rollup, Person: door.From(r.Context()).Email, History: s.history != nil, Sources: []sources.Run{}, Packs: []judge.PackVerdict{}, Kinds: []judge.KindVerdict{}, Pillars: []judge.PillarVerdict{}, Owners: []judge.OwnerVerdict{}, Waivers: []judge.WaiverView{}, ByState: map[judge.State]int{}, BySeverity: map[estate.Severity]int{}, Events: []board.Event{}}
	snap := s.board.Latest()
	out.Rules = s.rules(snap)
	out.PackTiers = map[string][]string{}
	for _, p := range s.board.Packs().Packs {
		if len(p.Tiers) > 0 {
			out.PackTiers[p.ID] = p.Tiers
		}
	}
	if snap != nil {
		t := snap.RefreshedAt
		out.RefreshedAt = &t
		out.TookMS = snap.Took.Milliseconds()
		out.Sources = snap.Runs
		ev := snap.Evaluation
		out.Level, out.Tally, out.Packs, out.Kinds, out.Pillars, out.WaiversRead = ev.Estate.Level, ev.Estate, ev.Packs, ev.Kinds, ev.Pillars, ev.WaiversKnown
		if ev.Owners != nil {
			out.Owners = ev.Owners
		}
		if ev.Waivers != nil {
			out.Waivers = ev.Waivers
		}
		if snap.Events != nil {
			out.Events = snap.Events
		}
		for _, f := range ev.Findings {
			out.ByState[f.State]++
			if f.State == judge.Open {
				out.BySeverity[f.Severity]++
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// Findings is what GET /v1/findings answers: every finding in every state
// and every subject with its level, after the query's filters.
type Findings struct {
	RefreshedAt *time.Time             `json:"refreshed_at"`
	Findings    []judge.Finding        `json:"findings"`
	Subjects    []judge.SubjectVerdict `json:"subjects"`
}

func (s *Server) findings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	packID, kind, severity, rule, pillar, control, owner, state := q.Get("pack"), q.Get("kind"), estate.Severity(q.Get("severity")), q.Get("rule"), q.Get("pillar"), q.Get("control"), q.Get("owner"), judge.State(q.Get("state"))
	_, ownerSet := q["owner"]
	overdue := q.Get("overdue") == "true"
	filtered := packID != "" || severity != "" || rule != "" || pillar != "" || control != "" || ownerSet || state != "" || overdue
	match := func(f judge.Finding) bool {
		return (packID == "" || f.Pack == packID) && (kind == "" || f.Kind == kind) && (severity == "" || f.Severity == severity) && (rule == "" || f.Rule == rule) && (pillar == "" || f.Pillar == pillar) && (control == "" || slices.Contains(f.Controls, control)) && (!ownerSet || f.Owner == owner) && (state == "" || f.State == state) && (!overdue || f.Overdue)
	}
	keep := func(fs []judge.Finding) []judge.Finding {
		out := []judge.Finding{}
		for _, f := range fs {
			if match(f) {
				out = append(out, f)
			}
		}
		return out
	}
	out := Findings{Findings: []judge.Finding{}, Subjects: []judge.SubjectVerdict{}}
	if snap := s.board.Latest(); snap != nil {
		t := snap.RefreshedAt
		out.RefreshedAt = &t
		out.Findings = keep(snap.Evaluation.Findings)
		for _, sv := range snap.Evaluation.Subjects {
			if kind != "" && sv.Kind != kind {
				continue
			}
			sv.Facts, sv.Missed = nil, nil
			if !filtered {
				out.Subjects = append(out.Subjects, sv)
				continue
			}
			sv.Findings = keep(sv.Findings)
			if len(sv.Findings) > 0 {
				out.Subjects = append(out.Subjects, sv)
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// subject is GET /v1/subject?kind=&key=: one subject with its findings in
// every state, its unchecked rules, and every fact with the source that
// read it and when, so a person or an agent has what a fix needs in one
// answer.
func (s *Server) subject(w http.ResponseWriter, r *http.Request) {
	kind, key := r.URL.Query().Get("kind"), r.URL.Query().Get("key")
	snap := s.board.Latest()
	if snap != nil {
		for _, sv := range snap.Evaluation.Subjects {
			if sv.Kind == kind && sv.Key == key {
				w.Header().Set("Cache-Control", "no-store")
				writeJSON(w, http.StatusOK, sv)
				return
			}
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such subject on the board"})
}

func (s *Server) rulesPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"rules": s.rules(s.board.Latest())})
}

func (s *Server) refreshNow(w http.ResponseWriter, r *http.Request) {
	s.log.InfoContext(r.Context(), "refresh asked", "person", door.From(r.Context()).Email)
	go func() { _, _ = s.check(context.WithoutCancel(r.Context())) }()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "refreshing"})
}

// Control is the operator's port: POST /v1/refresh runs a pass and answers
// when it and its sends are done.
func (s *Server) Control() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/refresh", s.refreshWait)
	return obs.Handler(mux, "control")
}

// UseJudge turns on POST /v1/judge with the judge, counting each answer in
// metrics. Without it the route answers that the judge is off.
func (s *Server) UseJudge(j *gate.Judge, metrics *meter.Metrics) {
	s.judge, s.metrics = j, metrics
}

// MaxStatement is the most a judge request may hold: a statement at the
// kit's limits on things, effects, and unknown parts fits well inside it.
const MaxStatement = 8 << 20

// JudgeRequest is what POST /v1/judge takes.
type JudgeRequest struct {
	Statement *change.Statement `json:"statement"`
}

// JudgeError is what POST /v1/judge answers when it gives no route: a code
// a caller can act on, and the words.
type JudgeError struct {
	Error  string `json:"error"`
	Reason string `json:"reason"`
}

// judgeNow answers the route for one statement: 200 with the answer, 400
// for a statement the kit's validator refuses, 503 when the judge is off,
// and 500 when it could not answer. A caller sends a change to people on
// anything but 200.
func (s *Server) judgeNow(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	started := time.Now()
	refuse := func(status int, code, reason string) {
		outcome := "refused"
		if status >= 500 {
			outcome = "failed"
		}
		s.metrics.Judged(ctx, outcome, time.Since(started))
		s.log.WarnContext(ctx, "change not judged", "status", status, "error", code, "reason", reason)
		writeJSON(w, status, JudgeError{Error: code, Reason: reason})
	}
	if s.judge == nil {
		refuse(http.StatusServiceUnavailable, "judge_off", "this board has no gate settings, so it judges no change")
		return
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxStatement))
	dec.DisallowUnknownFields()
	var req JudgeRequest
	if err := dec.Decode(&req); err != nil {
		refuse(http.StatusBadRequest, "bad_json", "the body is not a judge request: "+err.Error())
		return
	}
	if req.Statement == nil {
		refuse(http.StatusBadRequest, "missing_statement", "the body names no statement")
		return
	}
	if err := req.Statement.Validate(s.judge.Kinds()); err != nil {
		code := change.Reason(err)
		if code == "" {
			code = "bad_statement"
		}
		refuse(http.StatusBadRequest, code, err.Error())
		return
	}
	ans, err := s.judge.Judge(ctx, req.Statement)
	if err != nil {
		s.log.ErrorContext(ctx, "the judge could not answer", "key", req.Statement.Key, "error", err)
		refuse(http.StatusInternalServerError, "judge_failed", err.Error())
		return
	}
	took := time.Since(started)
	s.metrics.Judged(ctx, ans.Route.To, took)
	digest, _ := req.Statement.Digest()
	s.log.InfoContext(ctx, "change judged", "key", req.Statement.Key, "digest", digest, "route", ans.Route.To, "reasons", len(ans.Reasons), "drafts", len(ans.Drafts), "effects", len(req.Statement.Effects), "took", took.Round(time.Microsecond).String())
	writeJSON(w, http.StatusOK, ans)
}

// Pass is what the control port's POST /v1/refresh answers: how the pass
// it ran read each source and how each sink took it.
type Pass struct {
	RefreshedAt time.Time     `json:"refreshed_at"`
	TookMS      int64         `json:"took_ms"`
	Level       estate.Level  `json:"level"`
	Open        int           `json:"open"`
	Sources     []sources.Run `json:"sources"`
	Sent        []board.Send  `json:"sent"`
}

func (s *Server) refreshWait(w http.ResponseWriter, r *http.Request) {
	s.log.InfoContext(r.Context(), "refresh asked on the control port")
	snap, err := s.check(r.Context())
	if err != nil {
		s.log.WarnContext(r.Context(), "refresh waiter left before the pass ended; it runs on", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "left before the pass ended; it runs on"})
		return
	}
	out := Pass{RefreshedAt: snap.RefreshedAt, TookMS: snap.Took.Milliseconds(), Level: snap.Evaluation.Estate.Level, Open: len(snap.Evaluation.Open()), Sources: snap.Runs, Sent: snap.Sent}
	if out.Sources == nil {
		out.Sources = []sources.Run{}
	}
	if out.Sent == nil {
		out.Sent = []board.Send{}
	}
	writeJSON(w, http.StatusOK, out)
}

// historyJSON is one subject's, rule's, or pack's past over ?days= (90 by
// default, at most 1095): ?kind=&subject=, ?rule=, or ?pack=.
func (s *Server) historyJSON(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this board keeps no history"})
		return
	}
	q := r.URL.Query()
	days := 90
	if v := q.Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1095 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "days is a whole number from 1 to 1095"})
			return
		}
		days = n
	}
	since := time.Now().UTC().AddDate(0, 0, -days)
	ctx := r.Context()
	var out any
	var err error
	switch {
	case q.Get("subject") != "" && q.Get("kind") != "":
		out, err = s.history.Subject(ctx, q.Get("kind"), q.Get("subject"), since)
	case q.Get("rule") != "":
		out, err = s.history.Rule(ctx, q.Get("rule"), since)
	case q.Get("pack") != "":
		out, err = s.history.Pack(ctx, q.Get("pack"), since)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name a subject (kind and subject), a rule, or a pack"})
		return
	}
	if err != nil {
		s.log.ErrorContext(ctx, "history read failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "history could not be read"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"since": since, "days": days, "history": out})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
