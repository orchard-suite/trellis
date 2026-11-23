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
)

// fakeHub is an ingest service that keeps the digests it was sent, as the
// hub does, and records each batch.
type fakeHub struct {
	sightings bool
	mu        sync.Mutex
	digests   map[string]string
	batches   [][]map[string]any
	committed int
}

func (h *fakeHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/ingest/runs":
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "run1", "sightings": h.sightings})
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/entities"):
		var body struct {
			Entities []map[string]any `json:"entities"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		h.batches = append(h.batches, body.Entities)
		wanted := []Ref{}
		for _, e := range body.Entities {
			id := fmt.Sprint(e["blueprint"], ":", e["key"])
			d, _ := e["digest"].(string)
			if len(e) > 3 {
				h.digests[id] = d
			} else if h.digests[id] == "" || h.digests[id] != d {
				wanted = append(wanted, Ref{Blueprint: e["blueprint"].(string), Key: e["key"].(string)})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"accepted": len(body.Entities), "wanted": wanted})
	case strings.HasSuffix(r.URL.Path, "/commit"):
		h.committed++
		_ = json.NewEncoder(w).Encode(map[string]any{"counts": map[string]int{}})
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

func hubClient(t *testing.T, h *fakeHub) *Client {
	t.Helper()
	h.digests = map[string]string{}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "tok", slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func someEntities(n int) []Entity {
	out := make([]Entity, n)
	for i := range out {
		out[i] = Entity{Blueprint: "plat_verdict", Key: fmt.Sprint("k", i), Title: fmt.Sprint("K", i), Fields: map[string]any{"tier": "gold", "rank": i, "z": 1, "a": 2}}
	}
	return out
}

func TestASnapshotSightsFirstAndSendsOnlyWhatTheHubWants(t *testing.T) {
	h := &fakeHub{sightings: true}
	c := hubClient(t, h)
	ctx := context.Background()

	first, err := c.Snapshot(ctx, Scope, someEntities(3))
	if err != nil {
		t.Fatal(err)
	}
	if first.Sent != 3 || first.Sighted != 0 || len(h.batches) != 2 {
		t.Fatalf("a new hub wants everything in full: %+v, %d batches", first, len(h.batches))
	}
	for _, e := range h.batches[0] {
		if len(e) != 3 || e["digest"] == "" {
			t.Errorf("a sighting is the blueprint, the key, and the digest: %v", e)
		}
	}
	for _, e := range h.batches[1] {
		if e["digest"] != Digest(Entity{Blueprint: "plat_verdict", Key: e["key"].(string), Title: e["title"].(string), Fields: map[string]any{"tier": "gold", "rank": e["fields"].(map[string]any)["rank"], "z": 1, "a": 2}}) || e["fields"] == nil {
			t.Errorf("a full item carries its own digest: %v", e)
		}
	}

	h.batches = nil
	moved := someEntities(3)
	moved[1].Fields["tier"] = "silver"
	again, err := c.Snapshot(ctx, Scope, moved)
	if err != nil {
		t.Fatal(err)
	}
	if again.Sighted != 2 || again.Sent != 1 || len(h.batches) != 2 || len(h.batches[1]) != 1 || h.batches[1][0]["key"] != "k1" {
		t.Fatalf("only the moved entity goes in full: %+v, %v", again, h.batches)
	}
	if h.committed != 2 {
		t.Errorf("each snapshot commits once: %d", h.committed)
	}
}

func TestASnapshotSendsInFullToAHubThatTakesNoSightings(t *testing.T) {
	h := &fakeHub{}
	c := hubClient(t, h)
	counts, err := c.Snapshot(context.Background(), Scope, someEntities(batchSize+1))
	if err != nil {
		t.Fatal(err)
	}
	if counts.Sent != batchSize+1 || counts.Sighted != 0 || len(h.batches) != 2 {
		t.Fatalf("everything goes in full, batch by batch: %+v, %d batches", counts, len(h.batches))
	}
	for _, b := range h.batches {
		for _, e := range b {
			if _, has := e["digest"]; has || e["fields"] == nil {
				t.Fatalf("a hub without sightings gets plain entities: %v", e)
			}
		}
	}
}

func TestTheDigestIsTheSameForTheSameEntity(t *testing.T) {
	a := Entity{Blueprint: "b", Key: "k", Fields: map[string]any{"x": 1, "y": []any{"a", "b"}, "m": map[string]any{"q": 1, "p": 2}}}
	b := Entity{Blueprint: "b", Key: "k", Fields: map[string]any{"m": map[string]any{"p": 2, "q": 1}, "y": []any{"a", "b"}, "x": 1}}
	if Digest(a) != Digest(b) {
		t.Error("map order moved the digest")
	}
	b.Fields["x"] = 2
	if Digest(a) == Digest(b) {
		t.Error("a changed value kept the digest")
	}
}
