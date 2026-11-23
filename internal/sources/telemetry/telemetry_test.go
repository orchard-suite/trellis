package telemetry

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/sources/telemetry/declaration"
	"github.com/orchard-suite/trellis/internal/sources/telemetry/probe"
)

func TestShapeWritesEveryMeasurementWithItsEvidence(t *testing.T) {
	d := &declaration.Declaration{Service: "app", Title: "App", Runtime: declaration.Go, MaxLabelValues: 50}
	m := probe.Measures{Window: time.Hour}
	m.BuildInfo.Series = 1
	m.BuildInfo.Versions = []string{"1.2.3"}
	m.Logs.Lines, m.Logs.NotJSON = 9, 2
	m.Logs.Evidence = []probe.Evidence{{Source: "loki", Query: `{app="app"}`, Got: "9 lines"}}
	m.TraceIDs.Error = "could not read the store: timeout"
	e := estate.New(time.Now())
	if err := Shape(e.Writer("telemetry"), d, m, "sw_workload"); err != nil {
		t.Fatal(err)
	}
	s, ok := e.Find("service", "app")
	if !ok || s.Title != "App" || s.Ref == nil || s.Ref.Blueprint != "sw_workload" || s.Ref.Key != "app" {
		t.Fatalf("service: %+v", s)
	}
	v, _ := s.Value("telemetry")
	fact := v.(map[string]any)
	if fact["window"] != "1h0m0s" || fact["declaration"].(map[string]any)["runtime"] != "go" {
		t.Errorf("window and declaration: %+v", fact)
	}
	logs := fact["logs"].(map[string]any)
	if logs["not_json"] != 2.0 || logs["evidence"].([]any)[0].(map[string]any)["got"] != "9 lines" {
		t.Errorf("logs: %+v", logs)
	}
	if _, has := fact["event_loop"]; has {
		t.Error("a measurement not taken is absent")
	}
	if fact["trace_ids"].(map[string]any)["error"] == nil {
		t.Error("a store that could not be read is recorded on its measurement")
	}
}

func TestShapeMissesWhenNoStoreAnswered(t *testing.T) {
	d := &declaration.Declaration{Service: "app", Title: "App"}
	var m probe.Measures
	for _, r := range []*probe.Read{&m.BuildInfo.Read, &m.HTTPDuration.Read, &m.ProcessCPU.Read, &m.ProcessMemory.Read, &m.Logs.Read, &m.TraceIDs.Read, &m.Cardinality.Read} {
		r.Error = "could not read the store: refused"
	}
	e := estate.New(time.Now())
	if err := Shape(e.Writer("telemetry"), d, m, ""); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Find("service", "app")
	if _, has := s.Value("telemetry"); has {
		t.Error("no fact is written from nothing")
	}
	if miss, ok := s.Missed["telemetry"]; !ok || miss.Source != "telemetry" {
		t.Errorf("missed: %+v", s.Missed)
	}
}

func TestLoadSkipsWhatDoesNotParse(t *testing.T) {
	dir := t.TempDir()
	good := "service: app\nnamespace: apps\nhost: app.example.test\nruntime: go\nedge: traefik\npage: false\npublic: false\n"
	for name, body := range map[string]string{"app.yaml": good, "broken.yaml": "service: [", ".hidden.yaml": good, "notes.txt": "x"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	decls, skipped, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(decls) != 1 || decls[0].Service != "app" {
		t.Errorf("declarations: %+v", decls)
	}
	if _, ok := skipped["broken.yaml"]; !ok || len(skipped) != 1 {
		t.Errorf("skipped: %v", skipped)
	}
	if _, _, err := Load(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing directory is an error")
	}
}

func TestTheCatalogBlueprintIsLinked(t *testing.T) {
	if got := New("", probe.Sources{}, probe.Options{}, "sw_workload", nil).Linked(); len(got) != 1 || got[0] != "sw_workload" {
		t.Errorf("a finding on a declared service links to its catalog entity: %v", got)
	}
	if got := New("", probe.Sources{}, probe.Options{}, "", nil).Linked(); got != nil {
		t.Errorf("no catalog, no link: %v", got)
	}
}
