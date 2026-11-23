package azure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestQueryPagesAndScopesToManagementGroups(t *testing.T) {
	var bodies []map[string]any
	throttled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if !throttled {
			throttled = true
			w.Header().Set("x-ms-user-quota-resets-after", "00:00:03")
			http.Error(w, `{"error":{"code":"RateLimiting"}}`, http.StatusTooManyRequests)
			return
		}
		if body["options"].(map[string]any)["$skipToken"] == nil {
			w.Header().Set("x-ms-user-quota-remaining", "0")
			w.Header().Set("x-ms-user-quota-resets-after", "00:00:02")
			io.WriteString(w, `{"data":[{"id":"a"}],"$skipToken":"next"}`)
			return
		}
		io.WriteString(w, `{"data":[{"id":"b"}]}`)
	}))
	defer srv.Close()
	signIns := 0
	m := NewManagement(srv.URL, func(context.Context) (string, error) { signIns++; return "tok", nil }, srv.Client())
	var waits []time.Duration
	m.Sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }

	rows, err := m.Query(context.Background(), Scope{ManagementGroups: []string{"root"}}, "policyresources", "id")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1]["id"] != "b" {
		t.Fatalf("rows %v", rows)
	}
	for _, b := range bodies {
		if q, _ := b["query"].(string); q != "policyresources\n| order by id asc" {
			t.Errorf("a page was asked for with no order, so pages may repeat and skip rows: %q", q)
		}
	}
	if mgs, _ := bodies[0]["managementGroups"].([]any); len(mgs) != 1 || mgs[0] != "root" || bodies[0]["subscriptions"] != nil {
		t.Errorf("scope not sent as management groups: %v", bodies[0])
	}
	if len(waits) != 2 || waits[0] != 3*time.Second || waits[1] != 2*time.Second {
		t.Errorf("waits %v: want the throttle's reset, then the spent quota's", waits)
	}
	if signIns != 1 {
		t.Errorf("signed in %d times for one query", signIns)
	}
	if _, err := m.Query(context.Background(), Scope{Subscriptions: []string{"s"}, ManagementGroups: []string{"m"}}, "x", "id"); err == nil {
		t.Error("a scope naming both was sent")
	}
}

// TestQueryReadsEveryRowOnceOrFails holds Query to a server that pages the
// way Resource Graph does: by position, over whatever order the query
// gives, and in a different order each time when it gives none.
func TestQueryReadsEveryRowOnceOrFails(t *testing.T) {
	const total = 2500
	calls := 0
	var truncated any = "false"
	repeat := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query   string `json:"query"`
			Options struct {
				Skip string `json:"$skipToken"`
			} `json:"options"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls++
		at, _ := strconv.Atoi(body.Options.Skip)
		ids := make([]int, total)
		for i := range ids {
			ids[i] = i
		}
		if !strings.HasSuffix(body.Query, "| order by id asc") {
			// Unordered: each request sees the rows turned by a different amount.
			for i := range ids {
				ids[i] = (i + calls*700) % total
			}
		}
		end := min(at+1000, total)
		page := map[string]any{"resultTruncated": truncated}
		var data []map[string]any
		for _, id := range ids[at:end] {
			data = append(data, map[string]any{"id": fmt.Sprintf("r%04d", id)})
		}
		if repeat && at > 0 {
			data[0] = map[string]any{"id": "r0000"}
		}
		page["data"] = data
		if end < total && truncated != "true" {
			page["$skipToken"] = strconv.Itoa(end)
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer srv.Close()
	m := NewManagement(srv.URL, func(context.Context) (string, error) { return "tok", nil }, srv.Client())

	rows, err := m.Query(context.Background(), Scope{}, "resources | project id", "id")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r["id"].(string)] = true
	}
	if len(rows) != total || len(seen) != total {
		t.Errorf("read %d rows, %d of them distinct; want each of %d once", len(rows), len(seen), total)
	}

	repeat = true
	if _, err := m.Query(context.Background(), Scope{}, "resources | project id", "id"); err == nil || !strings.Contains(err.Error(), "came back twice") {
		t.Errorf("a key that came back twice answered %v; the read is not whole and must fail", err)
	}
	repeat, truncated = false, "true"
	if _, err := m.Query(context.Background(), Scope{}, "resources | project id", "id"); err == nil || !strings.Contains(err.Error(), "cut the result short") {
		t.Errorf("a result Azure cut short answered %v; it must fail", err)
	}
	if _, err := m.Query(context.Background(), Scope{}, "resources", "id asc | take 1"); err == nil {
		t.Error("a key that is not a column name was sent")
	}
}

func TestGetSaysNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/providers/x/found" && r.URL.Query().Get("api-version") == "v1" {
			io.WriteString(w, `{"name":"found"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	m := NewManagement(srv.URL, func(context.Context) (string, error) { return "tok", nil }, srv.Client())
	var out struct{ Name string }
	if err := m.Get(context.Background(), "/providers/x/found", "v1", &out); err != nil || out.Name != "found" {
		t.Fatalf("got %+v, %v", out, err)
	}
	if err := m.Get(context.Background(), "/providers/x/gone", "v1", &out); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing id answered %v", err)
	}
}

func TestQuotaResetIsBounded(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "600")
	if got := quotaReset(h, time.Second); got != time.Minute {
		t.Errorf("waited %s; a wait is at most a minute", got)
	}
	if got := quotaReset(http.Header{}, 7*time.Second); got != 7*time.Second {
		t.Errorf("no header waited %s, not the fallback", got)
	}
}
