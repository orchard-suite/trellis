package orrery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orchard-suite/trellis"
	"github.com/orchard-suite/trellis/internal/board"
	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/meter"
	"github.com/orchard-suite/trellis/internal/pack"
	"github.com/orchard-suite/trellis/internal/sources"
)

// modelHub is a hub with a model: it lists the blueprints it has, answers
// a list it lacks as unknown, and refuses an offered blueprint whose
// relation targets a blueprint it lacks, as the hub does. It keeps what
// each committed run wrote, by scope.
type modelHub struct {
	mu       sync.Mutex
	lists    map[string][]map[string]any
	offers   int
	open     map[string]string
	pending  map[string][]map[string]any
	written  map[string][]map[string]any
	refuseBy string
}

func newModelHub(lists map[string][]map[string]any) *modelHub {
	return &modelHub{lists: lists, open: map[string]string{}, pending: map[string][]map[string]any{}, written: map[string][]map[string]any{}}
}

func (h *modelHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	answer := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/blueprints":
		ids := []map[string]any{}
		for id := range h.lists {
			ids = append(ids, map[string]any{"id": id})
		}
		answer(http.StatusOK, map[string]any{"blueprints": ids})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/entities":
		bp := r.URL.Query().Get("blueprint")
		list, ok := h.lists[bp]
		if !ok {
			answer(http.StatusNotFound, map[string]any{"error": fmt.Sprintf("Unknown blueprint %q", bp)})
			return
		}
		answer(http.StatusOK, map[string]any{"total": len(list), "entities": list})
	case r.Method == http.MethodPut && r.URL.Path == "/v1/ingest/blueprints":
		h.offers++
		var body struct {
			Blueprints []map[string]any `json:"blueprints"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		arriving := map[string]bool{}
		for _, bp := range body.Blueprints {
			arriving[bp["id"].(string)] = true
		}
		taken, rejected := []string{}, []map[string]any{}
		for _, bp := range body.Blueprints {
			id := bp["id"].(string)
			reason := ""
			if h.refuseBy != "" {
				reason = h.refuseBy
			}
			rels, _ := bp["relations"].(map[string]any)
			for name, def := range rels {
				target, _ := def.(map[string]any)["target"].(string)
				if _, has := h.lists[target]; !has && !arriving[target] {
					reason = fmt.Sprintf("Relation %q targets unknown blueprint %q", name, target)
				}
			}
			if reason != "" {
				rejected = append(rejected, map[string]any{"blueprint": id, "reason": reason})
				continue
			}
			if _, has := h.lists[id]; !has {
				h.lists[id] = nil
			}
			taken = append(taken, id)
		}
		code := http.StatusOK
		if len(rejected) > 0 {
			code = http.StatusMultiStatus
		}
		answer(code, map[string]any{"created": taken, "extended": []string{}, "kept": []string{}, "rejected": rejected})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/ingest/runs":
		var body struct {
			Scope string `json:"scope"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		id := fmt.Sprint("run", len(h.open)+1)
		h.open[id] = body.Scope
		answer(http.StatusOK, map[string]any{"id": id})
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/entities"):
		id := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/ingest/runs/"), "/")[0]
		var body struct {
			Entities []map[string]any `json:"entities"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		h.pending[id] = append(h.pending[id], body.Entities...)
		answer(http.StatusOK, map[string]any{"wanted": []any{}})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/commit"):
		id := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/ingest/runs/"), "/")[0]
		h.written[h.open[id]] = h.pending[id]
		answer(http.StatusOK, map[string]any{"counts": map[string]int{}})
	default:
		answer(http.StatusNotFound, map[string]any{"error": "unexpected " + r.Method + " " + r.URL.Path})
	}
}

// finding is the entity a committed run of the findings scope wrote under
// key, or nil.
func (h *modelHub) finding(key string) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.written[Scope] {
		if e["key"] == key {
			return e
		}
	}
	return nil
}

const twoListsPack = `
pack: acme
title: Acme
version: 1.0.0
family: platform
rules:
  - id: app.ok
    title: App is not ok
    kind: app
    severity: high
    standard: s
    pillar: platform/operations
    needs: { app.ok: the app was not read }
    check: app.ok
    detail: '"not ok"'
    fix: Fix it.
  - id: tool.ok
    title: Tool is not ok
    kind: tool
    severity: high
    standard: s
    pillar: platform/operations
    needs: { tool.ok: the tool was not read }
    check: tool.ok
    detail: '"not ok"'
    fix: Fix it.
`

// twoLists is a board over one hub: the estate maps two lists, and the
// sink writes what the board finds back to the same hub.
func twoLists(t *testing.T, hub *modelHub) *board.Board {
	t.Helper()
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := NewClient(srv.URL, "tok", log)
	client.retries = 0
	metrics, err := meter.New()
	if err != nil {
		t.Fatal(err)
	}
	src, err := NewSource(client, []Mapping{
		{Kind: "app", Blueprint: "app_list", Fields: map[string]string{"ok": "entity.fields.ok"}},
		{Kind: "tool", Blueprint: "tool_list", Fields: map[string]string{"ok": "entity.fields.ok"}},
	}, false, log)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := NewSink(client, "https://trellis.test", trellis.Blueprints, Write{}, nil, src.Linked, metrics, log)
	if err != nil {
		t.Fatal(err)
	}
	p, err := pack.Parse([]byte(twoListsPack), "test")
	if err != nil {
		t.Fatal(err)
	}
	set := &pack.Set{Packs: []*pack.Pack{p}}
	return board.New([]sources.Named{{Name: "orrery", Type: "orrery", Source: src, MaxAge: time.Hour}}, set, judge.Options{Params: judge.Merge(set)}, metrics, log, board.Sink{Name: "orrery", Push: sink.Push})
}

func entity(key string, ok bool) map[string]any {
	return map[string]any{"key": key, "title": key, "fields": map[string]any{"ok": ok}, "relations": map[string]any{}}
}

func TestAListTheHubLacksIsAMissAndTheRestIsWritten(t *testing.T) {
	hub := newModelHub(map[string][]map[string]any{"app_list": {entity("a", false)}})
	b := twoLists(t, hub)

	first := b.Refresh(context.Background())
	run := first.Runs[0]
	if run.Outcome != sources.OK {
		t.Fatalf("a list the hub lacks does not fail the read of the others: %+v", run)
	}
	if len(run.Gaps) != 1 || run.Gaps[0].Kind != "tool" || run.Gaps[0].Fact != "tool" || !strings.Contains(run.Gaps[0].Reason, "the hub has no list tool_list") || run.Missed != 1 {
		t.Errorf("the missing list is a miss on the run, with why: %+v", run)
	}
	if open := first.Evaluation.Open(); len(open) != 1 || open[0].Rule != "app.ok" {
		t.Errorf("the list the hub has is judged as usual: %+v", open)
	}
	f := hub.finding("app.ok/a")
	if f == nil {
		t.Fatalf("the finding on the list the hub has is written: %+v", hub.written)
	}
	if rels, _ := f["relations"].(map[string]any); rels["app_list"] != "app_list:a" {
		t.Errorf("and links to its entity: %+v", f["relations"])
	}
	if hub.offers != 1 {
		t.Errorf("one offer, taken: %d", hub.offers)
	}

	// The list arrives, with something to find.
	hub.mu.Lock()
	hub.lists["tool_list"] = []map[string]any{entity("t", false)}
	hub.mu.Unlock()
	second := b.Refresh(context.Background())
	if run := second.Runs[0]; len(run.Gaps) != 0 || run.Missed != 0 {
		t.Errorf("once the hub has the list nothing is missed: %+v", run)
	}
	f = hub.finding("tool.ok/t")
	if f == nil {
		t.Fatalf("the list that arrived is judged and written: %+v", hub.written[Scope])
	}
	if rels, _ := f["relations"].(map[string]any); rels["tool_list"] != "tool_list:t" {
		t.Errorf("and its findings link to it without a restart: %+v", f["relations"])
	}
	if hub.finding("app.ok/a") == nil || hub.offers != 2 {
		t.Errorf("the rest stands, and the blueprints were offered once more: offers %d", hub.offers)
	}

	third := b.Refresh(context.Background())
	if hub.offers != 2 || len(third.Evaluation.Open()) != 2 {
		t.Errorf("a steady hub is offered nothing again: offers %d", hub.offers)
	}
}

func TestAListThatLeavesTheHubKeepsItsFindingsWhileTheyAreYoung(t *testing.T) {
	hub := newModelHub(map[string][]map[string]any{"app_list": {entity("a", false)}, "tool_list": {entity("t", false)}})
	b := twoLists(t, hub)
	if open := b.Refresh(context.Background()).Evaluation.Open(); len(open) != 2 {
		t.Fatalf("both lists judged: %+v", open)
	}

	hub.mu.Lock()
	delete(hub.lists, "tool_list")
	hub.mu.Unlock()
	snap := b.Refresh(context.Background())
	var tool *judge.Finding
	for i, f := range snap.Evaluation.Open() {
		if f.Rule == "tool.ok" {
			tool = &snap.Evaluation.Open()[i]
		}
	}
	if tool == nil || !tool.Carried {
		t.Fatalf("the finding stands on the last reading, marked carried: %+v", snap.Evaluation.Open())
	}
	if run := snap.Runs[0]; run.Outcome != sources.OK || len(run.Gaps) != 1 || run.Carried != 1 || run.Missed != 0 {
		t.Errorf("the run names the gap and counts what it carried: %+v", run)
	}
	if len(snap.Events) != 0 {
		t.Errorf("a carried finding is no change: %+v", snap.Events)
	}
	if hub.finding("tool.ok/t") == nil || hub.finding("app.ok/a") == nil {
		t.Errorf("both findings stay in the catalog: %+v", hub.written[Scope])
	}
}

func TestARefusedOfferAfterOneWasTakenStillWritesThePass(t *testing.T) {
	hub := newModelHub(map[string][]map[string]any{"app_list": {entity("a", false)}})
	b := twoLists(t, hub)
	b.Refresh(context.Background())
	if hub.finding("app.ok/a") == nil {
		t.Fatal("the first pass is written")
	}

	// The list arrives, and the hub refuses the offer that would link it.
	hub.mu.Lock()
	hub.lists["tool_list"] = []map[string]any{entity("t", false)}
	hub.refuseBy = "the blueprint is defined in git; edit it there"
	hub.mu.Unlock()
	b.Refresh(context.Background())
	f := hub.finding("tool.ok/t")
	if f == nil {
		t.Fatalf("a refused offer does not stop the pass being written: %+v", hub.written[Scope])
	}
	if rels, _ := f["relations"].(map[string]any); len(rels) != 0 {
		t.Errorf("a finding uses no relation the hub did not take: %+v", rels)
	}

	hub.mu.Lock()
	hub.refuseBy = ""
	hub.mu.Unlock()
	b.Refresh(context.Background())
	if rels, _ := hub.finding("tool.ok/t")["relations"].(map[string]any); rels["tool_list"] != "tool_list:t" {
		t.Errorf("the offer is made again until the hub takes it: %+v", rels)
	}
}

func TestAnOfferTheHubNeverTookWritesNothing(t *testing.T) {
	hub := newModelHub(map[string][]map[string]any{"app_list": {entity("a", false)}})
	hub.refuseBy = `source "trellis" may not write blueprint`
	b := twoLists(t, hub)
	b.Refresh(context.Background())
	if len(hub.written) != 0 {
		t.Errorf("with no blueprint taken there is nothing to write into: %+v", hub.written)
	}
}
