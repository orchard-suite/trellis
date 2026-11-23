package azurepolicy

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/trellis/internal/azure"
	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/pack"
	"github.com/orchard-suite/trellis/internal/sources"
)

// fixture is testdata/azure.json: what Resource Graph and the management
// API answer for a fictional estate.
type fixture struct {
	States      []map[string]any           `json:"states"`
	Assignments []map[string]any           `json:"assignments"`
	Exemptions  []map[string]any           `json:"exemptions"`
	Definitions map[string]json.RawMessage `json:"definitions"`
	Sets        map[string]json.RawMessage `json:"sets"`
}

func load(t *testing.T) fixture {
	t.Helper()
	data, err := os.ReadFile("testdata/azure.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// azureServer answers as Azure would for the fixture: Resource Graph by
// the table a query reads, states over two pages, and definitions and
// initiatives by id, any other id not found. It counts each id read.
func azureServer(t *testing.T, f fixture, gets map[string]int, scopes *[]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body struct {
				Query            string         `json:"query"`
				Options          map[string]any `json:"options"`
				ManagementGroups []any          `json:"managementGroups"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			*scopes = append(*scopes, body.ManagementGroups)
			var rows []map[string]any
			skip := ""
			switch {
			case strings.Contains(body.Query, "policystates"):
				rows = f.States[:3]
				skip = "page2"
				if body.Options["$skipToken"] == "page2" {
					rows, skip = f.States[3:], ""
				}
			case strings.Contains(body.Query, "policyassignments"):
				rows = f.Assignments
			case strings.Contains(body.Query, "policyexemptions"):
				rows = f.Exemptions
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": rows, "$skipToken": skip})
			return
		}
		id := strings.ToLower(r.URL.Path)
		gets[id]++
		if body, ok := f.Definitions[id]; ok {
			_, _ = w.Write(body)
			return
		}
		if body, ok := f.Sets[id]; ok {
			_, _ = w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
}

func settings(t *testing.T) Settings {
	t.Helper()
	var c Config
	err := yaml.Unmarshal([]byte(`
management_groups: [mg-example]
tiers: [baseline, hardened]
tuning:
  default: { severity: medium, tier: baseline }
  categories:
    Storage: { severity: high }
  definitions:
    22222222-2222-2222-2222-222222222222: { severity: critical, tier: hardened }
exemptions: { owner: owner, control: control }
frameworks: { estate-baseline: example-framework }
`), &c)
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Settings("azure_policy")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const (
	sub1      = "/subscriptions/00000000-0000-0000-0000-000000000001/resourcegroups/"
	publicDoc = sub1 + "rg-web/providers/microsoft.storage/storageaccounts/stpublicdocs"
	logs      = sub1 + "rg-logs/providers/microsoft.storage/storageaccounts/stlogs"
	mixed     = sub1 + "rg-web/providers/microsoft.storage/storageaccounts/stmixed"
	vault     = sub1 + "rg-app/providers/microsoft.keyvault/vaults/kv-app"
	uncovered = sub1 + "rg-web/providers/microsoft.storage/storageaccounts/stuncovered"
	sqlData   = "/subscriptions/00000000-0000-0000-0000-000000000002/resourcegroups/rg-data/providers/microsoft.sql/servers/sql-data"
	unjudged  = sub1 + "rg-web/providers/microsoft.web/sites/app-quiet"

	ownerTag = "azure.require-owner-tag"
	tls      = "azure.11111111-1111-1111-1111-111111111111"
	private  = "azure.22222222-2222-2222-2222-222222222222"
	purge    = "azure.33333333-3333-3333-3333-333333333333"
	oldRule  = "azure.old-rule"
)

// TestAzurePassesThrough reads the fixture as the board would and judges
// it: every verdict is Azure's, and every exemption is weighed as a waiver.
func TestAzurePassesThrough(t *testing.T) {
	f := load(t)
	gets := map[string]int{}
	var scopes []any
	srv := azureServer(t, f, gets, &scopes)
	defer srv.Close()
	api := azure.NewManagement(srv.URL, func(context.Context) (string, error) { return "tok", nil }, srv.Client())
	src := New(api, azure.Scope{ManagementGroups: []string{"mg-example"}}, settings(t), slog.New(slog.NewTextHandler(io.Discard, nil)))

	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	e := estate.New(now)
	// A resource the catalog brought that no policy judges.
	e.Writer("orrery").Subject("azure_resource", unjudged, "app-quiet")
	if err := src.Collect(context.Background(), e.Writer("azure_policy")); err != nil {
		t.Fatal(err)
	}
	for _, s := range scopes {
		if mgs, _ := s.([]any); len(mgs) != 1 || mgs[0] != "mg-example" {
			t.Errorf("a query was not scoped to the management group: %v", s)
		}
	}

	packs := src.Packs()
	if len(packs) != 1 {
		t.Fatalf("packs: %d", len(packs))
	}
	p := packs[0]
	rules := map[string]*pack.Rule{}
	for _, r := range p.Rules {
		rules[r.ID] = r
	}
	if len(rules) != 5 || rules[ownerTag] == nil || rules[tls] == nil || rules[private] == nil || rules[purge] == nil || rules[oldRule] == nil {
		t.Fatalf("one rule per definition, the initiative flattened, the deleted one by its name: %v", keys(rules))
	}
	if r := rules[private]; r.Title != "Storage accounts deny public network access" || r.Severity != estate.Critical || r.Tier != "hardened" {
		t.Errorf("a definition's own tuning wins: %+v", r)
	}
	if r := rules[tls]; r.Severity != estate.High || r.Tier != "baseline" || len(r.Controls) != 1 || r.Controls[0] != "example-framework:Example_Control_1" {
		t.Errorf("a category's tuning, and the initiative's group as a control: %+v", r)
	}
	if r := rules[purge]; len(r.Controls) != 1 || r.Controls[0] != "example-framework:Example_Control_2" || r.Severity != estate.Medium {
		t.Errorf("a member Azure never judged is still a rule, with its control: %+v", r)
	}
	if r := rules[oldRule]; r.Title != "Old_Rule" {
		t.Errorf("a definition Azure no longer has is titled by its name: %+v", r)
	}
	if p.ID != "azure-policy" || p.Family != "security" || p.Source != "source azure_policy" || len(p.Version) != 12 {
		t.Errorf("pack: %+v", p)
	}

	set, refused := (&pack.Set{}).With(packs)
	if len(refused) != 0 {
		t.Fatal(refused)
	}
	ev := judge.Evaluate(e, set, judge.Options{Stage: pack.Board, Params: judge.Merge(set)})
	state := func(rule, subject string) (judge.Finding, bool) {
		for _, f := range ev.Findings {
			if f.Rule == rule && f.Subject == subject {
				return f, true
			}
		}
		return judge.Finding{}, false
	}

	if f, ok := state(private, publicDoc); !ok || f.State != judge.Waived || f.Waiver == nil || f.Waiver.Reason != "Serves public marketing PDFs to anyone." || f.Waiver.Owner != "web-team" || f.Waiver.Category != estate.Accepted || !f.Waiver.ExpiresAt.Equal(time.Date(2027, 1, 31, 0, 0, 0, 0, time.UTC)) || !strings.Contains(f.Waiver.ID, "policyExemptions/public-docs") {
		t.Errorf("an exemption with a reason, an owner, and an end waives its finding: %+v %+v", f, f.Waiver)
	}
	if f, ok := state(ownerTag, publicDoc); !ok || f.State != judge.Open || f.Detail != "Azure Policy finds it non-compliant under Estate baseline" {
		t.Errorf("one assignment failing fails the rule, whatever another says: %+v", f)
	}
	if _, ok := state(tls, publicDoc); ok {
		t.Error("a compliant resource has no finding")
	}

	if f, ok := state(private, logs); !ok || f.State != judge.Open || f.Detail != "Azure Policy exempts it: Logs are open" {
		t.Errorf("an exemption with no owner and no end does not count; the finding stays open: %+v", f)
	}
	rejected := false
	for _, w := range ev.Waivers {
		if w.Subject == logs && w.Status == judge.WaiverRejected && w.Reason == "Logs are open" && w.From == "azure_policy" {
			rejected = true
		}
	}
	if !rejected {
		t.Errorf("the board shows the rejected exemption and why: %+v", ev.Waivers)
	}

	if f, ok := state(ownerTag, mixed); !ok || f.State != judge.Open || f.Waiver != nil {
		t.Errorf("an exemption on one assignment never hides another's failure: %+v", f)
	}
	if f, ok := state(ownerTag, sqlData); !ok || f.State != judge.Waived || f.Waiver.Kind() != estate.Mitigated || f.Waiver.Control == "" || !f.Waiver.ReviewAt.Equal(time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("a management group's mitigated exemption for one member waives it until its review: %+v %+v", f, f.Waiver)
	}
	if _, ok := state(ownerTag, vault); ok {
		t.Error("an exemption over a resource that meets the policy anyway leaves no finding")
	}
	if f, ok := state(oldRule, vault); !ok || f.State != judge.Open || f.Detail != "Azure Policy finds it non-compliant under retired" {
		t.Errorf("an assignment trellis did not read is named by its id: %+v", f)
	}
	if f, ok := state(private, uncovered); !ok || f.State != judge.Open || !strings.Contains(f.Detail, "but trellis read no exemption that covers it") {
		t.Errorf("an exemption whose selector leaves the resource out does not cover it: %+v", f)
	}

	for _, sv := range ev.Subjects {
		switch sv.Key {
		case logs:
			if len(sv.Unchecked) != 1 || sv.Unchecked[0].Rule != tls || sv.Unchecked[0].Reason != "Azure Policy could not judge it: Error" {
				t.Errorf("Azure's error is no data, never a pass: %+v", sv.Unchecked)
			}
		case unjudged:
			if !sv.Outside {
				t.Errorf("a resource no policy judges is outside every rule: %+v", sv)
			}
		}
	}

	// A second pass reads no definition again.
	before := len(gets)
	total := 0
	for _, n := range gets {
		total += n
	}
	if err := src.Collect(context.Background(), estate.New(now).Writer("azure_policy")); err != nil {
		t.Fatal(err)
	}
	after := 0
	for _, n := range gets {
		after += n
	}
	if after != total || len(gets) != before {
		t.Errorf("definitions are read once a day, not every pass: %d reads, then %d", total, after)
	}
}

func TestTheSourceBuildsFromItsSettings(t *testing.T) {
	t.Setenv("AZURE_TENANT_ID", "t")
	t.Setenv("AZURE_CLIENT_ID", "c")
	t.Setenv("AZURE_FEDERATED_TOKEN_FILE", "/dev/null")
	doc := `
type: azure_policy
name: policy
max_age: 48h
management_groups: [mg-example]
tiers: [baseline]
tuning: { default: { tier: baseline } }
exemptions: { owner: owner }
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(doc), &node); err != nil {
		t.Fatal(err)
	}
	n, err := sources.Build(node.Content[0], sources.Env{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	src := n.Source.(*Source)
	if src.settings.Source != "policy" || src.settings.Kind != "azure_resource" || src.settings.Fact != "azure_policy" || src.settings.Tuning.Default.Severity != estate.Medium || n.MaxAge != 48*time.Hour {
		t.Errorf("settings: %+v", src.settings)
	}
	if _, ok := n.Source.(sources.Brings); !ok {
		t.Error("the source does not bring its pack")
	}
	for name, bad := range map[string]string{
		"both scopes":      doc + "subscriptions: [s]\n",
		"no default tier":  strings.Replace(doc, "tuning: { default: { tier: baseline } }", "tuning: {}", 1),
		"unknown tier":     strings.Replace(doc, "tier: baseline }", "tier: gold }", 1),
		"bad severity":     strings.Replace(doc, "{ tier: baseline }", "{ tier: baseline, severity: dire }", 1),
		"bad kind":         doc + "kind: Azure-Thing\n",
		"unknown settings": doc + "colour: blue\n",
	} {
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(bad), &node); err != nil {
			t.Fatal(err)
		}
		if _, err := sources.Build(node.Content[0], sources.Env{}); err == nil {
			t.Errorf("%s: built", name)
		}
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
