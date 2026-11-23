package azurepolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/pack"
)

// State is one row of Azure Policy's compliance: one assignment's verdict
// of one policy on one resource. Ids are lower case.
type State struct {
	Resource string `json:"resource"`
	// State is Azure's verdict: Compliant, NonCompliant, Exempt, or one that
	// says it could not judge, such as Unknown, Conflict, or Error.
	State string `json:"state"`
	// Under is the verdict under an exemption: Compliant when the resource
	// meets the policy anyway.
	Under      string   `json:"under"`
	Assignment string   `json:"assignment"`
	Definition string   `json:"definition"`
	Name       string   `json:"name"`
	Reference  string   `json:"reference"`
	Set        string   `json:"set"`
	SetName    string   `json:"setName"`
	Groups     []string `json:"groups"`
	// ManagementGroups are the management groups above the resource, by
	// name, comma separated.
	ManagementGroups string `json:"managementGroups"`
	Location         string `json:"location"`
	ResourceType     string `json:"resourceType"`
}

// Assignment is one policy or initiative assigned at a scope. Ids are
// lower case.
type Assignment struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Definition string `json:"definition"`
}

// Exemption is an exception made in Azure to one assignment, at a scope its
// id names, for every member of the assignment or the ones it lists.
type Exemption struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Assignment  string         `json:"assignment"`
	References  []string       `json:"references"`
	Category    string         `json:"category"`
	ExpiresOn   string         `json:"expiresOn"`
	Metadata    map[string]any `json:"metadata"`
	Selectors   []Selector     `json:"selectors"`
}

// Selector narrows an exemption to resources by their properties. A
// resource is covered when every condition of one selector holds.
type Selector struct {
	Name      string      `json:"name"`
	Selectors []Condition `json:"selectors"`
}

// Condition is one property test of a selector.
type Condition struct {
	Kind  string   `json:"kind"`
	In    []string `json:"in"`
	NotIn []string `json:"notIn"`
}

// Definition is one policy definition as the management API answers.
type Definition struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Properties struct {
		DisplayName string         `json:"displayName"`
		Description string         `json:"description"`
		Version     string         `json:"version"`
		Metadata    map[string]any `json:"metadata"`
	} `json:"properties"`
}

// Set is one initiative (a policy set definition) as the management API
// answers: its member policies and the compliance groups each is in.
type Set struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Properties struct {
		DisplayName       string `json:"displayName"`
		PolicyDefinitions []struct {
			PolicyDefinitionID          string   `json:"policyDefinitionId"`
			PolicyDefinitionReferenceID string   `json:"policyDefinitionReferenceId"`
			GroupNames                  []string `json:"groupNames"`
		} `json:"policyDefinitions"`
	} `json:"properties"`
}

// Read is one pass's reading of Azure Policy. Definitions and Sets are by
// lower-case id; a definition the read could not find is absent.
type Read struct {
	States      []State
	Assignments []Assignment
	Exemptions  []Exemption
	Definitions map[string]Definition
	Sets        map[string]Set
}

// Level is a rule's severity and tier, either of which may be unset.
type Level struct {
	Severity estate.Severity `yaml:"severity"`
	Tier     string          `yaml:"tier"`
}

// Tuning is the estate's severity and tier for Azure's rules, which Azure
// does not give: a default, laid over by a policy category, laid over by a
// definition name.
type Tuning struct {
	Default     Level            `yaml:"default"`
	Categories  map[string]Level `yaml:"categories"`
	Definitions map[string]Level `yaml:"definitions"`
}

// Settings say how Azure's rules become a pack. Every name is the estate's.
type Settings struct {
	// Source is the source's name, recorded as where the pack came from.
	Source string
	// Pack, Title, and Family are the pack's.
	Pack, Title, Family string
	// Kind is the kind of subject a resource is, and Fact the fact each
	// resource's verdicts are written as.
	Kind, Fact string
	// Standard and Pillar are what every rule enforces and serves.
	Standard, Pillar string
	// Tiers are the pack's tiers, lowest first; none when empty.
	Tiers  []string
	Tuning Tuning
	// Owner and Control are the exemption metadata keys that name an
	// exemption's owner and, for a mitigated one, its control.
	Owner, Control string
	// Frameworks name, by initiative name, the framework its compliance
	// groups are controls of; the initiative's name when unnamed.
	Frameworks map[string]string
}

// Verdict is Azure's verdict of one policy on one resource, over every
// assignment that judged it: the worst of them.
type Verdict struct {
	State string `json:"state"`
	// Assignments are the assignments whose state decided it, by name.
	Assignments []string `json:"assignments"`
	// Exemptions are the exemptions that cover it, by name, when Azure
	// exempts it; Uncovered the assignments that exempt it with no
	// exemption trellis read.
	Exemptions []string `json:"exemptions"`
	Uncovered  []string `json:"uncovered"`
}

// Azure's verdicts, as Resource Graph writes them.
const (
	Compliant    = "Compliant"
	NonCompliant = "NonCompliant"
	Exempt       = "Exempt"
)

// Shaped is a reading made into trellis's terms: a pack with one rule per
// policy definition, each resource's verdicts by rule, and each exemption
// that covers a whole failure as a waiver.
type Shaped struct {
	Pack     *pack.Pack
	Verdicts map[string]map[string]Verdict
	Waivers  []estate.Waiver
	// Skipped says what was left out, and why.
	Skipped []string
}

// RuleID is a policy definition's rule id: azure. and its name, lower
// case, with anything a rule id does not take as "-". It follows the
// definition, not an assignment, so moving an assignment keeps it.
func RuleID(definition string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(definition) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return "azure." + strings.TrimLeft(b.String(), "-")
}

// rule is what Shape knows of one definition before it is a rule.
type rule struct {
	id, name, title, description, category, version string
	controls                                        map[string]bool
}

// Shape makes a reading into a pack, verdicts, and waivers. Every policy
// an assignment names, an initiative flattened into its members, and every
// policy a state names is a rule.
func Shape(r Read, s Settings) (Shaped, error) {
	out := Shaped{Verdicts: map[string]map[string]Verdict{}}
	names := map[string]string{}
	for _, a := range r.Assignments {
		names[a.ID] = a.Name
	}
	defs := map[string]*rule{}
	add := func(id, name string) *rule {
		if d, ok := defs[id]; ok {
			return d
		}
		d := &rule{name: name, controls: map[string]bool{}}
		if def, ok := r.Definitions[id]; ok {
			d.name, d.title, d.description, d.version = def.Name, def.Properties.DisplayName, def.Properties.Description, def.Properties.Version
			d.category, _ = def.Properties.Metadata["category"].(string)
		}
		if d.name == "" {
			d.name = id[strings.LastIndex(id, "/")+1:]
		}
		defs[id] = d
		return d
	}
	framework := func(setName string) string {
		if f := s.Frameworks[setName]; f != "" {
			return f
		}
		return strings.ToLower(setName)
	}
	control := func(d *rule, setName, group string) {
		if group != "" && setName != "" {
			d.controls[framework(setName)+":"+strings.ReplaceAll(group, " ", "_")] = true
		}
	}
	for _, a := range r.Assignments {
		if !strings.Contains(a.Definition, "/policysetdefinitions/") {
			add(a.Definition, "")
			continue
		}
		set, ok := r.Sets[a.Definition]
		if !ok {
			out.Skipped = append(out.Skipped, fmt.Sprintf("initiative %s of assignment %s could not be read; only its members Azure judged are rules", a.Definition, a.ID))
			continue
		}
		for _, m := range set.Properties.PolicyDefinitions {
			d := add(strings.ToLower(m.PolicyDefinitionID), "")
			for _, g := range m.GroupNames {
				control(d, set.Name, g)
			}
		}
	}
	for _, st := range r.States {
		d := add(st.Definition, st.Name)
		for _, g := range st.Groups {
			control(d, st.SetName, g)
		}
	}

	ids := make([]string, 0, len(defs))
	for id := range defs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	ruleOf := map[string]string{}
	taken := map[string]string{}
	p := &pack.Pack{
		ID: s.Pack, Title: s.Title, Family: s.Family, Tiers: s.Tiers,
		About:  "Azure Policy's rules, verdicts, and exemptions, passed through",
		Source: "source " + s.Source,
	}
	digest := sha256.New()
	for _, id := range ids {
		d := defs[id]
		d.id = RuleID(d.name)
		if other, dup := taken[d.id]; dup {
			out.Skipped = append(out.Skipped, fmt.Sprintf("definition %s makes rule %s, which %s already has", id, d.id, other))
			continue
		}
		taken[d.id] = id
		ruleOf[id] = d.id
		rl := s.rule(d)
		p.Rules = append(p.Rules, rl)
		fmt.Fprintf(digest, "%s|%s|%s|%s|%s|%v|%s\n", rl.ID, rl.Title, rl.Severity, rl.Tier, d.version, rl.Controls, rl.Fix)
	}
	if len(p.Rules) == 0 {
		return out, fmt.Errorf("azure policy holds no rule: no assignment and no compliance state was read")
	}
	p.Version = hex.EncodeToString(digest.Sum(nil))[:12]
	if err := p.Compile(); err != nil {
		return out, err
	}
	out.Pack = p

	type pair struct{ resource, rule string }
	states := map[pair][]State{}
	for _, st := range r.States {
		id, ok := ruleOf[st.Definition]
		if !ok || st.Resource == "" {
			continue
		}
		k := pair{st.Resource, id}
		states[k] = append(states[k], st)
	}
	keys := make([]pair, 0, len(states))
	for k := range states {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].resource != keys[j].resource {
			return keys[i].resource < keys[j].resource
		}
		return keys[i].rule < keys[j].rule
	})
	for _, k := range keys {
		v, covering := verdict(states[k], names, r.Exemptions)
		if out.Verdicts[k.resource] == nil {
			out.Verdicts[k.resource] = map[string]Verdict{}
		}
		out.Verdicts[k.resource][k.rule] = v
		if v.State == Exempt && len(v.Uncovered) == 0 {
			w := s.waiver(covering)
			w.Rule, w.Subject = k.rule, k.resource
			out.Waivers = append(out.Waivers, w)
		}
	}
	return out, nil
}

// rule makes one definition a rule whose check reads Azure's verdict and
// nothing else.
func (s Settings) rule(d *rule) *pack.Rule {
	lvl := s.Tuning.Default
	for _, over := range []Level{s.Tuning.Categories[d.category], s.Tuning.Definitions[d.name]} {
		if over.Severity != "" {
			lvl.Severity = over.Severity
		}
		if over.Tier != "" {
			lvl.Tier = over.Tier
		}
	}
	if len(s.Tiers) == 0 {
		lvl.Tier = ""
	}
	title := d.title
	if title == "" {
		title = d.name
	}
	fix := "Meet the Azure policy, or exempt it in Azure with a reason, an owner, and an end."
	if desc := strings.TrimSpace(d.description); desc != "" {
		fix = strings.TrimRight(desc, ".") + ". Meet it, or exempt it in Azure with a reason, an owner, and an end."
	}
	controls := make([]string, 0, len(d.controls))
	for c := range d.controls {
		controls = append(controls, c)
	}
	sort.Strings(controls)
	v := s.Fact + "[" + strconv.Quote(d.id) + "]"
	return &pack.Rule{
		ID: d.id, Title: title, Kind: s.Kind, Severity: lvl.Severity, Tier: lvl.Tier,
		Standard: s.Standard, Pillar: s.Pillar, Controls: controls,
		Stages: []pack.Stage{pack.Board},
		Needs:  map[string]string{s.Fact: "Azure Policy was not read for it"},
		Skip:   []*pack.Condition{{If: "!(" + strconv.Quote(d.id) + " in " + s.Fact + ")", Because: "No assignment of this policy judges it"}},
		Unknown: []*pack.Condition{{
			If:      fmt.Sprintf("!(%s.state in [%q, %q, %q])", v, Compliant, NonCompliant, Exempt),
			Explain: fmt.Sprintf(`"Azure Policy could not judge it: " + %s.state`, v),
		}},
		Check:  fmt.Sprintf("%s.state == %q", v, Compliant),
		Detail: fmt.Sprintf(`%[1]s.state == %[2]q ? (size(%[1]s.uncovered) == 0 ? "Azure Policy exempts it: " + %[1]s.exemptions.join("; ") : "Azure Policy exempts it under " + %[1]s.uncovered.join(", ") + ", but trellis read no exemption that covers it") : "Azure Policy finds it non-compliant under " + %[1]s.assignments.join(", ")`, v, Exempt),
		Evidence: fmt.Sprintf(`[{"source": "Azure Resource Graph", "query": %s + subject.key + %s, "got": %s.state}]`,
			strconv.Quote("policyresources | where type =~ 'microsoft.policyinsights/policystates' | where tolower(tostring(properties.resourceId)) == '"),
			strconv.Quote("' and properties.policyDefinitionName =~ '"+strings.ReplaceAll(d.name, "'", "")+"'"), v),
		Fix: fix,
	}
}

// rank orders Azure's verdicts, worst first: a failure, then a failure
// Azure exempts, then one it could not judge, then a pass.
func rank(st State) int {
	switch {
	case st.State == NonCompliant:
		return 0
	case st.State == Exempt && st.Under != Compliant:
		return 1
	case st.State == Compliant, st.State == Exempt:
		return 3
	}
	return 2
}

// verdict is the worst of one policy's states on one resource, and when
// Azure exempts it, the exemptions that cover every exempt state.
func verdict(states []State, names map[string]string, exemptions []Exemption) (Verdict, []Exemption) {
	worst := 3
	for _, st := range states {
		worst = min(worst, rank(st))
	}
	v := Verdict{Assignments: []string{}, Exemptions: []string{}, Uncovered: []string{}}
	var covering []Exemption
	seen := map[string]bool{}
	for _, st := range states {
		if rank(st) != worst {
			continue
		}
		name := names[st.Assignment]
		if name == "" {
			name = st.Assignment[strings.LastIndex(st.Assignment, "/")+1:]
		}
		v.Assignments = appendNew(v.Assignments, name)
		switch worst {
		case 0:
			v.State = NonCompliant
		case 1:
			v.State = Exempt
			found := false
			for _, e := range exemptions {
				if covers(e, st) {
					found = true
					if !seen[e.ID] {
						seen[e.ID] = true
						covering = append(covering, e)
						v.Exemptions = appendNew(v.Exemptions, e.Name)
					}
				}
			}
			if !found {
				v.Uncovered = appendNew(v.Uncovered, name)
			}
		case 2:
			v.State = st.State
		default:
			v.State = Compliant
		}
	}
	sort.Strings(v.Assignments)
	sort.Strings(v.Exemptions)
	sort.Strings(v.Uncovered)
	return v, covering
}

func appendNew(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// exemptionPath is where an exemption's own name begins in its id; what
// comes before is the scope it was made at.
const exemptionPath = "/providers/microsoft.authorization/policyexemptions/"

// covers says whether the exemption is the one behind an exempt state: it
// is made on the state's assignment, at a scope above the resource, for
// the state's initiative member or every member, and its selectors, if
// any, hold for the resource. A state never names its exemption.
func covers(e Exemption, st State) bool {
	if !strings.EqualFold(e.Assignment, st.Assignment) {
		return false
	}
	id := strings.ToLower(e.ID)
	at := strings.LastIndex(id, exemptionPath)
	if at < 0 {
		return false
	}
	scope := id[:at]
	const mg = "/providers/microsoft.management/managementgroups/"
	if strings.HasPrefix(scope, mg) {
		name := strings.TrimPrefix(scope, mg)
		if !contains(strings.Split(st.ManagementGroups, ","), name) {
			return false
		}
	} else if st.Resource != scope && !strings.HasPrefix(st.Resource, scope+"/") {
		return false
	}
	if len(e.References) > 0 {
		found := false
		for _, ref := range e.References {
			if strings.EqualFold(ref, st.Reference) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	if len(e.Selectors) == 0 {
		return true
	}
	for _, sel := range e.Selectors {
		if selects(sel, st) {
			return true
		}
	}
	return false
}

// selects says whether every condition of a selector holds for the
// state's resource. A condition on a property a state does not carry
// holds, since Azure, which can read it, already chose to exempt.
func selects(sel Selector, st State) bool {
	for _, c := range sel.Selectors {
		var got string
		switch strings.ToLower(c.Kind) {
		case "resourcelocation":
			got = st.Location
		case "resourcetype":
			got = st.ResourceType
		default:
			continue
		}
		if len(c.In) > 0 && !containsFold(c.In, got) {
			return false
		}
		if len(c.NotIn) > 0 && containsFold(c.NotIn, got) {
			return false
		}
	}
	return true
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if strings.TrimSpace(x) == s {
			return true
		}
	}
	return false
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// waiver is one or more exemptions that together cover a failure, as one
// waiver. Several count only if each does: the owner, control, and end
// are kept only when every part has one, and the end is the earliest.
func (s Settings) waiver(parts []Exemption) estate.Waiver {
	var w estate.Waiver
	var reasons, owners, controls, ids []string
	accepted, other := false, ""
	missingOwner, missingControl, missingEnd := false, false, false
	var end time.Time
	for _, e := range parts {
		reason := strings.TrimSpace(e.Description)
		if reason == "" {
			reason = strings.TrimSpace(e.Name)
		}
		reasons = appendNew(reasons, reason)
		ids = append(ids, e.ID)
		owner, _ := e.Metadata[s.Owner].(string)
		if s.Owner == "" || strings.TrimSpace(owner) == "" {
			missingOwner = true
		} else {
			owners = appendNew(owners, strings.TrimSpace(owner))
		}
		control, _ := e.Metadata[s.Control].(string)
		if s.Control == "" || strings.TrimSpace(control) == "" {
			missingControl = true
		} else {
			controls = appendNew(controls, strings.TrimSpace(control))
		}
		switch e.Category {
		case "Mitigated":
		case "Waiver":
			accepted = true
		default:
			other = e.Category
		}
		t, err := time.Parse(time.RFC3339Nano, e.ExpiresOn)
		if err != nil {
			missingEnd = true
		} else if end.IsZero() || t.Before(end) {
			end = t
		}
	}
	// Azure's Waiver is a risk accepted until it expires; its Mitigated is
	// a control standing in for the rule until a review. Any other category
	// is kept as Azure wrote it, so the waiver does not count and says why.
	switch {
	case other != "":
		w.Category = estate.Category(other)
	case accepted:
		w.Category = estate.Accepted
	default:
		w.Category = estate.Mitigated
	}
	w.Reason = strings.Join(reasons, "; ")
	w.ID = strings.Join(ids, ", ")
	if !missingOwner {
		w.Owner = strings.Join(owners, ", ")
	}
	if !missingControl {
		w.Control = strings.Join(controls, ", ")
	}
	if !missingEnd {
		if w.Category == estate.Mitigated {
			w.ReviewAt = end.UTC()
		} else {
			w.ExpiresAt = end.UTC()
		}
	}
	return w
}
