package webhook

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/board"
	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/meter"
)

func TestPushPostsTheVerdict(t *testing.T) {
	var got Body
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	metrics, err := meter.New()
	if err != nil {
		t.Fatal(err)
	}
	s := New(srv.URL, "tok", "work", metrics, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f := judge.Finding{Rule: "a.b", Subject: "x", State: judge.Open, Owner: "team"}
	s.Push(context.Background(), &board.Snapshot{RefreshedAt: time.Now(), Evaluation: judge.Evaluation{Estate: judge.Tally{Level: estate.Red}, Findings: []judge.Finding{f, {Rule: "a.c", Subject: "x", State: judge.Waived}}}, Events: []board.Event{{Type: board.Opened, Owner: "team", Finding: &f}}})
	if auth != "Bearer tok" || got.Estate != "work" || got.Level != estate.Red || len(got.Findings) != 1 || len(got.Events) != 1 || got.Events[0].Owner != "team" {
		t.Errorf("posted the open finding and what changed, with its owner: %+v with %q", got, auth)
	}
}
