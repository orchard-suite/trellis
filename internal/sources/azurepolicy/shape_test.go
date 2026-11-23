package azurepolicy

import (
	"strings"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
)

func TestRuleIDFollowsTheDefinitionName(t *testing.T) {
	for in, want := range map[string]string{
		"11111111-1111-1111-1111-111111111111": "azure.11111111-1111-1111-1111-111111111111",
		"Deny_Public IP":                       "azure.deny-public-ip",
		"_lead":                                "azure.lead",
	} {
		if got := RuleID(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

const (
	asgA = "/subscriptions/s/providers/microsoft.authorization/policyassignments/a"
	asgB = "/subscriptions/s/providers/microsoft.authorization/policyassignments/b"
	def  = "/providers/microsoft.authorization/policydefinitions/d"
	res  = "/subscriptions/s/resourcegroups/rg/providers/microsoft.storage/storageaccounts/st"
)

func exempt(assignment string) State {
	return State{Resource: res, State: Exempt, Under: NonCompliant, Assignment: assignment, Definition: def, Name: "d", ManagementGroups: "root,mg"}
}

func exemption(name, assignment, scope, expires string, meta map[string]any) Exemption {
	return Exemption{ID: scope + "/providers/Microsoft.Authorization/policyExemptions/" + name, Name: name, Description: "because " + name, Assignment: assignment, Category: "Waiver", ExpiresOn: expires, Metadata: meta}
}

func shapeOf(t *testing.T, r Read) Shaped {
	t.Helper()
	s := Settings{Source: "azure_policy", Pack: "azure-policy", Title: "Azure Policy", Family: "security", Kind: "azure_resource", Fact: "azure_policy", Standard: "s", Pillar: "p", Tuning: Tuning{Default: Level{Severity: estate.Medium}}, Owner: "owner", Control: "control"}
	out, err := Shape(r, s)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestAWaiverNeedsEveryExemptStateCovered holds a failure Azure exempts
// under two assignments to an exemption for each: one alone waives
// nothing, and two make one waiver that counts only if both would.
func TestAWaiverNeedsEveryExemptStateCovered(t *testing.T) {
	owner := map[string]any{"owner": "team-a"}
	one := shapeOf(t, Read{
		States:     []State{exempt(asgA), exempt(asgB)},
		Exemptions: []Exemption{exemption("ea", asgA, "/subscriptions/s", "2027-01-01T00:00:00Z", owner)},
	})
	if len(one.Waivers) != 0 {
		t.Errorf("an exemption on one of two assignments waived the failure: %+v", one.Waivers)
	}
	v := one.Verdicts[res]["azure.d"]
	if v.State != Exempt || len(v.Uncovered) != 1 || v.Uncovered[0] != "b" {
		t.Errorf("the assignment with no exemption is named: %+v", v)
	}

	both := shapeOf(t, Read{
		States: []State{exempt(asgA), exempt(asgB)},
		Exemptions: []Exemption{
			exemption("ea", asgA, "/subscriptions/s", "2027-03-01T00:00:00Z", owner),
			exemption("eb", asgB, "/subscriptions/s/resourceGroups/rg", "2027-01-01T00:00:00Z", map[string]any{"owner": "team-b"}),
		},
	})
	if len(both.Waivers) != 1 {
		t.Fatalf("waivers: %+v", both.Waivers)
	}
	w := both.Waivers[0]
	if w.Owner != "team-a, team-b" || !w.ExpiresAt.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) || !strings.Contains(w.ID, "/ea") || !strings.Contains(w.ID, "/eb") || w.Problem() != "" {
		t.Errorf("two exemptions make one waiver ending with the earlier: %+v", w)
	}

	unowned := shapeOf(t, Read{
		States: []State{exempt(asgA), exempt(asgB)},
		Exemptions: []Exemption{
			exemption("ea", asgA, "/subscriptions/s", "2027-03-01T00:00:00Z", owner),
			exemption("eb", asgB, "/subscriptions/s", "", nil),
		},
	})
	if w := unowned.Waivers[0]; w.Owner != "" || !w.ExpiresAt.IsZero() || w.Problem() == "" {
		t.Errorf("a part with no owner and no end leaves the waiver without them: %+v", w)
	}
}

func TestAnExemptionCoversOnlyWhereItIsMade(t *testing.T) {
	st := exempt(asgA)
	st.Reference = "tags"
	st.Location = "centralus"
	cases := []struct {
		name string
		e    Exemption
		want bool
	}{
		{"its resource group", exemption("x", asgA, "/subscriptions/s/resourceGroups/RG", "", nil), true},
		{"another assignment", exemption("x", asgB, "/subscriptions/s", "", nil), false},
		{"a sibling group", exemption("x", asgA, "/subscriptions/s/resourceGroups/rg2", "", nil), false},
		{"a group whose name begins the same", exemption("x", asgA, "/subscriptions/s/resourceGroups/r", "", nil), false},
		{"its management group", exemption("x", asgA, "/providers/Microsoft.Management/managementGroups/MG", "", nil), true},
		{"another management group", exemption("x", asgA, "/providers/Microsoft.Management/managementGroups/other", "", nil), false},
	}
	for _, c := range cases {
		if got := covers(c.e, st); got != c.want {
			t.Errorf("%s: covers %v, want %v", c.name, got, c.want)
		}
	}
	member := exemption("x", asgA, "/subscriptions/s", "", nil)
	member.References = []string{"other"}
	if covers(member, st) {
		t.Error("an exemption for another initiative member covered it")
	}
	member.References = []string{"Tags"}
	if !covers(member, st) {
		t.Error("an exemption for its initiative member did not cover it")
	}
	sel := exemption("x", asgA, "/subscriptions/s", "", nil)
	sel.Selectors = []Selector{{Selectors: []Condition{{Kind: "resourceLocation", NotIn: []string{"CentralUS"}}}}}
	if covers(sel, st) {
		t.Error("a selector that leaves its location out covered it")
	}
	sel.Selectors = append(sel.Selectors, Selector{Selectors: []Condition{{Kind: "resourceLocation", In: []string{"centralus"}}, {Kind: "resourceWithoutLocation", In: []string{"true"}}}})
	if !covers(sel, st) {
		t.Error("one selector that holds did not cover it")
	}
}

func TestTwoDefinitionsWithOneRuleIDKeepTheFirst(t *testing.T) {
	out := shapeOf(t, Read{States: []State{
		{Resource: res, State: Compliant, Assignment: asgA, Definition: "/providers/x/policydefinitions/deny_ip", Name: "Deny_IP"},
		{Resource: res, State: NonCompliant, Assignment: asgA, Definition: "/providers/y/policydefinitions/deny-ip", Name: "deny-ip"},
	}})
	if len(out.Pack.Rules) != 1 || len(out.Skipped) != 1 || !strings.Contains(out.Skipped[0], "azure.deny-ip") {
		t.Errorf("rules %d, skipped %v", len(out.Pack.Rules), out.Skipped)
	}
	if v := out.Verdicts[res]["azure.deny-ip"]; v.State != Compliant {
		t.Errorf("the left-out definition's states joined the kept one: %+v", v)
	}
}

func TestAReadWithNoRuleFails(t *testing.T) {
	if _, err := Shape(Read{}, Settings{}); err == nil {
		t.Error("an empty reading made a pack")
	}
}

func TestAnUnknownCategoryDoesNotCount(t *testing.T) {
	out := shapeOf(t, Read{
		States: []State{exempt(asgA)},
		Exemptions: []Exemption{func() Exemption {
			e := exemption("x", asgA, "/subscriptions/s", "2027-01-01T00:00:00Z", map[string]any{"owner": "o"})
			e.Category = "Someday"
			return e
		}()},
	})
	if p := out.Waivers[0].Problem(); !strings.Contains(p, "Someday") {
		t.Errorf("an unknown category counted: %q", p)
	}
}
