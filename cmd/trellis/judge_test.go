package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/orchard-suite/platformkit/v3/change"

	"github.com/orchard-suite/trellis/internal/config"
)

// The example estate's settings make a judge: the base gate pack and the
// example's own, with its default approvers.
func TestTheExampleEstateHasAJudge(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	t.Setenv("FINDINGS_WEBHOOK_URL", "https://hooks.example.org/findings")
	cfg, err := config.Load("../../deploy/kubernetes/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Packs.Dir = "../../deploy/kubernetes/packs"
	j, err := newJudge(cfg, "", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if j.Pack() != "example-gate@1.0.0" {
		t.Fatalf("pack %s", j.Pack())
	}
	if _, ok := j.Kinds().Get("sends"); !ok {
		t.Fatal("the example's own effect kind is registered")
	}
}

// trellis judge posts the statement as it was written, under statement.
func TestJudgePostsTheStatementAsWritten(t *testing.T) {
	var got struct {
		Statement change.Statement `json:"statement"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/judge" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"unknown_kind","reason":"effects[0].kind \"teleports\" is not a registered effect kind"}`)
	}))
	defer srv.Close()
	status, body, err := askJudge(t.Context(), srv.Client(), srv.URL+"/v1/judge", []byte(` {"key":"run-1001","applier":"hub"}
`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Statement.Key != "run-1001" || got.Statement.Applier != "hub" {
		t.Fatalf("posted %+v", got.Statement)
	}
	if status != http.StatusBadRequest || len(body) == 0 {
		t.Fatalf("%d %s", status, body)
	}
	if _, _, err := askJudge(t.Context(), srv.Client(), srv.URL+"/v1/judge", []byte("not json")); err == nil {
		t.Fatal("a statement that is not JSON is refused before it is sent")
	}
}
