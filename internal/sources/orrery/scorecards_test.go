package orrery

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/orchard-suite/trellis"
	"github.com/orchard-suite/trellis/internal/board"
	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/meter"
	"github.com/orchard-suite/trellis/internal/pack"
)

const recoverable = `
pack: recoverable
title: Recoverable
about: Whether a machine or a database comes back after a loss.
version: 1.0.0
family: platform
tiers: [bronze, silver, in_contract]
rules:
  - { id: vm.backup, title: Virtual machine has no Azure Backup, aim: Virtual machine has Azure Backup, kind: vm, severity: high, standard: s, pillar: p, tier: bronze, check: vm.backup, detail: '"no backup"', fix: Turn on Azure Backup. }
  - { id: db.pitr, title: Database has no point-in-time restore, aim: Database has point-in-time restore, kind: database, severity: high, standard: s, pillar: p, tier: bronze, check: db.pitr, detail: '"no PITR"', fix: Turn it on. }
  - { id: vm.drill, title: Never restored in a drill, kind: vm, severity: low, standard: s, pillar: p, tier: in_contract, check: vm.drilled, detail: '"never drilled"', fix: Restore it in a drill. }
  - { id: vm.draft, title: Draft, kind: vm, severity: low, standard: s, pillar: p, tier: silver, status: draft, check: "false", detail: '"draft"', fix: f }
  - { id: vm.soon, title: Soon, kind: vm, severity: low, standard: s, pillar: p, tier: silver, from: 2030-01-01T00:00:00Z, check: "false", detail: '"soon"', fix: f }
  - { id: app.thing, title: App thing, kind: app, severity: low, standard: s, pillar: p, tier: bronze, check: "true", detail: '"x"', fix: f }
`

func adoptedOf(t *testing.T, src string) Adopted {
	t.Helper()
	p, err := pack.Parse([]byte(src), "test")
	if err != nil {
		t.Fatal(err)
	}
	var tiered []*pack.Rule
	for _, r := range p.Rules {
		if r.Tier != "" {
			tiered = append(tiered, r)
		}
	}
	return Adopted{ID: p.ID, Title: p.Title, About: p.About, Status: string(p.State), Tiers: p.Tiers, Tiered: tiered}
}

func recoverableEval(drilled string) judge.Evaluation {
	at := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	ref := func(bp, key string) *estate.Ref { return &estate.Ref{Blueprint: bp, Key: key} }
	return judge.Evaluation{At: at, Subjects: []judge.SubjectVerdict{
		{Kind: "vm", Key: "vm1", Ref: ref("it_virtual_machine", "vm1"), Tiers: map[string]string{"recoverable": drilled}},
		{Kind: "database", Key: "db1", Ref: ref("it_virtual_machine", "db1"), Tiers: map[string]string{"recoverable": "bronze"}},
		{Kind: "app", Key: "a", Tiers: map[string]string{"recoverable": "bronze"}},
		{Kind: "vm", Key: "draft", Ref: ref("it_virtual_machine", "draft"), Tiers: map[string]string{"trial": ""}},
	}}
}

func TestScorecardBodyIsThePacksRules(t *testing.T) {
	cards := &Scorecards{Field: "trellis_{pack}_checks"}
	trial := adoptedOf(t, strings.Replace(recoverable, "pack: recoverable", "pack: trial\nstatus: draft", 1))
	unjudged := adoptedOf(t, strings.Replace(recoverable, "pack: recoverable", "pack: unjudged", 1))
	got := BuildScorecards(cards, []Adopted{adoptedOf(t, recoverable), trial, unjudged}, recoverableEval("bronze"))
	body, _ := json.MarshalIndent(got, "", "  ")
	want := `{
  "trellis_recoverable": {
    "title": "Recoverable",
    "description": "Whether a machine or a database comes back after a loss. Judged by trellis's Recoverable pack.",
    "levels": [
      {
        "name": "Bronze"
      },
      {
        "name": "Silver"
      },
      {
        "name": "In contract"
      }
    ],
    "blueprints": {
      "it_virtual_machine": {
        "where": {
          "field": "trellis_recoverable_checks",
          "op": "exists"
        },
        "rules": {
          "Bronze": [
            {
              "id": "vm_backup",
              "title": "Virtual machine has Azure Backup",
              "description": "Virtual machine has no Azure Backup",
              "fix": "Turn on Azure Backup.",
              "judged": {
                "field": "trellis_recoverable_checks",
                "check": "vm.backup"
              }
            },
            {
              "id": "db_pitr",
              "title": "Database has point-in-time restore",
              "description": "Database has no point-in-time restore",
              "fix": "Turn it on.",
              "judged": {
                "field": "trellis_recoverable_checks",
                "check": "db.pitr"
              }
            }
          ],
          "In contract": [
            {
              "id": "vm_drill",
              "title": "Never restored in a drill",
              "fix": "Restore it in a drill.",
              "judged": {
                "field": "trellis_recoverable_checks",
                "check": "vm.drill"
              }
            }
          ]
        }
      }
    }
  }
}`
	if string(body) != want {
		t.Errorf("scorecards:\n%s\nwant:\n%s", body, want)
	}
	if got := BuildScorecards(nil, []Adopted{adoptedOf(t, recoverable)}, recoverableEval("bronze")); len(got) != 0 {
		t.Errorf("no setting puts no scorecard: %v", got)
	}
	named := BuildScorecards(&Scorecards{Field: "f", ID: "std_{pack}", Packs: map[string]PackCard{"recoverable": {ID: "backups", Title: "Backups"}}}, []Adopted{adoptedOf(t, recoverable)}, recoverableEval("bronze"))
	if c, ok := named["backups"]; !ok || len(named) != 1 || c.Title != "Backups" {
		t.Errorf("a pack's own id and title win: %v", named)
	}
	if got := BuildScorecards(&Scorecards{Field: "f", Packs: map[string]PackCard{"other": {}}}, []Adopted{adoptedOf(t, recoverable)}, recoverableEval("bronze")); len(got) != 0 {
		t.Errorf("a pack the setting does not name gets no scorecard: %v", got)
	}
	if got := BuildScorecards(&Scorecards{Field: "f", ID: "std_{pack}"}, []Adopted{adoptedOf(t, strings.Replace(recoverable, "pack: recoverable", "pack: re-cover", 1))}, judge.Evaluation{Subjects: []judge.SubjectVerdict{{Kind: "vm", Ref: &estate.Ref{Blueprint: "b"}, Tiers: map[string]string{"re-cover": ""}}}}); got["std_re_cover"].Title == "" {
		t.Errorf("the default id fills {pack}, made a hub id: %v", got)
	}
}

func TestScorecardRuleIDs(t *testing.T) {
	for in, want := range map[string]string{"vm.backup": "vm_backup", "graft.lane-open": "graft_lane_open", "App.X": "app_x", "9.x": "r_9_x"} {
		if got := ScorecardRuleID(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
	for in, want := range map[string]string{"in_contract": "In contract", "bronze": "Bronze", "": ""} {
		if got := Level(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestScorecardsPutOnlyWhatMoved(t *testing.T) {
	status := http.StatusForbidden
	var puts []string
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		puts = append(puts, r.Method+" "+r.URL.Path)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"the source holds no scorecards grant"}`))
	}))
	defer hub.Close()
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	metrics, _ := meter.New()
	sink, err := NewSink(NewClient(hub.URL, "tok", log), "https://trellis.test", trellis.Blueprints, Write{}, &Scorecards{Field: "trellis_{pack}_checks"}, nil, metrics, log)
	if err != nil {
		t.Fatal(err)
	}
	p, err := pack.Parse([]byte(recoverable), "test")
	if err != nil {
		t.Fatal(err)
	}
	set := &pack.Set{Packs: []*pack.Pack{p}}
	pass := func(drilled string) *board.Snapshot {
		return &board.Snapshot{Evaluation: recoverableEval(drilled), Packs: set}
	}
	ctx := context.Background()
	sink.putScorecards(ctx, pass("bronze"))
	sink.putScorecards(ctx, pass("bronze"))
	if len(puts) != 2 || puts[0] != "PUT /v1/scorecards/trellis_recoverable" {
		t.Errorf("a refused put is tried again each pass: %v", puts)
	}
	if n := strings.Count(logs.String(), `"msg":"orrery refused the scorecard`); n != 1 {
		t.Errorf("the refusal is said once, not %d times:\n%s", n, logs.String())
	}
	status = http.StatusOK
	sink.putScorecards(ctx, pass("bronze"))
	sink.putScorecards(ctx, pass("silver"))
	if len(puts) != 3 {
		t.Errorf("a body put once is not put again: %v", puts)
	}
	p.Rules[0].Fix = "Turn on Azure Backup now."
	sink.putScorecards(ctx, pass("bronze"))
	if len(puts) != 4 {
		t.Errorf("a body that moved is put again: %v", puts)
	}
	if _, err := NewSink(nil, "", trellis.Blueprints, Write{}, &Scorecards{}, nil, metrics, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Error("scorecards with no field was taken")
	}
}
