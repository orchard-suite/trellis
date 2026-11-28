package orrery

import (
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
)

// The safe_paths include maps the hub's own safe paths into the fact shape
// the judge's catalog reads: the path, the thing it reaches, and the days.
func TestTheSafePathsMappingReadsTheHubsSafePaths(t *testing.T) {
	maps, err := ParseMappings(Included["safe_paths"])
	if err != nil {
		t.Fatal(err)
	}
	var compiledMaps []*compiled
	for _, m := range maps {
		c, err := Compile(m)
		if err != nil {
			t.Fatal(err)
		}
		compiledMaps = append(compiledMaps, c)
	}
	e := estate.New(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC))
	apply(t, e, compiledMaps, Records{"orrery_safe_path": {
		rec(t, "orrery_safe_path", "app/beacon/swap", "swap on Beacon", `{"path":"swap","subject_blueprint":"app","subject_key":"beacon","proven_at":"2026-11-03T00:00:00Z","proven_run":"r1","failed_at":null,"status":"green"}`, ""),
	}})
	got := fact(t, e, "safe_path", "app/beacon/swap", "safe_path")
	if got["path"] != "swap" || got["subject_blueprint"] != "app" || got["subject_key"] != "beacon" || got["proven_at"] != "2026-11-03T00:00:00Z" || got["failed_at"] != nil {
		t.Fatalf("safe path: %v", got)
	}
	if _, ok := Included["standard"]; !ok {
		t.Fatal("the standard mappings are included by name")
	}
}
