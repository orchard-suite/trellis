package waivers

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
)

func TestCollectAddsValidWaiversAndSetsTheRestAside(t *testing.T) {
	path := filepath.Join(t.TempDir(), "waivers.yaml")
	body := `waivers:
  - rule: api.breaking
    subject: github/example/app/v1
    reason: The definition now documents a body the route always required.
    owner: the API's owner
    expires_at: 2026-12-01T00:00:00Z
  - rule: repo.no-ci
    subject: github/example/mirror
    reason: no owner
    expires_at: 2026-12-01T00:00:00Z
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	e := estate.New(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC))
	e.Writer("catalog").AddWaivers([]estate.Waiver{{Rule: "a.b", Subject: "s", Reason: "from the catalog", Owner: "o", ExpiresAt: e.Now.Add(time.Hour)}})
	if err := (&Source{Path: path}).Collect(context.Background(), e.Writer("test")); err != nil {
		t.Fatal(err)
	}
	if len(e.Waivers) != 3 || e.Waivers[1].From != "test" || e.Waivers[2].Problem() == "" || !e.WaiversKnown {
		t.Errorf("both stores' waivers, each stamped with its store, the one with no owner not counting: %+v", e.Waivers)
	}
	if err := os.WriteFile(path, []byte("waivers:\n  - rule: x\n    colour: blue\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (&Source{Path: path}).Collect(context.Background(), estate.New(time.Now()).Writer("test")); err == nil {
		t.Error("an unknown field was read")
	}
}
