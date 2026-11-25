package trellis

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/judge"
	"github.com/orchard-suite/trellis/internal/pack"
	"github.com/orchard-suite/trellis/internal/packtest"
)

func TestEveryBuiltInPackCompiles(t *testing.T) {
	set, err := pack.Load(Packs(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"security": true, "telemetry": true, "build": true, "interfaces": true, "graft": true}
	for _, p := range set.Packs {
		delete(want, p.ID)
	}
	if len(want) > 0 {
		t.Fatalf("packs missing: %v", want)
	}
}

// TestEveryRuleAnswersItsFixtures holds every built-in rule to the three
// answers its pack's file in packs/testdata gives it: fail with a detail,
// pass, and no data.
func TestEveryRuleAnswersItsFixtures(t *testing.T) {
	set, err := pack.Load(Packs(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	fx, err := packtest.ReadDir("packs/testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range packtest.Run(set, fx) {
		t.Error(p)
	}
}

// TestAccessGrantTiersWaitToBeTurnedOn holds the access pack's grant and
// application rules to n/a on the subjects they fail, until an estate
// names the tiers in access_tiers; the admin rule waits for admin_roles too.
func TestAccessGrantTiersWaitToBeTurnedOn(t *testing.T) {
	set, err := pack.Load(Packs(), "", []string{"access"})
	if err != nil {
		t.Fatal(err)
	}
	fx, err := packtest.Read("packs/testdata/access.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defaults := judge.Merge(set)["access"]
	hygieneOnly := map[string]any{}
	for k, v := range defaults {
		hygieneOnly[k] = v
	}
	hygieneOnly["access_tiers"] = []any{"hygiene"}
	for _, c := range []struct {
		rule   string
		params map[string]any
	}{
		{"access.grant-account-gone", defaults},
		{"access.grant-guest", defaults},
		{"access.admin-direct", defaults},
		{"access.admin-direct", hygieneOnly},
		{"access.application-unreviewed", defaults},
		{"access.application-unreviewed", hygieneOnly},
	} {
		r, ok := set.Rule(c.rule)
		if !ok {
			t.Fatalf("no rule %s", c.rule)
		}
		s := estate.New(fx.Now).Writer("fixture").Subject(r.Kind, "example", "example")
		for name, v := range fx.Rules[c.rule]["fail"] {
			if err := s.Set(name, v); err != nil {
				t.Fatal(err)
			}
		}
		if res := r.Judge(pack.Prepare(s.Subject, fx.Now, nil), c.params); res.Status != pack.NotApplicable {
			t.Errorf("%s with access_tiers %v and admin_roles %v answered %s: %s", c.rule, c.params["access_tiers"], c.params["admin_roles"], res.Status, res.Detail)
		}
	}
}

// TestApplicationMarkedForReviewIsJudgedWithoutGrants holds the reviewed
// tier to an application marked for access review even when the catalog
// holds none of its grants, and leaves an unmarked one with no grants n/a.
func TestApplicationMarkedForReviewIsJudgedWithoutGrants(t *testing.T) {
	set, err := pack.Load(Packs(), "", []string{"access"})
	if err != nil {
		t.Fatal(err)
	}
	r, ok := set.Rule("access.application-unreviewed")
	if !ok {
		t.Fatal("no rule access.application-unreviewed")
	}
	params := map[string]any{}
	for k, v := range judge.Merge(set)["access"] {
		params[k] = v
	}
	params["access_tiers"] = []any{"reviewed"}
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name   string
		review bool
		want   pack.Status
		detail string
	}{
		{"marked", true, pack.Fail, "no access review on record"},
		{"unmarked", false, pack.NotApplicable, ""},
	} {
		s := estate.New(now).Writer("fixture").Subject(r.Kind, c.name, c.name)
		if err := s.Set("application", map[string]any{"grants": 0, "reviewed_at": "", "review": c.review}); err != nil {
			t.Fatal(err)
		}
		res := r.Judge(pack.Prepare(s.Subject, now, nil), params)
		if res.Status != c.want || (c.detail != "" && res.Detail != c.detail) {
			t.Errorf("%s: answered %s: %q, want %s: %q", c.name, res.Status, res.Detail, c.want, c.detail)
		}
	}
}

// TestGraftDataRunnersWaitForTheEstate holds a runner that serves the
// data tier to a finding until the estate turns data_runners on.
func TestGraftDataRunnersWaitForTheEstate(t *testing.T) {
	set, err := pack.Load(Packs(), "", []string{"graft"})
	if err != nil {
		t.Fatal(err)
	}
	fx, err := packtest.Read("packs/testdata/graft.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r, ok := set.Rule("graft.runner-data")
	if !ok {
		t.Fatal("no rule graft.runner-data")
	}
	for _, c := range []struct {
		on   bool
		want pack.Status
	}{{false, pack.Fail}, {true, pack.Pass}} {
		params := map[string]any{}
		for k, v := range judge.Merge(set)["graft"] {
			params[k] = v
		}
		params["data_runners"] = c.on
		s := estate.New(fx.Now).Writer("fixture").Subject(r.Kind, "example", "example")
		for name, v := range fx.Rules[r.ID]["fail"] {
			if err := s.Set(name, v); err != nil {
				t.Fatal(err)
			}
		}
		if res := r.Judge(pack.Prepare(s.Subject, fx.Now, nil), params); res.Status != c.want {
			t.Errorf("data_runners %v answered %s: %s", c.on, res.Status, res.Detail)
		}
	}
}

// TestChecksAgreeWithTiersOverEveryFixture judges every built-in fixture
// at once and holds each subject's checks to the tier it reached: tier N
// exactly when every recorded rule at tier N or below is pass.
func TestChecksAgreeWithTiersOverEveryFixture(t *testing.T) {
	set, err := pack.Load(Packs(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	fx, err := packtest.ReadDir("packs/testdata")
	if err != nil {
		t.Fatal(err)
	}
	e := estate.New(fx.Now)
	w := e.Writer("fixture")
	for _, r := range set.Rules() {
		for name, facts := range fx.Rules[r.ID] {
			key := r.ID + "/" + name
			h := w.Subject(r.Kind, key, key)
			for k, v := range facts {
				if k != "linked" {
					if err := h.Set(k, v); err != nil {
						t.Fatal(err)
					}
					continue
				}
				for lk, list := range v.(map[string]any) {
					items, _ := list.([]any)
					for i, item := range items {
						lkey := fmt.Sprintf("%s/linked-%d", key, i)
						l := w.Subject(lk, lkey, lkey)
						for fname, fv := range item.(map[string]any) {
							if err := l.Set(fname, fv); err != nil {
								t.Fatal(err)
							}
						}
						h.Link(lk, lkey)
					}
				}
			}
		}
	}
	params := judge.Merge(set)
	for id, over := range fx.Params {
		merged := map[string]any{}
		for k, v := range params[id] {
			merged[k] = v
		}
		for k, v := range over {
			merged[k] = v
		}
		plain, err := estate.Plain(merged)
		if err != nil {
			t.Fatal(err)
		}
		params[id], _ = plain.(map[string]any)
	}
	ev := judge.Evaluate(e, set, judge.Options{Stage: pack.Board, Params: params})
	tiered := 0
	for _, sv := range ev.Subjects {
		for id, reached := range sv.Tiers {
			p, _ := set.Pack(id)
			checks := sv.Checks[id]
			if len(checks) == 0 {
				t.Errorf("%s/%s reached %q in %s with no checks", sv.Kind, sv.Key, reached, id)
			}
			want := ""
			for i, tier := range p.Tiers {
				met := true
				for rule, state := range checks {
					r, _ := set.Rule(rule)
					if slices.Index(p.Tiers, r.Tier) <= i && state != judge.CheckPass {
						met = false
					}
				}
				if !met {
					break
				}
				want = tier
			}
			if reached != want {
				t.Errorf("%s/%s reached %q in %s, but its checks %v say %q", sv.Kind, sv.Key, reached, id, checks, want)
			}
			tiered++
		}
		for id := range sv.Checks {
			if _, ok := sv.Tiers[id]; !ok {
				t.Errorf("%s/%s has checks in %s but no tier", sv.Kind, sv.Key, id)
			}
		}
	}
	if tiered < 100 {
		t.Errorf("only %d tiered verdicts were judged", tiered)
	}
}

// TestEveryBuiltInRuleHasAnAim holds every built-in rule to an aim: the
// requirement as met, which a catalog shows beside a check mark.
func TestEveryBuiltInRuleHasAnAim(t *testing.T) {
	set, err := pack.Load(Packs(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range set.Rules() {
		switch {
		case strings.TrimSpace(r.Aim) == "":
			t.Errorf("%s has no aim", r.ID)
		case strings.HasSuffix(r.Aim, "."):
			t.Errorf("%s: an aim ends without a period: %q", r.ID, r.Aim)
		}
	}
}

// TestAQuietLogStreamSaysNothing holds the log rules to no data on a
// window with no line, unless the selector matched no stream over the
// lookback, which is the declaration's fault and fails.
func TestAQuietLogStreamSaysNothing(t *testing.T) {
	set, err := pack.Load(Packs(), "", []string{"telemetry"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	decl := map[string]any{"service": "myapp", "namespace": "myapps", "host": "myapp.example.test", "runtime": "go", "edge": "cloudflare"}
	logs := func(seen map[string]any, kubelet int) map[string]any {
		m := map[string]any{"lines": 0, "not_json": 0, "no_timestamp": 0, "no_level": 0, "no_message": 0, "kubelet": kubelet, "evidence": []any{}}
		if seen != nil {
			m["seen"] = seen
		}
		return m
	}
	traces := func(seen map[string]any) map[string]any {
		m := map[string]any{"lines": 0, "traced": 0, "evidence": []any{}}
		if seen != nil {
			m["seen"] = seen
		}
		return m
	}
	never := map[string]any{"lookback": "30d", "streams": 0}
	unread := map[string]any{"error": "could not read the store: timeout", "evidence": []any{}}
	cases := []struct {
		name, fact string
		value      map[string]any
		want       pack.Status
		detail     string
	}{
		{"no stream ever", "logs", logs(never, 0), pack.Fail, "the log selector matches no stream in the last 30d"},
		{"quiet", "logs", logs(map[string]any{"lookback": "30d", "streams": 1}, 0), pack.NoData, "no log lines in the window"},
		{"only kubelet lines", "logs", logs(nil, 2), pack.NoData, "no log lines in the window"},
		{"store down", "logs", unread, pack.NoData, "could not read the store: timeout"},
		{"quiet", "trace_ids", traces(map[string]any{"lookback": "30d", "streams": 1}), pack.NoData, "no log lines in the window"},
		{"no stream ever", "trace_ids", traces(never), pack.Fail, "the log selector matches no stream in the last 30d"},
		{"store down", "trace_ids", unread, pack.NoData, "could not read the store: timeout"},
	}
	rules := map[string][]string{"logs": {"telemetry.logs-json", "telemetry.logs-fields"}, "trace_ids": {"telemetry.logs-trace-id"}}
	for _, c := range cases {
		for _, id := range rules[c.fact] {
			r, ok := set.Rule(id)
			if !ok {
				t.Fatalf("no rule %s", id)
			}
			s := estate.New(now).Writer("fixture").Subject(r.Kind, "myapp", "myapp")
			if err := s.Set("telemetry", map[string]any{"declaration": decl, c.fact: c.value}); err != nil {
				t.Fatal(err)
			}
			res := r.Judge(pack.Prepare(s.Subject, now, nil), nil)
			if res.Status != c.want || !strings.HasPrefix(res.Detail, c.detail) {
				t.Errorf("%s, %s: answered %s: %q, want %s: %q", id, c.name, res.Status, res.Detail, c.want, c.detail)
			}
		}
	}
}
