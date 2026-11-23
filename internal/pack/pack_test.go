package pack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/orchard-suite/trellis/internal/estate"
)

const onePack = `
pack: sample
title: Sample
version: 1.0.0
family: platform
params: { limit: 3 }
rules:
  - id: thing.count
    title: Too many
    kind: thing
    severity: medium
    standard: some-rule
    pillar: platform/catalog
    skip: [{ if: thing.retired, because: retired }]
    needs: { thing.count: nobody counted }
    unknown: [{ if: thing.count < 0, explain: '"counted " + string(thing.count)' }]
    check: thing.count <= params.limit
    detail: plural(thing.count, "thing", "things") + " over"
    fix: Have fewer.
`

func judge(t *testing.T, p *Pack, facts map[string]any) Result {
	t.Helper()
	e := estate.New(time.Now())
	s := e.Writer("src").Subject("thing", "a", "a")
	for k, v := range facts {
		if err := s.Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	params, _ := estate.Plain(p.Params)
	return p.Rules[0].Judge(Prepare(s.Subject, e.Now, nil), params.(map[string]any))
}

func TestJudgeAnswersInOrder(t *testing.T) {
	p, err := Parse([]byte(onePack), "test")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		facts  map[string]any
		status Status
		detail string
	}{
		{map[string]any{"thing": map[string]any{"retired": true}}, NotApplicable, "retired"},
		{map[string]any{"thing": map[string]any{"retired": false}}, NoData, "nobody counted"},
		{map[string]any{"thing": map[string]any{"retired": false, "count": -1}}, NoData, "counted -1"},
		{map[string]any{"thing": map[string]any{"retired": false, "count": 9}}, Fail, "9 things over"},
		{map[string]any{"thing": map[string]any{"retired": false, "count": 1}}, Pass, ""},
		{map[string]any{"thing": map[string]any{"retired": false, "count": "many"}}, NoData, "cannot judge"},
	}
	for _, c := range cases {
		res := judge(t, p, c.facts)
		if res.Status != c.status || !strings.HasPrefix(res.Detail, c.detail) {
			t.Errorf("%v: %s %q, want %s %q", c.facts, res.Status, res.Detail, c.status, c.detail)
		}
	}
}

func TestParseRefusesWhatCannotBeJudged(t *testing.T) {
	bad := map[string]string{
		"no family":     strings.Replace(onePack, "family: platform", "family: other", 1),
		"bad severity":  strings.Replace(onePack, "severity: medium", "severity: urgent", 1),
		"no standard":   strings.Replace(onePack, "standard: some-rule", "standard: ''", 1),
		"bad check":     strings.Replace(onePack, "check: thing.count <= params.limit", "check: thing.count <=", 1),
		"unknown field": strings.Replace(onePack, "fix: Have fewer.", "fix: Have fewer.\n    owner: someone", 1),
		"bad stage":     strings.Replace(onePack, "fix: Have fewer.", "fix: Have fewer.\n    stages: [nightly]", 1),
		"bad need":      strings.Replace(onePack, "thing.count: nobody counted", "Thing Count: nobody counted", 1),
		"unknown scope": strings.Replace(onePack, "fix: Have fewer.", "fix: Have fewer.\n    scope: elsewhere", 1),
		"bad scope":     strings.Replace(onePack, "params: { limit: 3 }", "params: { limit: 3 }\nscopes: { live: 'thing.(' }", 1),
		"bad status":    strings.Replace(onePack, "fix: Have fewer.", "fix: Have fewer.\n    status: maybe", 1),
	}
	for name, doc := range bad {
		if _, err := Parse([]byte(doc), "test"); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestLoadLetsAnEstateReplaceAPack(t *testing.T) {
	builtin := fstest.MapFS{"sample.yaml": {Data: []byte(onePack)}}
	dir := t.TempDir()
	mine := strings.Replace(onePack, "version: 1.0.0", "version: 1.1.0", 1)
	if err := os.WriteFile(filepath.Join(dir, "sample.yaml"), []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := Load(builtin, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Packs) != 1 || set.Packs[0].Version != "1.1.0" {
		t.Errorf("the estate's pack replaces the built-in one: %+v", set.Packs)
	}
	if _, err := Load(builtin, "", []string{"missing"}); err == nil {
		t.Error("a pack that does not exist was switched on")
	}
	twin := strings.Replace(onePack, "pack: sample", "pack: twin", 1)
	if err := os.WriteFile(filepath.Join(dir, "twin.yaml"), []byte(twin), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(builtin, dir, nil); err == nil {
		t.Error("one rule in two packs was taken")
	}
}

func TestARuleNamesTheFactsItReads(t *testing.T) {
	doc := strings.Replace(onePack, "params: { limit: 3 }", "params: { limit: 3 }\nscopes: { live: '!owner.gone' }", 1)
	doc = strings.Replace(doc, "fix: Have fewer.", "fix: Have fewer.\n    scope: live", 1)
	doc = strings.Replace(doc, "check: thing.count <= params.limit", "check: thing.count <= params.limit && linked.box.all(b, b.size < 3) && [1].all(x, x > 0)", 1)
	p, err := Parse([]byte(doc), "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.Rules[0].Reads, ","); got != "owner,thing" {
		t.Errorf("reads its facts and its scope's, never params, linked, or a loop variable: %s", got)
	}
}

func TestTheCarriedFactsAnAnswerReadsSetItsAge(t *testing.T) {
	p, err := Parse([]byte(onePack), "test")
	if err != nil {
		t.Fatal(err)
	}
	then := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	prev := estate.New(then)
	_ = prev.Writer("counter").Subject("thing", "a", "a").Set("thing", map[string]any{"retired": false, "count": 9})
	now := estate.New(then.Add(time.Hour))
	_ = now.Writer("other").Subject("thing", "a", "a").Set("unrelated", true)
	now.Carry(prev, map[string]bool{"counter": true}, func(string) time.Duration { return 2 * time.Hour })
	s, _ := now.Find("thing", "a")
	res := p.Rules[0].Judge(Prepare(s, now.Now, nil), map[string]any{"limit": 3.0})
	if res.Status != Fail || !res.Carried || !res.AsOf.Equal(then) {
		t.Errorf("judged on the carried fact, as of its reading: %+v", res)
	}
}

func TestATuningFileTunesTheEstatesPacks(t *testing.T) {
	builtin := fstest.MapFS{"sample.yaml": {Data: []byte(strings.Replace(onePack, "params: { limit: 3 }", "params: { limit: 3 }\nscopes: { live: '!thing.retired' }", 1))}}
	dir := t.TempDir()
	tuning := "params: { sample: { limit: 10 } }\nscopes: { sample: { live: 'true' } }\n"
	if err := os.WriteFile(filepath.Join(dir, TuningFile), []byte(tuning), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := Load(builtin, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Packs) != 1 || set.Params["sample"]["limit"] != 10 || set.Packs[0].Scopes["live"] != "true" {
		t.Errorf("the tuning file is no pack, and tunes the one there is: %+v %+v", set.Params, set.Packs[0].Scopes)
	}
	if err := os.WriteFile(filepath.Join(dir, TuningFile), []byte("params: { other: { x: 1 } }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(builtin, dir, nil); err == nil {
		t.Error("tuning for a pack that is not on was taken")
	}
}

// TestJudgingCostsTheSameHoweverManySubjectsAreLinked builds what a
// subject's expressions see once, so each rule judged over it costs the
// same whether it links to one subject or hundreds.
func TestJudgingCostsTheSameHoweverManySubjectsAreLinked(t *testing.T) {
	p, err := Parse([]byte(onePack), "test")
	if err != nil {
		t.Fatal(err)
	}
	r := p.Rules[0]
	params := map[string]any{"limit": 3.0}
	allocs := func(n int) float64 {
		e := estate.New(time.Now())
		w := e.Writer("src")
		s := w.Subject("thing", "a", "a")
		if err := s.Set("thing", map[string]any{"retired": false, "count": 9}); err != nil {
			t.Fatal(err)
		}
		var linked []*estate.Subject
		for i := range n {
			o := w.Subject("other", strings.Repeat("o", i+1), "")
			if err := o.Set("other", map[string]any{"n": i}); err != nil {
				t.Fatal(err)
			}
			linked = append(linked, o.Subject)
		}
		prepared := Prepare(s.Subject, e.Now, map[string][]*estate.Subject{"other": linked})
		return testing.AllocsPerRun(20, func() { r.Judge(prepared, params) })
	}
	if one, many := allocs(1), allocs(500); many > one {
		t.Errorf("a rule over a subject with 500 linked made %.0f allocations, with 1 linked %.0f", many, one)
	}
}
