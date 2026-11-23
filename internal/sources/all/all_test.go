package all

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/config"
	"github.com/orchard-suite/trellis/internal/sources"
	"github.com/orchard-suite/trellis/internal/sources/orrery"
)

const sample = `
estate: acme
sources:
  - type: orrery
    url: https://orrery.example.test
    token: tok
    include: [standard, own.yaml]
    mappings:
      - { kind: storage_account, blueprint: azure_storage }
  - type: telemetry
    max_age: 6h
    declarations: decls
    prometheus: http://prometheus:9090
    loki: http://loki:3100
    tempo: http://tempo:3200
    window: 24h
    labels:
      edge_access: '{app="edge", site="{host}"}'
  - type: command
    name: scorecard
    run: [scorecard-facts, --all]
  - type: waivers
    file: waivers.yaml
`

func build(t *testing.T, doc string) ([]sources.Named, error) {
	t.Helper()
	dir := t.TempDir()
	own := "mappings:\n  - { kind: vm, blueprint: vmware_vm, fields: { os: entity.fields.guest_os } }\n"
	if err := os.WriteFile(filepath.Join(dir, "own.yaml"), []byte(own), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []sources.Named
	for i := range c.Sources {
		n, err := sources.Build(&c.Sources[i], sources.Env{Dir: c.Dir})
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func TestEverySourceBuildsFromItsSettings(t *testing.T) {
	got, err := build(t, sample)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("%d sources", len(got))
	}
	if got[0].Name != "orrery" || got[2].Name != "scorecard" || got[1].MaxAge != 6*time.Hour || got[0].MaxAge != sources.DefaultMaxAge {
		t.Errorf("names and ages: %+v", got)
	}
	o := got[0].Source.(*orrery.Source)
	facts := strings.Join(o.Facts(), ",")
	if !strings.Contains(facts, "storage_account") || !strings.Contains(facts, "vm") || !strings.Contains(facts, "repo") {
		t.Errorf("own mappings, the standard's, and the included file's: %s", facts)
	}
}

func TestASourceRefusesWhatItCannotRun(t *testing.T) {
	cases := map[string]string{
		"unknown type":    strings.Replace(sample, "type: command", "type: carrier-pigeon", 1),
		"unknown setting": strings.Replace(sample, "window: 24h", "window: 24h\n    colour: blue", 1),
		"missing include": strings.Replace(sample, "own.yaml]", "missing.yaml]", 1),
		"bad max age":     strings.Replace(sample, "max_age: 6h", "max_age: soon", 1),
	}
	for name, doc := range cases {
		if _, err := build(t, doc); err == nil {
			t.Errorf("%s: built", name)
		}
	}
}
