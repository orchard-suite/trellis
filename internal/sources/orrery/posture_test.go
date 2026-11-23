package orrery

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
)

// A posture as the hub answers /v1/posture, cut to what the pack reads.
const postureJSON = `{
  "principals": [
    { "kind": "person", "name": "user@example.com", "admin": true },
    { "kind": "token", "name": "agent", "admin": true },
    { "kind": "token", "name": "reader", "admin": false }
  ],
  "items": [
    { "target": { "kind": "blueprint", "id": "vm" }, "title": "VM", "group": "Cloud", "open": true, "class": null, "coverage": null },
    { "target": { "kind": "field", "blueprint": "vm", "field": "name" }, "title": "VM: name", "open": true, "class": null, "coverage": "reviewed" },
    { "target": { "kind": "field", "blueprint": "vm", "field": "price_cents" }, "title": "VM: price", "open": true, "class": null, "coverage": "unreviewed" },
    { "target": { "kind": "field", "blueprint": "vm", "field": "cost_cents" }, "title": "VM: cost", "open": false, "class": "cost", "coverage": "classified" },
    { "target": { "kind": "blueprint", "id": "project" }, "title": "Project", "group": null, "open": false, "class": null, "coverage": null },
    { "target": { "kind": "field", "blueprint": "project", "field": "budget" }, "title": "Project: budget", "open": false, "class": null, "coverage": "ruled" }
  ],
  "findings": [{ "id": "nobody:field:project.budget" }, { "id": "repeats" }],
  "audiences": [{ "id": "cost", "title": "Cost", "members": ["user@example.com"], "strangers": ["gone@example.com"] }],
  "review": [
    { "blueprint": "vm", "title": "VM", "createdAt": "2026-10-01T00:00:00.000Z", "reviewedAt": "2026-10-02T00:00:00.000Z", "newFields": ["price_cents"] },
    { "blueprint": "project", "title": "Project", "createdAt": "2026-11-01T00:00:00.000Z", "reviewedAt": null, "newFields": ["budget"] }
  ]
}`

func TestShapePostureMakesASubjectPerBlueprintAudienceAndHub(t *testing.T) {
	var p Posture
	if err := json.Unmarshal([]byte(postureJSON), &p); err != nil {
		t.Fatal(err)
	}
	e := estate.New(time.Now())
	c := ShapePosture(e.Writer("orrery"), "hub.example.test", p)
	if c != (PostureCounts{Blueprints: 2, Audiences: 1}) {
		t.Fatalf("counts: %+v", c)
	}
	vm := fact(t, e, "access_blueprint", "hub.example.test:vm", "access")
	if !reflect.DeepEqual(vm["open_fields"], []any{"name", "price_cents"}) || vm["group"] != "Cloud" || vm["reviewed_at"] != "2026-10-02T00:00:00.000Z" {
		t.Fatalf("vm: %+v", vm)
	}
	project := fact(t, e, "access_blueprint", "hub.example.test:project", "access")
	if project["reviewed_at"] != nil || !reflect.DeepEqual(project["nobody"], []any{"budget"}) || !reflect.DeepEqual(project["open_fields"], []any{}) {
		t.Fatalf("project: %+v", project)
	}
	cost := fact(t, e, "access_audience", "hub.example.test:cost", "audience")
	if cost["members"] != float64(1) || !reflect.DeepEqual(cost["strangers"], []any{"gone@example.com"}) {
		t.Fatalf("cost: %+v", cost)
	}
	hub := fact(t, e, "access_hub", "hub.example.test", "posture")
	if !reflect.DeepEqual(hub["admin_tokens"], []any{"agent"}) {
		t.Fatalf("hub: %+v", hub)
	}
	if HubName("https://orrery-ingest.example.test/") != "orrery-ingest.example.test" {
		t.Fatal("hub name")
	}
}
