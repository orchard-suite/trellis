package repo

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
)

const v1 = `openapi: 3.1.0
paths:
  /v1/things:
    get: { operationId: listThings, responses: { default: { description: an error } } }
  /v1/things/{id}:
    delete: { operationId: dropThing, responses: { default: { description: an error } } }
`

// checkout makes a git repository with orrery.yaml and openapi.yaml at v1,
// tagged app-v1.0.0, then another package's tag on top.
func checkout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=agent@example.test", "-c", "user.name=agent", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	git("remote", "add", "origin", "git@github.com:example/app.git")
	write("AGENTS.md", "# app\n")
	write("orrery.yaml", "service: github/example/app\nprovides:\n- {id: v1, title: App API, kind: rest, spec: openapi.yaml}\n- {id: mcp, title: App MCP, kind: mcp}\n")
	write("openapi.yaml", v1)
	write("Makefile", "default: image\nimage:\n\tdocker build .\n")
	git("add", ".")
	git("commit", "-q", "-m", "v1")
	git("tag", "app-v1.0.0")
	write("other.txt", "x")
	git("add", ".")
	git("commit", "-q", "-m", "other")
	git("tag", "other-v9.0.0")
	return dir
}

func collect(t *testing.T, dir, base string) *estate.Estate {
	t.Helper()
	e := estate.New(time.Now())
	s := &Source{Dir: dir, Base: base, Match: "app-v*", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := s.Collect(context.Background(), e.Writer("test")); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestCollectReadsTheCheckout(t *testing.T) {
	dir := checkout(t)
	e := collect(t, dir, "")
	r, ok := e.Find("repository", "github/example/app")
	if !ok {
		t.Fatalf("repository keyed by its remote: %v", e.Kinds())
	}
	files := r.Facts["forge"].Value.(map[string]any)["files"].([]any)
	if len(files) != 5 || files[0] != "AGENTS.md" {
		t.Errorf("tree: %v", files)
	}
	if mk := r.Facts["build"].Value.(map[string]any)["make"].(map[string]any); mk["default"] != "default" || mk["present"] != true {
		t.Errorf("the checkout's Makefile is read: %+v", mk)
	}
	api, ok := e.Find("api", "github/example/app/v1")
	if !ok || api.Facts["definition"].Value.(map[string]any)["format"] != "openapi" {
		t.Fatalf("v1: %+v", api)
	}
	if b := api.Facts["breaking"].Value.(map[string]any); b["base"] != "app-v1.0.0" || len(b["changes"].([]any)) != 0 {
		t.Errorf("unchanged since the release: %+v", b)
	}
	if mcp, _ := e.Find("api", "github/example/app/mcp"); mcp.Facts["definition"].Value != nil {
		t.Error("an API with no definition reads none")
	}
}

func TestCollectFindsABreakingChange(t *testing.T) {
	dir := checkout(t)
	gone := strings.Replace(v1, "  /v1/things/{id}:\n    delete: { operationId: dropThing, responses: { default: { description: an error } } }\n", "", 1)
	if err := os.WriteFile(filepath.Join(dir, "openapi.yaml"), []byte(gone), 0o644); err != nil {
		t.Fatal(err)
	}
	api, _ := collect(t, dir, "").Find("api", "github/example/app/v1")
	changes := api.Facts["breaking"].Value.(map[string]any)["changes"].([]any)
	if len(changes) != 1 || changes[0] != "DELETE /v1/things/{id} is gone" {
		t.Errorf("changes: %v", changes)
	}
	if api, _ := collect(t, dir, "none").Find("api", "github/example/app/v1"); api.Facts["breaking"].Value != nil {
		t.Error("-base none compares nothing")
	}
}
