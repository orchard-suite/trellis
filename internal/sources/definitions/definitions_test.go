package definitions

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
)

const doc = "openapi: 3.1.0\npaths: {/v1/a: {get: {operationId: a}}}\n"

func TestCollectReadsEachLinkOnceFromItsSource(t *testing.T) {
	var forge, served atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forge.Add(1)
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("Accept") != "application/vnd.github.raw" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path + "?" + r.URL.RawQuery {
		case "/repos/example/app/contents/api/openapi.yaml?ref=HEAD":
			io.WriteString(w, doc)
		case "/repos/example/app/contents/api.proto?ref=HEAD":
			io.WriteString(w, "syntax = \"proto3\";\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer gh.Close()
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		io.WriteString(w, `{"swagger":"2.0"}`)
	}))
	defer web.Close()

	e := estate.New(time.Now())
	api := func(key, kind, spec string) {
		_ = e.Writer("test").Subject("api", key, key).Set("api", map[string]any{"kind": kind, "spec": spec})
	}
	link := "https://github.com/example/app/blob/HEAD/api/openapi.yaml"
	api("v1", "rest", link)
	api("v1-again", "rest", link)
	api("gone", "rest", "https://github.com/example/app/blob/HEAD/missing.json")
	api("old", "rest", web.URL+"/swagger.json")
	api("grpc", "grpc", "https://github.com/example/app/blob/HEAD/api.proto")
	api("none", "rest", "")

	p := New(Config{GitHubAPI: gh.URL, GitHubToken: "tok"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := p.Collect(context.Background(), e.Writer("test")); err != nil {
		t.Fatal(err)
	}
	def := func(key string) map[string]any {
		s, _ := e.Find("api", key)
		d, _ := s.Facts["definition"].Value.(map[string]any)
		return d
	}
	if d := def("v1"); d["format"] != "openapi" || len(d["openapi"].(map[string]any)["operations"].([]any)) != 1 {
		t.Errorf("v1 is read: %+v", d)
	}
	if d := def("v1-again"); d["format"] != "openapi" {
		t.Errorf("v1-again shares the read: %+v", d)
	}
	if d := def("gone"); !strings.Contains(d["error"].(string), "404") {
		t.Errorf("a missing file says what the forge answered: %+v", d)
	}
	if d := def("old"); !strings.Contains(d["error"].(string), "Swagger") {
		t.Errorf("a served Swagger document is read without the forge's token and refused: %+v", d)
	}
	if d := def("grpc"); d["format"] != "protobuf" || d["error"] != "" {
		t.Errorf("a protobuf file is named, not parsed: %+v", d)
	}
	if d := def("none"); d != nil {
		t.Error("an API with no link is left unread")
	}
	if forge.Load() != 3 || served.Load() != 1 {
		t.Errorf("reads: forge %d (want 3: v1, gone, the proto), served %d (want 1)", forge.Load(), served.Load())
	}
}

func TestCollectFailsOnlyWhenNothingCouldBeRead(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusBadGateway) }))
	defer down.Close()
	e := estate.New(time.Now())
	api := func(key, kind, spec string) {
		_ = e.Writer("test").Subject("api", key, key).Set("api", map[string]any{"kind": kind, "spec": spec})
	}
	api("a", "rest", down.URL+"/openapi.json")
	p := New(Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := p.Collect(context.Background(), e.Writer("test")); err == nil {
		t.Error("every read failed, so the run fails")
	}
}
