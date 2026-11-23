package estate

import (
	"strings"
	"testing"
	"time"
)

func TestCarryKeepsWhatASourceCouldNotReadWhileItIsYoung(t *testing.T) {
	then := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	prev := New(then)
	_ = prev.Writer("forge").Subject("repository", "a", "a").Set("forge", map[string]any{"files": []any{"AGENTS.md"}})
	_ = prev.Writer("forge").Subject("repository", "b", "b").Set("forge", map[string]any{"files": []any{}})
	_ = prev.Writer("scanner").Subject("image", "only-scanner", "img").Set("scan", map[string]any{"kev": 1})
	_ = prev.Writer("scanner").Subject("image", "old", "old").Set("scan", map[string]any{"kev": 0})
	prev.Writer("cluster").Subject("workload", "w", "w").Link("image", "only-scanner")
	_ = prev.Writer("cluster").Subject("workload", "gone", "gone").Set("pods", map[string]any{})

	now := New(then.Add(time.Hour))
	cat := now.Writer("catalog")
	cat.Subject("repository", "a", "a").Miss("forge", "")
	f := now.Writer("forge")
	f.Subject("repository", "a", "a").Miss("forge", "the tree could not be read")
	_ = f.Subject("repository", "b", "b").Set("forge", map[string]any{"files": []any{"README.md"}})
	now.Writer("cluster").Subject("workload", "w", "w")
	// The scanner failed outright; the cluster ran and "gone" is no more.
	old := map[string]time.Duration{"forge": 2 * time.Hour, "scanner": 90 * time.Minute, "cluster": 2 * time.Hour}
	prev.Writer("scanner").Subject("image", "old", "old").Facts["scan"] = Fact{Value: map[string]any{"kev": 0}, Source: "scanner", ObservedAt: then.Add(-time.Hour)}
	now.Carry(prev, map[string]bool{"scanner": true}, func(s string) time.Duration { return old[s] })

	a, _ := now.Find("repository", "a")
	if fa := a.Facts["forge"]; !fa.Carried || !fa.ObservedAt.Equal(then) || fa.Source != "forge" || len(a.Missed) != 0 {
		t.Errorf("a missed fact is carried from its last reading: %+v missed %+v", fa, a.Missed)
	}
	b, _ := now.Find("repository", "b")
	if fb := b.Facts["forge"]; fb.Carried || len(fb.Value.(map[string]any)["files"].([]any)) != 1 {
		t.Errorf("a fact read this pass is never replaced: %+v", fb)
	}
	img, ok := now.Find("image", "only-scanner")
	if !ok || !img.Facts["scan"].Carried || img.Title != "img" {
		t.Errorf("a subject only a failed source brought comes back with its facts: %+v", img)
	}
	if _, ok := now.Find("image", "old"); ok {
		t.Error("a subject whose only facts are past their max age is gone")
	}
	if _, ok := now.Find("workload", "gone"); ok {
		t.Error("a subject a source that ran no longer brings is gone, never carried")
	}
	w, _ := now.Find("workload", "w")
	if len(w.Links) != 0 {
		t.Errorf("links come from what is read, not carried onto a subject that is still read: %+v", w.Links)
	}
}

func TestCarryNamesAFactTooOldToKeep(t *testing.T) {
	then := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	prev := New(then)
	_ = prev.Writer("scanner").Subject("image", "x", "x").Set("scan", map[string]any{"kev": 1})
	now := New(then.Add(48 * time.Hour))
	_ = now.Writer("catalog").Subject("image", "x", "x").Set("image", map[string]any{})
	now.Carry(prev, map[string]bool{"scanner": true}, func(string) time.Duration { return 24 * time.Hour })
	x, _ := now.Find("image", "x")
	if _, has := x.Facts["scan"]; has || !strings.Contains(x.Missed["scan"].Reason, "48h0m0s old") {
		t.Errorf("past its max age the fact is missing, with why: %+v", x.Missed)
	}
}

func TestAWaiverEndsAndSaysWhyItDoesNotCount(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	ok := Waiver{Rule: "r.x", Subject: "s", Reason: "why", Owner: "o", ExpiresAt: now.AddDate(0, 1, 0)}
	mitigated := Waiver{Rule: "r.x", Subject: "s", Reason: "why", Owner: "o", Control: "egress blocked", ReviewAt: now.AddDate(0, 6, 0)}
	cases := map[string]Waiver{
		"no owner":            {Rule: "r.x", Subject: "s", Reason: "why", ExpiresAt: now},
		"no end":              {Rule: "r.x", Subject: "s", Reason: "why", Owner: "o"},
		"mitigated, no ctrl":  {Rule: "r.x", Subject: "s", Reason: "why", Owner: "o", Category: Mitigated, ReviewAt: now},
		"unknown category":    {Rule: "r.x", Subject: "s", Reason: "why", Owner: "o", Category: "forever", ExpiresAt: now},
		"accepted, review at": {Rule: "r.x", Subject: "s", Reason: "why", Owner: "o", Category: Accepted, ReviewAt: now},
	}
	for name, w := range cases {
		if w.Problem() == "" {
			t.Errorf("%s: counted", name)
		}
	}
	if ok.Problem() != "" || ok.Kind() != Accepted || mitigated.Problem() != "" || mitigated.Kind() != Mitigated || !mitigated.Ends().Equal(now.AddDate(0, 6, 0)) {
		t.Errorf("an accepted risk ends on its expiry, a mitigated one at its review: %v %v", ok.Problem(), mitigated.Problem())
	}
	p := Policy{MaxDays: map[Severity]int{Critical: 30}, WarnDays: 14}
	if p.TooLong(ok, Critical, now) != "" || p.TooLong(mitigated, Critical, now) == "" || p.TooLong(mitigated, High, now) != "" {
		t.Error("a waiver may run as long as the policy allows its rule's severity")
	}
	granted := ok
	granted.GrantedAt = now.AddDate(0, -2, 0)
	if p.TooLong(granted, Critical, now) == "" {
		t.Error("the length counts from when it was granted, so renewing by editing the end does not reset it")
	}
	if p.Ending(ok, now) || !p.Ending(ok, now.AddDate(0, 0, 20)) {
		t.Error("ending within the warning")
	}
}

func TestAGapIsAMissOnEverySubjectOfItsKindAndItsFactsAreCarried(t *testing.T) {
	then := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	prev := New(then)
	_ = prev.Writer("catalog").Subject("tool", "kept", "kept").Set("tool", map[string]any{"state": "ok"})
	_ = prev.Writer("catalog").Subject("tool", "stale", "stale").Set("tool", map[string]any{"state": "ok"})
	prev.Writer("catalog").Subject("tool", "stale", "stale").Facts["tool"] = Fact{Value: map[string]any{"state": "ok"}, Source: "catalog", ObservedAt: then.Add(-48 * time.Hour)}
	_ = prev.Writer("catalog").Subject("app", "a", "a").Set("app", map[string]any{})

	now := New(then.Add(time.Hour))
	cat := now.Writer("catalog")
	_ = cat.Subject("app", "a", "a").Set("app", map[string]any{})
	// Another source still brings one tool; the catalog's list of them is gone.
	_ = now.Writer("cluster").Subject("tool", "seen", "seen").Set("pods", map[string]any{})
	cat.Gap("tool", "tool", "the hub has no list tools")
	cat.Gap("tool", "tool", "the hub has no list tools")
	cat.Gap("tool", "tool", "the list more_tools could not be read")
	now.Carry(prev, nil, func(string) time.Duration { return 24 * time.Hour })

	gaps := now.Gaps()
	if len(gaps) != 1 || gaps[0] != (Gap{Source: "catalog", Kind: "tool", Fact: "tool", Reason: "the hub has no list tools; the list more_tools could not be read"}) {
		t.Errorf("one gap per kind and fact, with each reason once: %+v", gaps)
	}
	seen, _ := now.Find("tool", "seen")
	if m := seen.Missed["tool"]; m.Source != "catalog" || !strings.Contains(m.Reason, "the hub has no list tools") {
		t.Errorf("a subject of the kind misses the fact, with the gap's reason: %+v", seen.Missed)
	}
	kept, ok := now.Find("tool", "kept")
	if !ok || !kept.Facts["tool"].Carried || len(kept.Missed) != 0 {
		t.Errorf("a subject only the gap brought comes back with its young fact: %+v", kept)
	}
	if _, ok := now.Find("tool", "stale"); ok {
		t.Error("a fact past its max age is not carried")
	}
	a, _ := now.Find("app", "a")
	if a.Facts["app"].Carried || len(a.Missed) != 0 {
		t.Errorf("another kind is untouched: %+v", a)
	}
}

func TestAGapOnTheFirstPassIsAMissWithNothingToCarry(t *testing.T) {
	now := New(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC))
	_ = now.Writer("cluster").Subject("tool", "seen", "seen").Set("pods", map[string]any{})
	now.Writer("catalog").Gap("tool", "tool", "the hub has no list tools")
	now.Carry(nil, nil, func(string) time.Duration { return time.Hour })
	seen, _ := now.Find("tool", "seen")
	if seen.Missed["tool"].Reason != "the hub has no list tools" {
		t.Errorf("no earlier pass is needed for the miss: %+v", seen.Missed)
	}
}
