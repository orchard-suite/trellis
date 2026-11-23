package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/orchard-suite/trellis/internal/board"
	"github.com/orchard-suite/trellis/internal/server"
)

func TestRefreshFailsWhenASinkDid(t *testing.T) {
	answer := `{"refreshed_at":"2026-11-22T18:00:00Z","took_ms":1000,"level":"red","open":3,"sources":[{"source":"orrery","outcome":"ok"}],"sent":[{"sink":"orrery","ok":false,"error":"orrery rejected 2 of 9 entities"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/refresh" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, answer)
	}))
	defer srv.Close()
	pass, err := askPass(context.Background(), srv.Client(), srv.URL+"/v1/refresh")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	var fail *failure
	if err := reportPass(&out, pass); !errors.As(err, &fail) || fail.n != 1 {
		t.Errorf("a failed sink fails the command: %v", err)
	}
	if !strings.Contains(out.String(), "orrery rejected 2 of 9 entities") {
		t.Errorf("says why: %s", out.String())
	}
	if err := reportPass(io.Discard, &server.Pass{Sent: []board.Send{{Sink: "orrery", OK: true}}}); err != nil {
		t.Errorf("all sent: %v", err)
	}
}
