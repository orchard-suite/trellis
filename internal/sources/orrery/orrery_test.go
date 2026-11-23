package orrery

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"time"

	"github.com/orchard-suite/trellis"
	"github.com/orchard-suite/trellis/internal/board"
	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/meter"
	"github.com/orchard-suite/trellis/internal/pack"
	"github.com/orchard-suite/trellis/internal/trend"
)

// rec is a hub record from fields and relations written as JSON.
func rec(t *testing.T, bp, key, title, fields, relations string) Record {
	t.Helper()
	r := Record{Blueprint: bp, Key: key, Title: title, Fields: map[string]json.RawMessage{}, Relations: map[string][]Ref{}}
	if fields != "" {
		if err := json.Unmarshal([]byte(fields), &r.Fields); err != nil {
			t.Fatal(err)
		}
	}
	if relations != "" {
		if err := json.Unmarshal([]byte(relations), &r.Relations); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func fact(t *testing.T, e *estate.Estate, kind, key, name string) map[string]any {
	t.Helper()
	s, ok := e.Find(kind, key)
	if !ok {
		t.Fatalf("no %s %s", kind, key)
	}
	v, _ := s.Value(name)
	f, _ := v.(map[string]any)
	return f
}

func has(s *estate.Subject, name string) bool {
	_, ok := s.Facts[name]
	return ok
}

func value(s *estate.Subject, name string) map[string]any {
	v, _ := s.Value(name)
	m, _ := v.(map[string]any)
	return m
}

func standard(t *testing.T) []*compiled {
	t.Helper()
	maps, err := ParseMappings(Standard)
	if err != nil {
		t.Fatal(err)
	}
	var out []*compiled
	for _, m := range maps {
		c, err := Compile(m)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

func apply(t *testing.T, e *estate.Estate, maps []*compiled, recs Records) {
	t.Helper()
	if err := Apply(context.Background(), e.Writer("orrery"), maps, recs, nil, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
}

// accessRecords are grants and applications the way the Entra pusher and
// the review action write them.
func accessRecords(t *testing.T) Records {
	t.Helper()
	grant := func(key, role, kind, holder string) Record {
		return rec(t, "entra_app_role_assignment", key, "Wiki: "+role, `{"role":"`+role+`","principal_type":"`+kind+`"}`,
			`{"app":[{"blueprint":"entra_app","key":"t/wiki","title":"Wiki"}],"`+kind+`":[{"blueprint":"entra_`+kind+`","key":"`+holder+`"}]}`)
	}
	return Records{
		"entra_app_role_assignment": {
			grant("t/a1", "Admin", "user", "t/u1"),
			grant("t/a2", "Reader", "user", "t/u2"),
			grant("t/a3", "Reader", "user", "t/u3"),
			grant("t/a4", "Reader", "group", "t/g1"),
			grant("t/a5", "Reader", "group", "t/g2"),
		},
		"entra_user": {
			rec(t, "entra_user", "t/u1", "user@example.com", `{"user_type":"member","enabled":true}`, ""),
			rec(t, "entra_user", "t/u2", "guest@example.test", `{"user_type":"guest","enabled":false}`, ""),
		},
		"entra_group": {rec(t, "entra_group", "t/g1", "wiki-readers", `{"kind":"security"}`, "")},
		"it_application": {
			rec(t, "it_application", "wiki", "Wiki", "", `{"sign_in":[{"blueprint":"entra_app","key":"t/wiki"},{"blueprint":"entra_app","key":"t/wiki-test"}]}`),
			rec(t, "it_application", "chat", "Chat", "", ""),
		},
		"it_access_review": {
			rec(t, "it_access_review", "r1", "Wiki, January", `{"reviewed_at":"2026-01-10T00:00:00Z"}`, `{"application":[{"blueprint":"it_application","key":"wiki"}]}`),
			rec(t, "it_access_review", "r2", "Wiki, June", `{"reviewed_at":"2026-06-01T09:00:00+02:00"}`, `{"application":[{"blueprint":"it_application","key":"wiki"}]}`),
			rec(t, "it_access_review", "r3", "Wiki, open", `{"reviewed_at":null}`, `{"application":[{"blueprint":"it_application","key":"wiki"}]}`),
		},
	}
}

// standardRecords are a catalog on the blueprint standard.
func standardRecords(t *testing.T) Records {
	t.Helper()
	return Records{
		"sw_repository": {
			rec(t, "sw_repository", "github/example/app", "app", `{"url":"https://github.com/example/app","organization":"example","name":"app","default_branch":"main","archived":false,"branch_protected":false,"has_ci":true,"dependabot_critical":1,"code_scanning_critical":null,"dependabot_high":2,"code_scanning_high":1}`, ""),
			rec(t, "sw_repository", "github/example/old", "old", `{"url":"https://github.com/example/old","archived":true}`, ""),
		},
		"sw_workload": {
			rec(t, "sw_workload", "api", "API", `{"repo":"https://github.com/example/app/tree/main/workloads/api","owner":"team-app","docs":null,"cluster_workloads":2,"vms":0}`, `{"program":[{"blueprint":"program","key":"platform"}]}`),
			rec(t, "sw_workload", "app", "App", `{"repo":"https://github.com/Example/app/"}`, ""),
		},
		"k8s_workload": {rec(t, "k8s_workload", "cluster-a/apps/web", "web", `{"kind":"Deployment","images":["ghcr.io/example/web:1.2.0","nginx:latest"]}`, "")},
		"sw_container_image": {
			rec(t, "sw_container_image", "ghcr.io/example/web:1.2.0", "1.2.0", `{"software_count":120,"cve_kev":0,"cve_critical":1,"cve_fixable":1,"epss_max":0.02,"namespaces":["apps"]}`, ""),
			rec(t, "sw_container_image", "ghcr.io/example/idle:0.1", "0.1", `{"software_count":10,"cve_kev":1,"namespaces":["idle"]}`, ""),
		},
		"it_workstation": {rec(t, "it_workstation", "osquery/AAA", "desk", `{"os":"Ubuntu","os_version":"24.04","last_seen":"2026-10-30T10:00:00Z","software_count":500,"cve_kev":0}`, "")},
		"sw_service": {
			rec(t, "sw_service", "github/example/app", "app", `{"origin":"built"}`, ""),
			rec(t, "sw_service", "github/upstream/db", "db", `{"origin":"adopted"}`, ""),
		},
		"sw_api": {
			rec(t, "sw_api", "github/example/app/v1", "App API", `{"kind":"rest","spec":"https://github.com/example/app/blob/HEAD/openapi.json"}`, `{"service":[{"blueprint":"sw_service","key":"github/example/app"}]}`),
			rec(t, "sw_api", "github/upstream/db/query", "DB", `{"kind":"rest"}`, `{"service":[{"blueprint":"sw_service","key":"github/upstream/db"}]}`),
			rec(t, "sw_api", "partner/pay", "Pay", `{"kind":"rest","external":true}`, ""),
		},
		"program": {rec(t, "program", "platform", "Platform", `{"description":"The platform.","services":0,"workloads":1}`, `{"team":[{"blueprint":"team","key":"ops","title":"Operations"}]}`)},
		"plat_waiver": {
			rec(t, "plat_waiver", "w1", "w1", `{"rule":"repo.no-ci","subject":"github/example/old","reason":"a mirror","expires_at":"2027-01-31T00:00:00Z"}`, `{"person":[{"blueprint":"person","key":"owner","title":"The owner"}]}`),
			rec(t, "plat_waiver", "w2", "w2", `{"rule":"repo.no-ci","subject":"github/example/app","reason":"no owner","expires_at":"2027-01-31T00:00:00Z"}`, ""),
		},
	}
}

// TestTheStandardMappingsReadAccess reads grants and applications the way
// the Entra pusher and the review action write them: each grant's holder
// and type through the user and group lists, and each application's
// grants and latest review through the relations that point at it.
func TestTheStandardMappingsReadAccess(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	recs := accessRecords(t)
	e := estate.New(now)
	apply(t, e, standard(t), recs)
	for key, want := range map[string]map[string]any{
		"t/a1": {"role": "Admin", "principal_type": "user", "app": "Wiki", "account": "t/u1", "holder": "active", "user_type": "member"},
		"t/a2": {"role": "Reader", "principal_type": "user", "app": "Wiki", "account": "t/u2", "holder": "disabled", "user_type": "guest"},
		"t/a3": {"role": "Reader", "principal_type": "user", "app": "Wiki", "account": "t/u3", "holder": "gone"},
		"t/a4": {"role": "Reader", "principal_type": "group", "app": "Wiki", "account": "t/g1", "holder": "active"},
		"t/a5": {"role": "Reader", "principal_type": "group", "app": "Wiki", "account": "t/g2", "holder": "gone"},
	} {
		if got := fact(t, e, "access_grant", key, "grant"); !reflect.DeepEqual(got, want) {
			t.Errorf("grant %s = %v, want %v", key, got, want)
		}
	}
	if s, _ := e.Find("access_grant", "t/a1"); s.Ref == nil || *s.Ref != (estate.Ref{Blueprint: "entra_app_role_assignment", Key: "t/a1"}) {
		t.Errorf("a grant's findings link to its assignment: %+v", s.Ref)
	}
	if got := fact(t, e, "application", "wiki", "application"); got["grants"] != 5.0 || got["reviewed_at"] != "2026-06-01T09:00:00+02:00" {
		t.Errorf("an application counts the grants of its sign-in apps and carries its latest review: %v", got)
	}
	if got := fact(t, e, "application", "chat", "application"); got["grants"] != 0.0 || got["reviewed_at"] != "" {
		t.Errorf("an application with no sign-in app and no review: %v", got)
	}

	// With the user, group, and review lists unread, what they give stays unknown.
	e = estate.New(now)
	apply(t, e, standard(t), Records{"entra_app_role_assignment": recs["entra_app_role_assignment"], "it_application": recs["it_application"]})
	if got := fact(t, e, "access_grant", "t/a1", "grant"); got["holder"] != nil || got["user_type"] != nil || got["role"] != "Admin" {
		t.Errorf("a grant whose holders were not read: %v", got)
	}
	if got := fact(t, e, "application", "wiki", "application"); got["grants"] != 5.0 || got["reviewed_at"] != nil {
		t.Errorf("an application whose reviews were not read: %v", got)
	}
}

func TestTheStandardMappingsReadTheBlueprintStandard(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	e := estate.New(now)
	recs := standardRecords(t)
	apply(t, e, standard(t), recs)
	if n := len(e.Of("repository")); n != 2 {
		t.Errorf("repositories: %d", n)
	}
	if n := len(e.Of("workload")); n != 1 {
		t.Errorf("workloads: %d", n)
	}
	if n := len(e.Of("image")); n != 2 {
		t.Errorf("images: %d", n)
	}
	if n := len(e.Of("host")); n != 1 {
		t.Errorf("hosts: %d", n)
	}
	if n := len(e.Of("api")); n != 1 {
		t.Errorf("apis: %d", n)
	}
	repo := fact(t, e, "repository", "github/example/app", "repo")
	if repo["alerts_critical"] != 1.0 || repo["alerts_high"] != 3.0 || repo["branch_protected"] != false || repo["owner"] != "example" {
		t.Errorf("repo: %+v", repo)
	}
	if folders := fact(t, e, "repository", "github/example/app", "runs")["folders"].([]any); len(folders) != 2 || folders[0] != "workloads/api" || folders[1] != "" {
		t.Errorf("what runs from the repository: %+v", folders)
	}
	if folders := fact(t, e, "repository", "github/example/old", "runs")["folders"].([]any); len(folders) != 0 {
		t.Errorf("nothing runs from old, and that is known: %+v", folders)
	}
	images := fact(t, e, "workload", "cluster-a/apps/web", "workload")["images"].([]any)
	if len(images) != 2 || images[1].(map[string]any)["registry"] != "docker.io" || images[1].(map[string]any)["tag"] != "latest" {
		t.Errorf("workload images: %+v", images)
	}
	if web, _ := e.Find("image", "ghcr.io/example/web:1.2.0"); value(web, "scan")["cve_critical"] != 1.0 || web.Ref == nil || *web.Ref != (estate.Ref{Blueprint: "sw_container_image", Key: "ghcr.io/example/web:1.2.0"}) {
		t.Errorf("a scanned image carries its scan, and its findings link to the image's entity: %+v", web)
	}
	if s, _ := e.Find("image", "nginx:latest"); has(s, "scan") {
		t.Error("an image the hub never scanned has no scan")
	}
	if s, _ := e.Find("image", "nginx:latest"); s.Ref != nil || value(s, "image")["registry"] != "docker.io" || len(s.Links) != 1 || s.Links[0] != (estate.Link{Kind: "workload", Key: "cluster-a/apps/web"}) {
		t.Errorf("an image is no catalog entity of its own, is parsed, and links to the workload that runs it: %+v", s)
	}
	if _, ok := e.Find("image", "ghcr.io/example/idle:0.1"); ok {
		t.Error("a scanned tag no workload runs is not judged, whatever namespace the scan saw it in")
	}
	if h := fact(t, e, "host", "it_workstation:osquery/AAA", "host"); h["last_seen"] != "2026-10-30T10:00:00Z" || h["last_patched"] != nil {
		t.Errorf("host: %+v", h)
	}
	api, ok := e.Find("api", "github/example/app/v1")
	if !ok || api.Ref == nil || api.Ref.Blueprint != "sw_api" || value(api, "api")["spec"] == "" {
		t.Errorf("only the API a built service provides is judged: %+v", api)
	}
	if len(e.Waivers) != 2 || e.Waivers[0].Owner != "The owner" || e.Waivers[0].Problem() != "" || e.Waivers[1].Problem() == "" || e.Waivers[0].From != "orrery" {
		t.Errorf("both waivers are read, and the one with no owner does not count: %+v", e.Waivers)
	}
	if app := fact(t, e, "app", "api", "app"); app["owner"] != "team-app" || app["docs"] != "" || app["description"] != "" || app["runs"] != 2.0 || app["program"] != "platform" {
		t.Errorf("an app's catalog fields, a value nobody set as empty: %+v", app)
	}
	if app := fact(t, e, "app", "app", "app"); app["runs"] != nil {
		t.Errorf("with no discovery counts, what runs it is unknown: %+v", app)
	}
	if p := fact(t, e, "program", "platform", "program"); p["team"] != "Operations" || p["workloads"] != 1.0 || p["lifecycle"] != "" {
		t.Errorf("program: %+v", p)
	}
	if owners := fact(t, e, "repository", "github/example/app", "runs")["owners"].([]any); len(owners) != 1 || owners[0] != "team-app" {
		t.Errorf("a repository's owners are its workloads': %+v", owners)
	}
}

func TestAMissingJoinLeavesItsFactUnknown(t *testing.T) {
	e := estate.New(time.Now())
	apply(t, e, standard(t), Records{"sw_repository": {rec(t, "sw_repository", "r", "r", `{"url":"https://github.com/example/r"}`, "")}})
	s, _ := e.Find("repository", "r")
	if has(s, "runs") || e.WaiversKnown {
		t.Errorf("unread joins stay unknown: %+v waivers known %v", s.Facts, e.WaiversKnown)
	}
}

func TestSinkWritesIntoTheBlueprintsTheEstateNames(t *testing.T) {
	metrics, err := meter.New()
	if err != nil {
		t.Fatal(err)
	}
	write := Write{FindingSecurity: "sec_finding", FindingPlatform: "plat_finding", Verdict: "plat_verdict", Pack: "plat_pack", Pillar: "plat_pillar"}
	packs := &pack.Set{Packs: []*pack.Pack{
		{ID: "security", Family: "security", Version: "2.0.0", State: pack.Active, Rules: rules(24)},
		{ID: "interfaces", Family: "platform", State: pack.Active},
		{ID: "secure", Family: "platform", State: pack.Active, Tiers: []string{"bronze", "silver", "gold"}},
		{ID: "trial", Family: "platform", State: pack.Draft, Tiers: []string{"bronze"}},
	}}
	sink, err := NewSink(nil, "https://trellis.test", trellis.Blueprints, write, nil, func() []string { return []string{"azure_storage", "sw_api", "sw_container_image"} }, metrics, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]map[string]any{}
	for _, bp := range sink.blueprints {
		ids[bp["id"].(string)] = bp
	}
	for _, id := range []string{"sec_finding", "plat_finding", "plat_verdict", "plat_pack"} {
		if ids[id] == nil {
			t.Errorf("offers %s: %v", id, ids)
		}
	}
	rels := ids["plat_verdict"]["relations"].(map[string]any)
	if rels["azure_storage"].(map[string]any)["target"] != "azure_storage" || rels["sw_api"] == nil {
		t.Errorf("a verdict links to every mapped blueprint: %v", rels)
	}
	if ids["sec_finding"]["relations"].(map[string]any)["pillar"].(map[string]any)["target"] != "plat_pillar" {
		t.Errorf("the pillar relation targets the named blueprint: %v", ids["sec_finding"]["relations"])
	}
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	storage := &estate.Ref{Blueprint: "azure_storage", Key: "sub/rg/acct"}
	snap := &board.Snapshot{RefreshedAt: at, Packs: packs, Evaluation: judge.Evaluation{
		Findings: []judge.Finding{
			{Rule: "image.cve-kev", Pack: "security", Severity: estate.Critical, Standard: "s", Pillar: "platform/security", Kind: "image", Subject: "ghcr.io/example/app:1.0", Title: "app", Ref: &estate.Ref{Blueprint: "sw_container_image", Key: "ghcr.io/example/app:1.0"}, Detail: "1 CVE", Fix: "Rebuild.", Evidence: []estate.Evidence{{Source: "scan", Query: "q", Got: "1"}}, State: judge.Open, Owner: "team-app", Overdue: true, Due: &at},
			{Rule: "api.versioned", Pack: "interfaces", Severity: estate.Medium, Standard: "s", Pillar: "platform/interfaces", Kind: "api", Subject: "github/example/app/v1", Title: "App API", Ref: &estate.Ref{Blueprint: "sw_api", Key: "github/example/app/v1"}, Detail: "unversioned", Fix: "Version it.", State: judge.Open},
			{Rule: "cluster.no-policy-engine", Pack: "security", Severity: estate.Medium, Pillar: "platform/security", Kind: "cluster", Subject: "cluster-a", Title: "cluster-a", State: judge.Open},
			{Rule: "repo.no-ci", Pack: "security", Severity: estate.Medium, Kind: "repository", Subject: "github/example/old", State: judge.Waived},
			{Rule: "storage.shared-key", Pack: "secure", Tier: "silver", Severity: estate.High, Pillar: "platform/security", Kind: "storage_account", Subject: "/subscriptions/s/acct", Title: "acct", Ref: storage, State: judge.Open},
		},
		Subjects: []judge.SubjectVerdict{
			{Kind: "storage_account", Key: "/subscriptions/s/acct", Title: "acct", Ref: storage,
				Tiers:     map[string]string{"secure": "bronze", "trial": ""},
				Findings:  []judge.Finding{{Rule: "storage.shared-key", Pack: "secure", State: judge.Open}, {Rule: "storage.tls", Pack: "secure", State: judge.Waived}},
				Unchecked: []judge.Unchecked{{Rule: "storage.private", Pack: "secure"}, {Rule: "x", Pack: "trial"}}},
			{Kind: "cluster", Key: "cluster-a", Title: "cluster-a"},
		},
	}}
	ents := sink.Entities(snap)
	if len(ents) != 5 {
		t.Fatalf("four open findings, not the waived one, and one verdict, the draft pack writing none: %d %+v", len(ents), ents)
	}
	img := ents[0]
	if img.Blueprint != "sec_finding" || img.Relations["sw_container_image"] != "sw_container_image:ghcr.io/example/app:1.0" || img.Relations["pillar"] != "plat_pillar:platform/security" {
		t.Errorf("image finding: %+v", img)
	}
	if !strings.Contains(img.Fields["evidence"].(string), "**scan**") || img.Fields["standard"] != "s" || img.Fields["first_seen"] != "2026-11-04T12:00:00Z" || img.Fields["owner"] != "team-app" || img.Fields["overdue"] != true || img.Fields["due"] != "2026-11-04T12:00:00Z" {
		t.Errorf("image fields: %+v", img.Fields)
	}
	if api := ents[1]; api.Blueprint != "plat_finding" || api.Relations["sw_api"] != "sw_api:github/example/app/v1" {
		t.Errorf("api finding: %+v", api)
	}
	if c := ents[2]; len(c.Relations) != 1 || c.Relations["pillar"] == nil {
		t.Errorf("a finding with no catalog entity links only its pillar: %+v", c.Relations)
	}
	v := ents[4]
	if v.Blueprint != "plat_verdict" || v.Key != "secure/storage_account//subscriptions/s/acct" || v.Relations["azure_storage"] != "azure_storage:sub/rg/acct" {
		t.Errorf("verdict: %+v", v)
	}
	if v.Fields["tier"] != "bronze" || v.Fields["tier_rank"] != 1 || v.Fields["findings"] != 1 || v.Fields["unchecked"] != 1 {
		t.Errorf("verdict fields: %+v", v.Fields)
	}
	if p := sink.PackEntities(snap); len(p) != 4 || p[0].Blueprint != "plat_pack" || p[0].Fields["version"] != "2.0.0" || len(p[2].Fields["tiers"].([]string)) != 3 {
		t.Errorf("packs adopted: %+v", p)
	}
}

func TestSinkWithNoPillarOrVerdictsWritesNeither(t *testing.T) {
	metrics, _ := meter.New()
	off := false
	sink, err := NewSink(nil, "https://trellis.test", trellis.Blueprints, Write{Verdicts: &off}, nil, nil, metrics, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, bp := range sink.blueprints {
		if bp["id"] == "verdict" || bp["id"] == "rule" || bp["id"] == "waiver" {
			t.Errorf("offers %v: verdicts are off, and no rule or waiver list is named", bp["id"])
		}
		if rels, ok := bp["relations"].(map[string]any); ok && rels["pillar"] != nil {
			t.Errorf("no pillar named links no pillar: %v", rels)
		}
	}
	if bp := sink.blueprints[0]["id"]; bp != "finding_security" {
		t.Errorf("an unnamed role keeps its own id: %v", bp)
	}
}

func TestAMappingReadsAnyListByExpression(t *testing.T) {
	e := estate.New(time.Now())
	recs := Records{
		"azure_storage": {
			rec(t, "azure_storage", "sub/rg/acct", "acct", `{"type":"storage_account","resource_id":"/subscriptions/S/resourceGroups/RG/providers/Microsoft.Storage/storageAccounts/acct","minimal_tls":"TLS1_0","shared_key_access":true}`, `{"program":[{"blueprint":"program","key":"4242","title":"Beacon"}]}`),
			rec(t, "azure_storage", "sub/rg/acr", "acr", `{"type":"container_registry"}`, ""),
			rec(t, "azure_storage", "sub/rg/nokey", "nokey", `{"type":"storage_account"}`, ""),
		},
		"program": {rec(t, "program", "4242", "Beacon", `{"criticality":"High"}`, "")},
	}
	m, err := Compile(Mapping{
		Kind: "storage_account", Blueprint: "azure_storage", Fact: "azure",
		Where: `entity.fields.type == "storage_account"`,
		Key:   `entity.fields.resource_id.lowerAscii()`,
		Join:  map[string]Join{"programs": {Blueprint: "program"}},
		Fields: map[string]string{
			"tls":         `entity.fields.minimal_tls`,
			"shared_key":  `entity.fields.shared_key_access`,
			"criticality": `joins.programs[entity.relations.program[0].key].fields.criticality`,
			"missing":     `entity.fields.nothing`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, _, err := m.Apply(e.Writer("orrery"), recs)
	if err != nil {
		t.Fatal(err)
	}
	if res.Subjects != 1 || len(res.Skipped) != 1 {
		t.Fatalf("one storage account, the registry filtered out, the one with no id skipped by name: %+v", res)
	}
	s, ok := e.Find("storage_account", "/subscriptions/s/resourcegroups/rg/providers/microsoft.storage/storageaccounts/acct")
	if !ok || s.Ref == nil || s.Ref.Blueprint != "azure_storage" || s.Ref.Key != "sub/rg/acct" {
		t.Fatalf("keyed by its resource id, linked to its entity: %+v", s)
	}
	f := value(s, "azure")
	if f["tls"] != "TLS1_0" || f["shared_key"] != true || f["criticality"] != "High" {
		t.Errorf("fields, one through a join: %+v", f)
	}
	if _, set := f["missing"]; set {
		t.Error("a field the entity lacks is unknown, not empty")
	}
}

func TestAMappingWithNoFieldsKeepsTheEntityWhole(t *testing.T) {
	e := estate.New(time.Now())
	m, err := Compile(Mapping{Kind: "storage_account", Blueprint: "azure_storage_account", Fact: "azure"})
	if err != nil {
		t.Fatal(err)
	}
	recs := Records{"azure_storage_account": {rec(t, "azure_storage_account", "sub/rg/acct", "acct", `{"min_tls":"TLS1_0"}`, `{"subscription":[{"blueprint":"azure_subscription","key":"sub"}]}`)}}
	if _, _, err := m.Apply(e.Writer("orrery"), recs); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Find("storage_account", "sub/rg/acct")
	f := value(s, "azure")
	if f["min_tls"] != "TLS1_0" || f["relations"].(map[string]any)["subscription"].([]any)[0].(map[string]any)["key"] != "sub" {
		t.Errorf("fact: %+v", f)
	}
}

func TestTwoMappingsFillOneSubject(t *testing.T) {
	e := estate.New(time.Now())
	a, _ := Compile(Mapping{Kind: "vm", Blueprint: "vmware_vm", Fact: "vm", Fields: map[string]string{"os": "entity.fields.guest_os"}})
	b, _ := Compile(Mapping{Kind: "vm", Blueprint: "vmware_vm", Fact: "vm", Fields: map[string]string{"on": `entity.fields.power == "poweredOn"`}})
	recs := Records{"vmware_vm": {rec(t, "vmware_vm", "vm-1", "app01", `{"guest_os":"Windows Server 2012","power":"poweredOn"}`, "")}}
	for _, m := range []*compiled{a, b} {
		if _, _, err := m.Apply(e.Writer("orrery"), recs); err != nil {
			t.Fatal(err)
		}
	}
	if f := fact(t, e, "vm", "vm-1", "vm"); f["os"] != "Windows Server 2012" || f["on"] != true {
		t.Errorf("both mappings' fields: %+v", f)
	}
}

func TestAWaiverMappingSetsWaivers(t *testing.T) {
	e := estate.New(time.Now())
	m, err := Compile(Mapping{Kind: WaiverKind, Blueprint: "posture_exception", Fields: map[string]string{
		"rule": "entity.fields.rule", "subject": "entity.fields.target", "reason": "entity.fields.why",
		"owner": "entity.relations.owner[0].title", "expires_at": "entity.fields.ends",
	}})
	if err != nil {
		t.Fatal(err)
	}
	maps := []*compiled{m}
	apply(t, e, maps, Records{"posture_exception": {
		rec(t, "posture_exception", "x1", "x1", `{"rule":"storage.tls","target":"acct","why":"vendor tool","ends":"2027-03-01T00:00:00Z"}`, `{"owner":[{"blueprint":"person","key":"p1","title":"A person"}]}`),
		rec(t, "posture_exception", "x2", "x2", `{"rule":"storage.tls","target":"acct2","why":"no owner"}`, ""),
	}})
	if !e.WaiversKnown || len(e.Waivers) != 2 || e.Waivers[0].Owner != "A person" || e.Waivers[1].Problem() == "" {
		t.Errorf("both read, the one with no owner and no end not counting: %+v", e.Waivers)
	}
}

func TestABadMappingIsRefusedWithItsName(t *testing.T) {
	for _, m := range []Mapping{
		{Blueprint: "b"},
		{Kind: "k"},
		{Kind: "k", Blueprint: "b", Key: "entity.("},
		{Kind: "k", Blueprint: "b", Fields: map[string]string{"x": "1 +"}},
		{Kind: "k", Blueprint: "b", Join: map[string]Join{"j": {}}},
		{Kind: WaiverKind, Blueprint: "b", Each: "entity.fields.list"},
	} {
		if _, err := Compile(m); err == nil {
			t.Errorf("accepted %+v", m)
		}
	}
}

func TestAnOptionalListThatFailsLeavesTheRestStanding(t *testing.T) {
	maps := needed(standard(t))
	if maps["sw_repository"] || maps["k8s_workload"] {
		t.Errorf("the inventory lists are required: %v", maps)
	}
	if !maps["sw_workload"] || !maps["plat_waiver"] || !maps["it_virtual_machine"] {
		t.Errorf("joins, waivers, and host lists may fail alone: %v", maps)
	}
}

func TestTheOfferDeclaresNoReservedField(t *testing.T) {
	metrics, _ := meter.New()
	sink, err := NewSink(nil, "https://trellis.test", trellis.Blueprints, Write{Rule: "rule", Waiver: "waiver"}, nil, nil, metrics, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.blueprints) != 6 {
		t.Errorf("every role offered once named: %d", len(sink.blueprints))
	}
	for _, bp := range sink.blueprints {
		fields, _ := bp["fields"].(map[string]any)
		for _, reserved := range []string{"title", "icon"} {
			if _, ok := fields[reserved]; ok {
				t.Errorf("%s declares %q, which every blueprint already has; the hub refuses the offer", bp["id"], reserved)
			}
		}
	}
}

func TestAWorkloadWithANullRepoStillMaps(t *testing.T) {
	e := estate.New(time.Now())
	apply(t, e, standard(t), Records{
		"sw_repository": {rec(t, "sw_repository", "r", "r", `{"url":"https://github.com/example/r"}`, "")},
		"sw_workload": {
			rec(t, "sw_workload", "demarc", "demarc", `{"repo":null}`, ""),
			rec(t, "sw_workload", "app", "app", `{"repo":"https://github.com/example/r/tree/main/deploy/app"}`, ""),
		},
	})
	if got := fact(t, e, "repository", "r", "runs")["folders"]; len(got.([]any)) != 1 {
		t.Errorf("the linked workload's folder, the null one skipped: %v", got)
	}
}

func TestSinkSendsTheTrendsHistoryKnows(t *testing.T) {
	metrics, _ := meter.New()
	secure := &pack.Set{Packs: []*pack.Pack{{ID: "secure", Family: "platform", State: pack.Active, Tiers: []string{"bronze", "silver", "gold"}}}}
	sink, err := NewSink(nil, "https://trellis.test", trellis.Blueprints, Write{}, nil, func() []string { return []string{"azure_storage"} }, metrics, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	opened := at.AddDate(0, 0, -12)
	seven, share := 4.5, 64.54
	snap := &board.Snapshot{RefreshedAt: at, Packs: secure, Evaluation: judge.Evaluation{
		Findings: []judge.Finding{{Rule: "storage.tls", Pack: "secure", Severity: estate.High, Kind: "storage_account", Subject: "acct", Title: "acct", State: judge.Open, FirstSeen: opened}},
		Subjects: []judge.SubjectVerdict{{Kind: "storage_account", Key: "acct", Title: "acct", Tiers: map[string]string{"secure": "silver"}, Checks: map[string]map[string]judge.CheckState{"secure": {"storage.tls": judge.CheckFail, "storage.keys": judge.CheckPass}}}},
		Packs:    []judge.PackVerdict{{Pack: "secure", Tally: judge.Tally{Level: estate.Amber, Conformant: &share, Overdue: 1}}},
	}, Trends: &trend.Trends{
		Window:   90 * 24 * time.Hour,
		Verdicts: map[string]trend.Verdict{"secure/storage_account/acct": {Points: []trend.Point{{At: at.AddDate(0, 0, -90), Value: 3}, {At: at.AddDate(0, 0, -3), Value: 2}}, Since: at.AddDate(0, 0, -3), Best: 3, Direction: "worse"}},
		Findings: map[string]trend.Finding{"storage.tls/acct": {Opened: opened, Since: opened, Reopened: 2, Cause: "rule"}},
		Packs:    map[string]trend.Pack{"secure": {Points: []trend.Point{{At: at.AddDate(0, 0, -1), Value: 60}, {At: at, Value: 64.5}}, Change7: &seven}},
	}}
	ents := sink.Entities(snap)
	f := ents[0].Fields
	if f["first_seen"] != opened.Format(time.RFC3339) || f["days_open"] != 12 || f["reopened"] != 2 || f["cause"] != "rule" {
		t.Errorf("finding history: %+v", f)
	}
	v := ents[1].Fields
	if c := v["checks"]; !reflect.DeepEqual(c, map[string]any{"storage.tls": "fail", "storage.keys": "pass"}) {
		t.Errorf("verdict checks: %#v", c)
	}
	tr, ok := v["trend"].(map[string]any)
	if !ok || len(tr["points"].([]map[string]any)) != 2 || tr["step"] != true || tr["max"] != 3 || tr["labels"].(map[string]string)["2"] != "silver" {
		t.Fatalf("verdict trend: %+v", v["trend"])
	}
	if !strings.Contains(tr["link"].(string), "open=s%3Astorage_account%7Cacct") || v["tier_best"] != 3 || v["tier_direction"] != "worse" {
		t.Errorf("verdict: %+v", v)
	}
	p := sink.PackEntities(snap)[0].Fields
	if p["conformant"] != 64.5 || p["level"] != "amber" || p["overdue"] != 1 || p["change_7d"] != 4.5 || p["change_30d"] != nil || p["trend"] == nil {
		t.Errorf("pack: %+v", p)
	}
	if tr["to"] != "2026-11-07T00:00:00Z" || tr["from"] != "2026-08-09T00:00:00Z" {
		t.Errorf("a tier trend's axis ends at the end of the pass's day, never at the pass's time: from %v to %v", tr["from"], tr["to"])
	}
	if pt := p["trend"].(map[string]any); pt["to"] != "2026-11-06T00:00:00Z" || pt["from"] != "2026-08-08T00:00:00Z" {
		t.Errorf("a share trend's axis ends on its latest day: from %v to %v", pt["from"], pt["to"])
	}
	snap.Trends = nil
	if f := sink.Entities(snap)[0].Fields; f["reopened"] != nil || f["days_open"] != 12 {
		t.Errorf("no history writes no reopen count, and days open still counts from the first sighting: %+v", f)
	}
}

// TestSinkWritesTheSameWhenNothingMoved keeps every pass of a day from
// rewriting the hub: two passes over the same findings and history write
// the same trend, the same as_of, and no time of the pass itself.
func TestSinkWritesTheSameWhenNothingMoved(t *testing.T) {
	metrics, _ := meter.New()
	secure := &pack.Set{Packs: []*pack.Pack{{ID: "secure", Family: "platform", State: pack.Active, Tiers: []string{"bronze", "silver", "gold"}}}}
	sink, err := NewSink(nil, "https://trellis.test", trellis.Blueprints, Write{}, nil, nil, metrics, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	share := 64.5
	day := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	at := func(refreshed time.Time) *board.Snapshot {
		return &board.Snapshot{RefreshedAt: refreshed, Packs: secure, Evaluation: judge.Evaluation{
			Findings: []judge.Finding{{Rule: "storage.tls", Pack: "secure", Severity: estate.High, Kind: "storage_account", Subject: "acct", Title: "acct", State: judge.Open, FirstSeen: day.AddDate(0, 0, -12), AsOf: refreshed.Add(-time.Hour)}},
			Subjects: []judge.SubjectVerdict{{Kind: "storage_account", Key: "acct", Title: "acct", Tiers: map[string]string{"secure": "silver"}}},
			Packs:    []judge.PackVerdict{{Pack: "secure", Tally: judge.Tally{Level: estate.Amber, Conformant: &share}}},
		}, Trends: &trend.Trends{
			Window:   90 * 24 * time.Hour,
			Verdicts: map[string]trend.Verdict{"secure/storage_account/acct": {Points: []trend.Point{{At: day.AddDate(0, 0, -90), Value: 3}, {At: day.AddDate(0, 0, -3), Value: 2}}, Since: day.AddDate(0, 0, -3), Best: 3, Direction: "worse"}},
			Packs:    map[string]trend.Pack{"secure": {Points: []trend.Point{{At: day.AddDate(0, 0, -1), Value: 60}, {At: day, Value: 64.5}}}},
		}}
	}
	morning, evening := at(day.Add(6*time.Hour)), at(day.Add(21*time.Hour+45*time.Minute))
	a, b := sink.Entities(morning), sink.Entities(evening)
	if _, ok := a[0].Fields["last_seen"]; ok {
		t.Errorf("a finding carries no last_seen; the hub keeps when it last saw an entity: %+v", a[0].Fields)
	}
	if a[0].Fields["as_of"] != "2026-11-06T00:00:00Z" || !reflect.DeepEqual(a[0].Fields["as_of"], b[0].Fields["as_of"]) {
		t.Errorf("as_of is the day judged, the same all day: %v then %v", a[0].Fields["as_of"], b[0].Fields["as_of"])
	}
	if !reflect.DeepEqual(a[1].Fields["trend"], b[1].Fields["trend"]) {
		t.Errorf("a verdict's trend changes only when a point does:\n%+v\n%+v", a[1].Fields["trend"], b[1].Fields["trend"])
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("two passes of one day write the same entities:\n%+v\n%+v", a, b)
	}
	pa, pb := sink.PackEntities(morning)[0].Fields["trend"], sink.PackEntities(evening)[0].Fields["trend"]
	if !reflect.DeepEqual(pa, pb) {
		t.Errorf("a pack's trend changes only when a day does:\n%+v\n%+v", pa, pb)
	}
	// The next day's pass still writes the same share trend: no day was
	// added to it.
	if next := sink.PackEntities(at(day.Add(30 * time.Hour)))[0].Fields["trend"]; !reflect.DeepEqual(pa, next) {
		t.Errorf("a share trend is built from its points alone:\n%+v\n%+v", pa, next)
	}
}

// TestOfferedColoursAreOnesTheHubKnows keeps blueprints.yaml to the hub's
// colour names, which it refuses a whole blueprint over.
func TestOfferedColoursAreOnesTheHubKnows(t *testing.T) {
	hub := map[string]bool{"blue": true, "turquoise": true, "green": true, "lime": true, "yellow": true, "orange": true, "red": true, "pink": true, "purple": true, "brown": true, "grey": true}
	var file struct {
		Blueprints []struct {
			ID     string `yaml:"id"`
			Fields map[string]struct {
				Colors map[string]string `yaml:"colors"`
			} `yaml:"fields"`
		} `yaml:"blueprints"`
	}
	if err := yaml.Unmarshal(trellis.Blueprints, &file); err != nil {
		t.Fatal(err)
	}
	for _, bp := range file.Blueprints {
		for name, f := range bp.Fields {
			for value, c := range f.Colors {
				if !hub[c] {
					t.Errorf("%s.%s: %s is %q, which the hub does not know", bp.ID, name, value, c)
				}
			}
		}
	}
}

// rules are n empty rules, for a pack whose rule count is all a test reads.
func rules(n int) []*pack.Rule {
	out := make([]*pack.Rule, n)
	for i := range out {
		out[i] = &pack.Rule{}
	}
	return out
}

func TestSinkWritesEveryRuleAndEveryWeighedWaiver(t *testing.T) {
	metrics, _ := meter.New()
	write := Write{Pillar: "plat_pillar", Rule: "plat_rule", Waiver: "plat_weighed_waiver"}
	sink, err := NewSink(nil, "https://trellis.test", trellis.Blueprints, write, nil, func() []string { return []string{"azure_resource"} }, metrics, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	offered := map[string]map[string]any{}
	for _, bp := range sink.blueprints {
		offered[bp["id"].(string)] = bp
	}
	if rels := offered["plat_rule"]["relations"].(map[string]any); rels["pillar"].(map[string]any)["target"] != "plat_pillar" || rels["azure_resource"] != nil {
		t.Errorf("a rule links its pillar and no subject: %v", rels)
	}
	if rels := offered["plat_weighed_waiver"]["relations"].(map[string]any); rels["azure_resource"] == nil {
		t.Errorf("a waiver links to the entity it is about: %v", rels)
	}
	p, err := pack.Parse([]byte(`
pack: azure-policy
title: Azure Policy
version: abc
family: security
rules:
  - { id: azure.private, title: Storage is private, kind: azure_resource, severity: high, standard: s, pillar: platform/security, controls: ["fw:1"], stages: [board], check: "true", detail: '"x"', fix: Fix it in Azure. }
  - { id: azure.pipeline, title: Only in a pipeline, kind: azure_resource, severity: low, standard: s, pillar: p, stages: [check], check: "true", detail: '"x"', fix: f }
`), "source azure_policy")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	end := at.AddDate(0, 2, 0)
	ref := &estate.Ref{Blueprint: "azure_resource", Key: "/subscriptions/s/st"}
	snap := &board.Snapshot{RefreshedAt: at, Packs: &pack.Set{Packs: []*pack.Pack{p}}, Evaluation: judge.Evaluation{
		Rules:    []judge.RuleStat{{Rule: "azure.private", Pass: 3, Fail: 1, Waived: 1}},
		Findings: []judge.Finding{{Rule: "azure.private", Pack: "azure-policy", Subject: "/subscriptions/s/st", Title: "st", Ref: ref, State: judge.Waived}},
		Waivers: []judge.WaiverView{
			{Waiver: estate.Waiver{Rule: "azure.private", Subject: "/subscriptions/s/st", Reason: "Public documents", Owner: "web-team", Category: estate.Accepted, ExpiresAt: end, From: "azure_policy", ID: "/subscriptions/s/providers/Microsoft.Authorization/policyExemptions/docs"}, Status: judge.WaiverActive, Covers: 1},
			{Waiver: estate.Waiver{Rule: "azure.private", Subject: "/subscriptions/s/logs", Reason: "Logs are open", From: "azure_policy"}, Status: judge.WaiverRejected, Problem: "it names no owner"},
			{Waiver: estate.Waiver{Rule: "azure.private", Subject: "/subscriptions/s/logs", Reason: "Logs again", From: "azure_policy"}, Status: judge.WaiverRejected, Problem: "it names no owner"},
		},
	}}
	rules := sink.Rules(snap)
	if len(rules) != 1 {
		t.Fatalf("one rule runs on the board: %+v", rules)
	}
	r := rules[0]
	if r.Blueprint != "plat_rule" || r.Key != "azure.private" || r.Title != "Storage is private" || r.Fields["source"] != "source azure_policy" || r.Fields["status"] != "counted" || r.Fields["pass"] != 3 || r.Fields["waived"] != 1 || r.Relations["pillar"] != "plat_pillar:platform/security" {
		t.Errorf("rule: %+v", r)
	}
	ws := sink.Waivers(snap)
	if len(ws) != 3 {
		t.Fatalf("every waiver weighed: %+v", ws)
	}
	w := ws[0]
	if w.Blueprint != "plat_weighed_waiver" || w.Key != "azure_policy/azure.private//subscriptions/s/st" || w.Title != "st: azure.private" || w.Relations["azure_resource"] != "azure_resource:/subscriptions/s/st" {
		t.Errorf("waiver: %+v", w)
	}
	if w.Fields["status"] != "active" || w.Fields["category"] != "accepted" || w.Fields["reason"] != "Public documents" || w.Fields["ends"] != end.Format(time.RFC3339) || w.Fields["id"] == nil || w.Fields["covers"] != 1 {
		t.Errorf("waiver fields: %+v", w.Fields)
	}
	if bad := ws[1]; bad.Fields["status"] != "rejected" || bad.Fields["problem"] != "it names no owner" || bad.Fields["owner"] != nil || len(bad.Relations) != 0 {
		t.Errorf("a rejected waiver says why: %+v", bad)
	}
	if ws[1].Key == ws[2].Key {
		t.Errorf("two waivers of one store, rule, and subject share a key: %s", ws[1].Key)
	}
	if n := len(sink.Entities(snap)); n != 3 {
		t.Errorf("the waivers ride with the findings' snapshot: %d entities", n)
	}
}
