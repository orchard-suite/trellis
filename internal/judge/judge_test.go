package judge

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/pack"
)

func mustPack(t *testing.T, doc string) *pack.Pack {
	t.Helper()
	p, err := pack.Parse([]byte(doc), "test")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func board(set *pack.Set) Options {
	return Options{Stage: pack.Board, Params: Merge(set)}
}

func set(t *testing.T, docs ...string) *pack.Set {
	t.Helper()
	s := &pack.Set{}
	for _, d := range docs {
		s.Packs = append(s.Packs, mustPack(t, d))
	}
	return s
}

func byKey(ev Evaluation) map[string]Finding {
	out := map[string]Finding{}
	for _, f := range ev.Findings {
		out[f.Key()] = f
	}
	return out
}

const security = `
pack: security
title: Security
version: 1.0.0
family: security
rules:
  - id: image.kev
    title: Exploited
    kind: image
    severity: critical
    standard: s
    pillar: platform/security
    needs: { scan.kev: not scanned }
    check: scan.kev == 0
    detail: '"exploited"'
    fix: Rebuild.
  - id: image.fresh
    title: Old
    kind: image
    severity: low
    standard: s
    pillar: platform/control_plane
    needs: { scan.age: not scanned }
    check: scan.age < 30
    detail: '"old"'
    fix: Rebuild.
`

const draft = `
pack: trial
title: Trial
version: 0.1.0
family: platform
status: draft
rules:
  - id: image.small
    title: Big
    kind: image
    severity: critical
    standard: s
    pillar: platform/cost
    check: false
    detail: '"too big"'
    fix: Shrink.
    stages: [board]
  - id: image.ci-only
    title: CI only
    kind: image
    severity: critical
    standard: s
    pillar: platform/cost
    check: false
    detail: '"never on the board"'
    fix: None.
    stages: [check]
`

func TestASubjectOnlyADraftJudgesIsADraftOne(t *testing.T) {
	e := estate.New(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC))
	w := e.Writer("src")
	_ = w.Subject("image", "scanned", "scanned").Set("scan", map[string]any{"kev": 0, "age": 1})
	w.Subject("box", "only-draft", "only-draft")
	const boxes = `
pack: trial
title: Trial
version: 0.1.0
family: platform
status: draft
rules:
  - id: box.small
    title: Big
    kind: box
    severity: medium
    standard: s
    pillar: platform/cost
    check: false
    detail: '"too big"'
    fix: Shrink.
`
	s := set(t, security, boxes)
	ev := Evaluate(e, s, board(s))
	kinds := map[string]KindVerdict{}
	for _, k := range ev.Kinds {
		kinds[k.Kind] = k
	}
	if box := kinds["box"]; box.Subjects != 1 || box.Draft != 1 || box.Grey != 0 {
		t.Errorf("a subject only a draft pack judged is a draft one, never no data: %+v", box)
	}
	if img := kinds["image"]; img.Draft != 0 || img.Green != 1 {
		t.Errorf("an actively judged subject keeps its level: %+v", img)
	}
	for _, sv := range ev.Subjects {
		if want := sv.Key == "only-draft"; sv.Draft != want {
			t.Errorf("%s draft %v, want %v", sv.Key, sv.Draft, want)
		}
	}
}

func TestEvaluateRollsUpWaivesAndKeepsDraftsApart(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	e := estate.New(now)
	w := e.Writer("src")
	_ = w.Subject("image", "bad", "bad").Set("scan", map[string]any{"kev": 1, "age": 90})
	_ = w.Subject("image", "waived", "waived").Set("scan", map[string]any{"kev": 0, "age": 90})
	_ = w.Subject("image", "good", "good").Set("scan", map[string]any{"kev": 0, "age": 1})
	w.Subject("image", "unscanned", "unscanned")
	e.Writer("git").AddWaivers([]estate.Waiver{
		{Rule: "image.fresh", Subject: "waived", Reason: "rebuilt monthly", Owner: "an owner", ExpiresAt: now.Add(time.Hour)},
		{Rule: "image.fresh", Subject: "bad", Reason: "lapsed", Owner: "an owner", ExpiresAt: now.Add(-time.Hour)},
		{Rule: "image.kev", Subject: "bad", Reason: "no owner"},
		{Rule: "image.kev", Subject: "good", Reason: "reviewed", Owner: "an owner", ReviewAt: now.Add(time.Hour), Control: "egress blocked"},
	})
	s := set(t, security, draft)
	ev := Evaluate(e, s, board(s))

	open, waived := ev.Open(), ev.In(Waived)
	if ev.Estate.Level != estate.Red || len(open) != 2 || len(waived) != 1 {
		t.Fatalf("estate %s, open %+v, waived %+v", ev.Estate.Level, open, waived)
	}
	if open[0].Rule != "image.kev" || open[0].Subject != "bad" || open[1].Rule != "image.fresh" {
		t.Errorf("worst first: %+v", open)
	}
	status := map[string]WaiverStatus{}
	for _, v := range ev.Waivers {
		status[v.Key()] = v.Status
		if v.Status == WaiverRejected && !strings.Contains(v.Problem, "owner") {
			t.Errorf("a waiver with no owner is set aside, with why: %+v", v)
		}
		if v.Key() == "image.fresh/waived" && v.Covers != 1 {
			t.Errorf("a waiver counts what it covers: %+v", v)
		}
	}
	if status["image.fresh/bad"] != WaiverEnded || status["image.kev/bad"] != WaiverRejected || status["image.kev/good"] != WaiverActive {
		t.Errorf("waiver statuses: %v", status)
	}
	stats := map[string]RuleStat{}
	for _, r := range ev.Rules {
		stats[r.Rule] = r
	}
	if f := stats["image.fresh"]; f.Fail != 1 || f.Waived != 1 || f.Pass != 1 || f.NoData != 1 {
		t.Errorf("rule health counts every answer: %+v", f)
	}
	if d := stats["image.small"]; d.Phase != pack.Drafted || d.Fail != 4 {
		t.Errorf("a draft rule's hits are counted apart from the counted ones: %+v", d)
	}
	if len(ev.In(Draft)) != 4 {
		t.Errorf("the draft pack judges the scanned and unscanned alike, on the board only: %+v", ev.In(Draft))
	}
	for _, sv := range ev.Subjects {
		want := map[string]estate.Level{"bad": estate.Red, "waived": estate.Green, "good": estate.Green, "unscanned": estate.Grey}[sv.Key]
		if sv.Level != want {
			t.Errorf("%s: %s, want %s; a draft finding never sets a level", sv.Key, sv.Level, want)
		}
	}
	pillars := map[string]estate.Level{}
	for _, p := range ev.Pillars {
		pillars[p.Pillar] = p.Level
	}
	if pillars["platform/security"] != estate.Red || pillars["platform/control_plane"] != estate.Amber || len(pillars) != 2 {
		t.Errorf("pillars come from counted rules only: %v", pillars)
	}
	check := Evaluate(e, s, Options{Stage: pack.Check, Params: Merge(s)})
	if d := check.In(Draft); len(d) != 4 || d[0].Rule != "image.ci-only" {
		t.Errorf("in check, the board-only rule is out and the check-only one is in: %+v", d)
	}
}

const tiered = `
pack: maturity
title: Maturity
version: 1.0.0
family: platform
tiers: [bronze, silver, gold]
rules:
  - id: service.owner
    title: No owner
    kind: service
    severity: low
    standard: s
    pillar: platform/catalog
    tier: bronze
    controls: ["soc2:CC1.3"]
    check: has(svc.owner)
    detail: '"no owner"'
    fix: Name one.
  - id: service.slo
    title: No SLO
    kind: service
    severity: low
    standard: s
    pillar: platform/outcomes
    tier: silver
    needs: { svc.slo: the SLO was not read }
    check: svc.slo
    detail: '"no SLO"'
    fix: Set one.
  - id: service.chaos
    title: Never failed over
    kind: service
    severity: low
    standard: s
    pillar: platform/outcomes
    tier: gold
    check: svc.drilled
    detail: '"never drilled"'
    fix: Drill it.
`

func TestTiersAreTheHighestLevelEveryLowerRulePasses(t *testing.T) {
	e := estate.New(time.Now())
	w := e.Writer("src")
	_ = w.Subject("service", "gold", "gold").Set("svc", map[string]any{"owner": "a team", "slo": true, "drilled": true})
	_ = w.Subject("service", "silver", "silver").Set("svc", map[string]any{"owner": "a team", "slo": true, "drilled": false})
	_ = w.Subject("service", "bronze", "bronze").Set("svc", map[string]any{"owner": "a team", "drilled": true})
	_ = w.Subject("service", "none", "none").Set("svc", map[string]any{"slo": true, "drilled": true})
	s := set(t, tiered)
	ev := Evaluate(e, s, board(s))
	want := map[string]string{"gold": "gold", "silver": "silver", "bronze": "bronze", "none": ""}
	for _, sv := range ev.Subjects {
		if got := sv.Tiers["maturity"]; got != want[sv.Key] {
			t.Errorf("%s reached %q, want %q (a missing SLO is no data, which holds it at bronze)", sv.Key, got, want[sv.Key])
		}
	}
	checks := map[string]map[string]CheckState{
		"gold":   {"service.owner": CheckPass, "service.slo": CheckPass, "service.chaos": CheckPass},
		"silver": {"service.owner": CheckPass, "service.slo": CheckPass, "service.chaos": CheckFail},
		"bronze": {"service.owner": CheckPass, "service.slo": CheckUnknown, "service.chaos": CheckPass},
		"none":   {"service.owner": CheckFail, "service.slo": CheckPass, "service.chaos": CheckPass},
	}
	for _, sv := range ev.Subjects {
		if got := sv.Checks["maturity"]; !reflect.DeepEqual(got, checks[sv.Key]) {
			t.Errorf("%s checks %v, want %v", sv.Key, got, checks[sv.Key])
		}
	}
	if c := ev.Packs[0].Tiers; c["gold"] != 1 || c["silver"] != 1 || c["bronze"] != 1 || c[""] != 1 {
		t.Errorf("tier counts: %v", c)
	}
	for _, f := range ev.Findings {
		if f.Rule == "service.owner" && (f.Tier != "bronze" || f.Controls[0] != "soc2:CC1.3") {
			t.Errorf("a finding carries its tier and controls: %+v", f)
		}
	}
	if _, err := pack.Parse([]byte(strings.Replace(tiered, "tier: gold", "tier: platinum", 1)), "test"); err == nil {
		t.Error("a tier the pack does not name was taken")
	}
	if _, err := pack.Parse([]byte(strings.Replace(tiered, "    tier: gold\n", "", 1)), "test"); err == nil {
		t.Error("a rule with no tier in a tiered pack was taken")
	}
}

// TestChecksAgreeWithTheTier holds each subject's checks to the rules that
// set its tier: a waived failure is a pass, and a rule that does not apply,
// a draft, and an announced rule are left out.
func TestChecksAgreeWithTheTier(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	e := estate.New(now)
	w := e.Writer("src")
	_ = w.Subject("service", "a", "a").Set("svc", map[string]any{"owner": "a team", "slo": false})
	_ = w.Subject("service", "b", "b").Set("svc", map[string]any{"slo": true, "legacy": true})
	e.Writer("git").AddWaivers([]estate.Waiver{{Rule: "service.slo", Subject: "a", Reason: "accepted", Owner: "o", ExpiresAt: now.Add(time.Hour)}})
	s := set(t, `
pack: maturity
title: Maturity
version: 1.0.0
family: platform
tiers: [bronze, silver]
rules:
  - { id: service.owner, title: No owner, kind: service, severity: low, standard: s, pillar: p, tier: bronze, check: has(svc.owner), detail: '"no owner"', fix: f }
  - { id: service.slo, title: No SLO, kind: service, severity: low, standard: s, pillar: p, tier: silver, check: svc.slo, detail: '"no SLO"', fix: f }
  - { id: service.legacy, title: Legacy, kind: service, severity: low, standard: s, pillar: p, tier: silver, skip: [{ if: has(svc.legacy), because: legacy }], check: "false", detail: '"legacy"', fix: f }
  - { id: service.draft, title: Draft, kind: service, severity: low, standard: s, pillar: p, tier: bronze, status: draft, check: "false", detail: '"draft"', fix: f }
  - { id: service.soon, title: Soon, kind: service, severity: low, standard: s, pillar: p, tier: bronze, from: 2027-01-01T00:00:00Z, check: "false", detail: '"soon"', fix: f }
`)
	ev := Evaluate(e, s, Options{Stage: pack.Board, Params: Merge(s), Waivers: estate.Policy{}})
	want := map[string]map[string]CheckState{
		"a": {"service.owner": CheckPass, "service.slo": CheckPass, "service.legacy": CheckFail},
		"b": {"service.owner": CheckFail, "service.slo": CheckPass},
	}
	tiers := map[string]string{"a": "bronze", "b": ""}
	for _, sv := range ev.Subjects {
		if got := sv.Checks["maturity"]; !reflect.DeepEqual(got, want[sv.Key]) {
			t.Errorf("%s checks %v, want %v", sv.Key, got, want[sv.Key])
		}
		if sv.Tiers["maturity"] != tiers[sv.Key] {
			t.Errorf("%s tier %q, want %q", sv.Key, sv.Tiers["maturity"], tiers[sv.Key])
		}
	}
}

func TestAWaiverWithAMatchCoversOnlyThatChange(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	e := estate.New(now)
	w := e.Writer("src")
	_ = w.Subject("image", "a", "a").Set("scan", map[string]any{"kev": 1, "age": 1})
	_ = w.Subject("image", "b", "b").Set("scan", map[string]any{"kev": 1, "age": 1})
	e.Writer("git").AddWaivers([]estate.Waiver{
		{Rule: "image.kev", Subject: "a", Reason: "accepted", Owner: "o", ExpiresAt: now.Add(time.Hour), Match: "exploited"},
		{Rule: "image.kev", Subject: "b", Reason: "accepted", Owner: "o", ExpiresAt: now.Add(time.Hour), Match: "something else"},
	})
	s := set(t, security)
	ev := Evaluate(e, s, board(s))
	if wv, o := ev.In(Waived), ev.Open(); len(wv) != 1 || wv[0].Subject != "a" || len(o) != 1 || o[0].Subject != "b" {
		t.Errorf("waived %+v open %+v", wv, o)
	}
}

const scoped = `
pack: build
title: Build
version: 1.0.0
family: platform
scopes:
  live: '!repo.archived'
rules:
  - id: repo.ci
    title: No CI
    kind: repository
    severity: medium
    standard: s
    pillar: platform/golden_paths
    scope: live
    needs: { repo.has_ci: the forge did not say }
    check: repo.has_ci
    detail: '"no CI"'
    fix: Add CI.
  - id: repo.readme
    title: No README
    kind: repository
    severity: low
    standard: s
    pillar: platform/golden_paths
    from: 2026-12-01T00:00:00Z
    check: repo.readme
    detail: '"no README"'
    fix: Write one.
`

func TestAScopeKeepsASubjectOutOfTheCount(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	e := estate.New(now)
	w := e.Writer("src")
	_ = w.Subject("repository", "dead", "dead").Set("repo", map[string]any{"archived": true, "has_ci": false})
	_ = w.Subject("repository", "live", "live").Set("repo", map[string]any{"archived": false, "has_ci": false, "readme": true})
	_ = w.Subject("repository", "unknown", "unknown").Set("other", map[string]any{})
	s := set(t, scoped)
	ev := Evaluate(e, s, board(s))
	got := byKey(ev)
	if _, ok := got["repo.ci/dead"]; ok {
		t.Error("an archived repository is outside the live scope: no finding")
	}
	if got["repo.ci/live"].State != Open {
		t.Errorf("a live one without CI is open: %+v", got["repo.ci/live"])
	}
	if p := ev.Packs[0]; p.Subjects != 2 || p.Amber != 1 || p.Grey != 1 {
		t.Errorf("the one with no facts is grey, never green: %+v", p.Tally)
	}
	if ev.Estate.Subjects != 2 {
		t.Errorf("the archived one, which no rule applies to, is in no count: %+v", ev.Estate)
	}
	if err := s.Scope(map[string]map[string]string{"build": {"live": "true"}}); err != nil {
		t.Fatal(err)
	}
	if f := byKey(Evaluate(e, s, board(s)))["repo.ci/dead"]; f.State != Open {
		t.Errorf("an estate that redefines the scope counts the archived one too: %+v", f)
	}
	if err := s.Scope(map[string]map[string]string{"build": {"nope": "true"}}); err == nil {
		t.Error("a scope the pack does not have was redefined")
	}
}

func TestAnAnnouncedRuleShowsWhatIsComingAndCountsFromItsDay(t *testing.T) {
	before := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	s := set(t, scoped)
	at := func(now time.Time) Evaluation {
		e := estate.New(now)
		_ = e.Writer("src").Subject("repository", "r", "r").Set("repo", map[string]any{"archived": false, "has_ci": true, "readme": false})
		return Evaluate(e, s, board(s))
	}
	ev := at(before)
	f := byKey(ev)["repo.readme/r"]
	if f.State != Announced || f.CountsFrom == nil || !f.CountsFrom.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) || ev.Estate.Level != estate.Green {
		t.Errorf("before its day, the rule is announced and counts nowhere: %+v level %s", f, ev.Estate.Level)
	}
	ev = at(before.AddDate(0, 1, 0))
	if f := byKey(ev)["repo.readme/r"]; f.State != Open || ev.Estate.Level != estate.Amber {
		t.Errorf("from its day, it counts: %+v level %s", f, ev.Estate.Level)
	}
}

func TestDeadlinesCountFromWhenAFindingWasFirstSeen(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	e := estate.New(now)
	w := e.Writer("src")
	_ = w.Subject("image", "old", "old").Set("scan", map[string]any{"kev": 1, "age": 1})
	_ = w.Subject("image", "new", "new").Set("scan", map[string]any{"kev": 1, "age": 1})
	s := set(t, security)
	o := board(s)
	o.Deadlines = map[estate.Severity]time.Duration{estate.Critical: 7 * 24 * time.Hour}
	o.FirstSeen = func(key string) (time.Time, bool) {
		if key == "image.kev/old" {
			return now.AddDate(0, 0, -10), true
		}
		return time.Time{}, false
	}
	ev := Evaluate(e, s, o)
	got := byKey(ev)
	if f := got["image.kev/old"]; !f.Overdue || !f.Due.Equal(now.AddDate(0, 0, -3)) {
		t.Errorf("seen ten days ago with seven to fix: overdue three days: %+v", f)
	}
	if f := got["image.kev/new"]; f.Overdue || !f.FirstSeen.Equal(now) || !f.Due.Equal(now.AddDate(0, 0, 7)) {
		t.Errorf("first seen now, due in seven days: %+v", f)
	}
	if ev.Estate.Overdue != 1 || ev.Packs[0].Overdue != 1 {
		t.Errorf("overdue counts: estate %d pack %d", ev.Estate.Overdue, ev.Packs[0].Overdue)
	}
}

func TestOwnersComeFromTheSubjectOrWhatItLinksTo(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	e := estate.New(now)
	w := e.Writer("src")
	wl := w.Subject("workload", "apps/web", "web")
	_ = wl.Set("workload", map[string]any{"owner": "team-web"})
	wl.Link("image", "web:1")
	_ = w.Subject("image", "web:1", "web:1").Set("scan", map[string]any{"kev": 1, "age": 1})
	_ = w.Subject("image", "stray:1", "stray:1").Set("scan", map[string]any{"kev": 1, "age": 1})
	s := set(t, security)
	o := board(s)
	owners, err := NewOwners(map[string]string{"image": `linked.workload[0].workload.owner`})
	if err != nil {
		t.Fatal(err)
	}
	o.Owners = owners
	ev := Evaluate(e, s, o)
	got := byKey(ev)
	if got["image.kev/web:1"].Owner != "team-web" || got["image.kev/stray:1"].Owner != "" {
		t.Errorf("an image's owner is its workload's, across the link back; none when nothing says: %+v", ev.Findings)
	}
	if len(ev.Owners) != 2 || ev.Owners[0].Owner != "" || ev.Owners[1].Owner != "team-web" || ev.Owners[1].Findings != 1 {
		t.Errorf("owners tally what they answer for, the unowned apart: %+v", ev.Owners)
	}
}

func TestAShareRollupHoldsRedForACriticalAndOtherwiseByShare(t *testing.T) {
	e := estate.New(time.Now())
	w := e.Writer("src")
	for i, age := range []int{1, 1, 1, 1, 1, 1, 1, 1, 1, 90} {
		_ = w.Subject("image", string(rune('a'+i)), "").Set("scan", map[string]any{"kev": 0, "age": age})
	}
	s := set(t, security)
	o := board(s)
	o.Rollup = Rollup{Mode: Share, Green: 95, Amber: 80}
	ev := Evaluate(e, s, o)
	if ev.Estate.Level != estate.Amber || *ev.Estate.Conformant != 90 {
		t.Errorf("nine of ten green is amber at 95/80: %s %v", ev.Estate.Level, *ev.Estate.Conformant)
	}
	o.Rollup.Amber, o.Rollup.Green = 50, 90
	if ev := Evaluate(e, s, o); ev.Estate.Level != estate.Green {
		t.Errorf("at 90/50 it is green: %s", ev.Estate.Level)
	}
	_ = w.Subject("image", "k", "").Set("scan", map[string]any{"kev": 1, "age": 1})
	if ev := Evaluate(e, s, o); ev.Estate.Level != estate.Red || ev.Estate.Critical != 1 {
		t.Errorf("one critical finding holds it red whatever the share: %s", ev.Estate.Level)
	}
	o.Rollup = Rollup{}
	if ev := Evaluate(e, s, o); ev.Estate.Level != estate.Red {
		t.Errorf("worst takes the worst: %s", ev.Estate.Level)
	}
	for _, bad := range []Rollup{{Mode: Share, Green: 80, Amber: 90}, {Mode: "median"}, {Mode: Share}} {
		if bad.Check() == nil {
			t.Errorf("%+v was taken", bad)
		}
	}
}

func TestTheEstateBoundsHowLongAWaiverRuns(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	e := estate.New(now)
	_ = e.Writer("src").Subject("image", "a", "a").Set("scan", map[string]any{"kev": 1, "age": 90})
	e.Writer("git").AddWaivers([]estate.Waiver{
		{Rule: "image.kev", Subject: "a", Reason: "vendor fix pending", Owner: "o", ExpiresAt: now.AddDate(1, 0, 0)},
		{Rule: "image.fresh", Subject: "a", Reason: "rebuilt monthly", Owner: "o", GrantedAt: now.AddDate(0, 0, -20), ExpiresAt: now.AddDate(0, 0, 5)},
		{Rule: "image.gone", Subject: "a", Reason: "old rule", Owner: "o", ExpiresAt: now.AddDate(0, 0, 5)},
	})
	s := set(t, security)
	o := board(s)
	o.Waivers = estate.Policy{MaxDays: map[estate.Severity]int{estate.Critical: 30, estate.Low: 90}, WarnDays: 14}
	ev := Evaluate(e, s, o)
	views := map[string]WaiverView{}
	for _, v := range ev.Waivers {
		views[v.Key()] = v
	}
	if v := views["image.kev/a"]; v.Status != WaiverRejected || !strings.Contains(v.Problem, "may run 30") {
		t.Errorf("a year on a critical rule is past the limit: %+v", v)
	}
	if v := views["image.fresh/a"]; v.Status != WaiverEnding {
		t.Errorf("a waiver five days from its end is ending: %+v", v)
	}
	if v := views["image.gone/a"]; v.Status != WaiverRejected {
		t.Errorf("a waiver for a rule no pack has does not count: %+v", v)
	}
	got := byKey(ev)
	if got["image.kev/a"].State != Open || got["image.fresh/a"].State != Waived || !got["image.fresh/a"].WaiverEnding {
		t.Errorf("findings: %+v", ev.Findings)
	}
}

func TestAFindingOnACarriedFactSaysSoAndHowOld(t *testing.T) {
	then := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	prev := estate.New(then)
	_ = prev.Writer("scanner").Subject("image", "a", "a").Set("scan", map[string]any{"kev": 1, "age": 1})
	now := estate.New(then.Add(time.Hour))
	now.Carry(prev, map[string]bool{"scanner": true}, func(string) time.Duration { return 24 * time.Hour })
	s := set(t, security)
	ev := Evaluate(now, s, board(s))
	f := byKey(ev)["image.kev/a"]
	if f.State != Open || !f.Carried || !f.AsOf.Equal(then) {
		t.Errorf("the finding stands on the last reading, marked carried, as of then: %+v", f)
	}
	for _, r := range ev.Rules {
		if r.Rule == "image.kev" && r.Carried != 1 {
			t.Errorf("rule health counts carried answers: %+v", r)
		}
	}
}

func TestARuleThatReadsAFactNoSourceWritesIsNamed(t *testing.T) {
	e := estate.New(time.Now())
	s := set(t, security)
	o := board(s)
	o.Written = map[string]bool{"image": true}
	ev := Evaluate(e, s, o)
	for _, r := range ev.Rules {
		if len(r.Unwritten) != 1 || r.Unwritten[0] != "scan" {
			t.Errorf("%s reads scan, which nothing writes: %+v", r.Rule, r.Unwritten)
		}
	}
}
