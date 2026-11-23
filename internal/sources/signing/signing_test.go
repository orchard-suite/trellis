package signing

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
)

type fake map[string]Signature

func (f fake) Verify(_ context.Context, ref string) Signature { return f[ref] }

func TestCollect(t *testing.T) {
	e := estate.New(time.Now())
	for _, ref := range []string{"ghcr.io/example/signed:1", "ghcr.io/example/plain:1", "ghcr.io/example/broken:1", "quay.io/other/thing:2"} {
		e.Writer("test").Subject("image", ref, ref)
	}
	sig := func(ref string) map[string]any {
		s, _ := e.Find("image", ref)
		f, _ := s.Facts["signing"].Value.(map[string]any)
		return f
	}
	v := fake{
		"ghcr.io/example/signed:1": {Signed: true, SBOM: true, Identity: "https://github.com/example/signed/.github/workflows/build.yml@refs/tags/v1"},
		"ghcr.io/example/plain:1":  {},
		"ghcr.io/example/broken:1": {Error: "registry: 503"},
	}
	p := New(v, regexp.MustCompile(`^ghcr\.io/example/`), 2, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := p.Collect(context.Background(), e.Writer("test")); err != nil {
		t.Fatal(err)
	}
	if s := sig("ghcr.io/example/signed:1"); s["in_scope"] != true || s["signed"] != true || s["sbom"] != true {
		t.Errorf("signed: %+v", s)
	}
	if s := sig("ghcr.io/example/plain:1"); s["in_scope"] != true || s["signed"] != false {
		t.Errorf("plain: %+v", s)
	}
	if b, _ := e.Find("image", "ghcr.io/example/broken:1"); sig("ghcr.io/example/broken:1") != nil || b.Missed["signing"].Reason == "" || b.Missed["signing"].Source != "test" {
		t.Errorf("a check that could not run is a miss with why, never a fact: %+v", b)
	}
	if s := sig("quay.io/other/thing:2"); s == nil || s["in_scope"] != false {
		t.Errorf("out of scope: %+v", s)
	}
}

func TestPredicateType(t *testing.T) {
	statement := `{"_type":"https://in-toto.io/Statement/v0.1","predicateType":"https://spdx.dev/Document","subject":[]}`
	envelope := `{"payloadType":"application/vnd.in-toto+json","payload":"` + base64.StdEncoding.EncodeToString([]byte(statement)) + `","signatures":[]}`
	if got := predicateType([]byte(envelope)); got != "https://spdx.dev/Document" {
		t.Errorf("envelope: %q", got)
	}
	if got := predicateType([]byte(statement)); got != "https://spdx.dev/Document" {
		t.Errorf("bare statement: %q", got)
	}
	if !isSBOM("https://cyclonedx.org/bom") || !isSBOM("https://spdx.dev/Document") || isSBOM("https://slsa.dev/provenance/v1") {
		t.Error("isSBOM")
	}
}

// TestLive verifies a public image signed in GitHub Actions. It runs only
// when TRELLIS_LIVE_IMAGE names one, with TRELLIS_LIVE_IDENTITY its
// subject pattern, because it reaches the internet.
func TestLive(t *testing.T) {
	ref := os.Getenv("TRELLIS_LIVE_IMAGE")
	if ref == "" {
		t.Skip("TRELLIS_LIVE_IMAGE not set")
	}
	v, err := NewCosign(Identity{Issuer: "https://token.actions.githubusercontent.com", SubjectRegExp: os.Getenv("TRELLIS_LIVE_IDENTITY")})
	if err != nil {
		t.Fatal(err)
	}
	sig := v.Verify(context.Background(), ref)
	t.Logf("%s: %+v", ref, sig)
	if sig.Error != "" {
		t.Fatal(sig.Error)
	}
	if want := os.Getenv("TRELLIS_LIVE_SIGNED") == "true"; sig.Signed != want {
		t.Errorf("signed %v, want %v", sig.Signed, want)
	}
}
