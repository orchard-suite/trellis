package github

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
)

func TestCollectReadsTheTreesOfTheOwners(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		asked = append(asked, r.URL.Path+"?"+r.URL.RawQuery)
		switch r.URL.Path {
		case "/repos/Example/app", "/repos/example/new", "/repos/example/huge":
			_, _ = io.WriteString(w, `{"private":true}`)
		case "/repos/Example/app/git/trees/main":
			_, _ = io.WriteString(w, `{"tree":[{"path":"AGENTS.md","type":"blob","sha":"a1"},{"path":"web","type":"tree"},{"path":"web/CLAUDE.md","type":"blob","sha":"c1"},{"path":"Makefile","type":"blob","sha":"m1"}],"truncated":false}`)
		case "/repos/Example/app/git/blobs/m1":
			if r.Header.Get("Accept") != "application/vnd.github.raw+json" {
				http.Error(w, "raw only", http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, "default: ios\nios:\n\tbash install.command\n")
		case "/repos/example/new/git/trees/HEAD":
			http.Error(w, `{"message":"Git Repository is empty."}`, http.StatusConflict)
		case "/repos/example/huge/git/trees/HEAD":
			_, _ = io.WriteString(w, `{"tree":[],"truncated":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	e := estate.New(time.Now())
	repo := func(key, owner, name, branch string, archived, fork bool) {
		_ = e.Writer("test").Subject("repository", key, key).Set("repo", map[string]any{"owner": owner, "name": name, "branch": branch, "archived": archived, "fork": fork})
	}
	repo("app", "Example", "app", "main", false, false)
	repo("huge", "example", "huge", "", false, false)
	repo("new", "example", "new", "", false, false)
	repo("gone", "example", "gone", "", true, false)
	repo("fork", "example", "fork", "", false, true)
	repo("other", "someone-else", "x", "", false, false)
	forge := func(key string) map[string]any {
		s, _ := e.Find("repository", key)
		f, _ := s.Facts["forge"].Value.(map[string]any)
		return f
	}

	p := New(Config{API: srv.URL, Token: "tok", Owners: []string{"example"}, Concurrency: 2}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := p.Collect(context.Background(), e.Writer("test")); err != nil {
		t.Fatal(err)
	}
	if files, _ := forge("app")["files"].([]any); len(files) != 3 || files[0] != "AGENTS.md" || files[1] != "Makefile" || files[2] != "web/CLAUDE.md" {
		t.Errorf("app tree, blobs only, by path: %+v", forge("app"))
	}
	if forge("app")["private"] != true {
		t.Errorf("visibility is read: %+v", forge("app"))
	}
	app, _ := e.Find("repository", "app")
	build, _ := app.Facts["build"].Value.(map[string]any)
	mk, _ := build["make"].(map[string]any)
	if mk["present"] != true || mk["default"] != "default" {
		t.Errorf("the Makefile is read into the build fact: %+v", build)
	}
	if n := forge("new"); n["empty"] != true {
		t.Errorf("an empty repository is known to be empty: %+v", n)
	}
	if h, _ := e.Find("repository", "huge"); forge("huge") != nil || h.Missed["forge"].Reason == "" {
		t.Errorf("a truncated tree is a miss with why, not partial: %+v", h)
	}
	for _, k := range []string{"gone", "fork", "other"} {
		if forge(k) != nil {
			t.Errorf("%s should not be read", k)
		}
	}
	// Per live repository of the owners: its visibility and its tree, and
	// the one build file.
	if len(asked) != 7 {
		t.Errorf("requests: %v", asked)
	}
	asked = nil
	if err := p.Collect(context.Background(), e.Writer("test")); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 6 {
		t.Errorf("a blob already read is not fetched again: %v", asked)
	}
}

func TestCollectFailsWhenNothingCouldBeRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusBadGateway) }))
	defer srv.Close()
	e := estate.New(time.Now())
	_ = e.Writer("test").Subject("repository", "app", "app").Set("repo", map[string]any{"owner": "example", "name": "app"})
	p := New(Config{API: srv.URL, Owners: []string{"example"}}, slog.New(slog.DiscardHandler))
	if err := p.Collect(context.Background(), e.Writer("test")); err == nil {
		t.Fatal("a forge that answers nothing fails the source")
	}
}
