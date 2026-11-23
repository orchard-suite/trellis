package board

import (
	"sort"

	"github.com/orchard-suite/trellis/internal/estate"
	"github.com/orchard-suite/trellis/internal/judge"
)

// EventType is what happened to a finding or a waiver between two passes.
type EventType string

const (
	// Opened: a finding counts that did not before.
	Opened EventType = "opened"
	// Resolved: a finding that counted now passes or no longer applies.
	Resolved EventType = "resolved"
	// Excused: a finding that counted is now covered by a waiver.
	Excused EventType = "waived"
	// Overdue: an open finding passed its deadline.
	Overdue EventType = "overdue"
	// Dark: a finding that counted now has no data, so nobody can say
	// whether it was fixed.
	Dark EventType = "unknown"
	// Ending: a waiver entered its warning before it ends.
	Ending EventType = "waiver_ending"
	// Ended: a waiver ended, so what it covered counts again.
	Ended EventType = "waiver_ended"
)

// Event is one change, with the finding or waiver it is about and its
// owner, so a sink can route it.
type Event struct {
	Type    EventType      `json:"type"`
	Owner   string         `json:"owner"`
	Finding *judge.Finding `json:"finding,omitempty"`
	Waiver  *estate.Waiver `json:"waiver,omitempty"`
}

// Diff is what changed from one evaluation to the next.
func Diff(prev, now judge.Evaluation) []Event {
	before := map[string]judge.Finding{}
	for _, f := range prev.Findings {
		before[f.Key()] = f
	}
	after := map[string]judge.Finding{}
	for _, f := range now.Findings {
		after[f.Key()] = f
	}
	unchecked := map[string]bool{}
	for _, sv := range now.Subjects {
		for _, u := range sv.Unchecked {
			unchecked[estate.FindingKey(u.Rule, sv.Key)] = true
		}
	}
	var out []Event
	add := func(t EventType, f judge.Finding) {
		out = append(out, Event{Type: t, Owner: f.Owner, Finding: &f})
	}
	for key, f := range after {
		was, had := before[key]
		switch {
		case f.State == judge.Open && (!had || was.State != judge.Open):
			add(Opened, f)
		case f.State == judge.Waived && had && was.State == judge.Open:
			add(Excused, f)
		}
		if f.State == judge.Open && f.Overdue && !(had && was.Overdue) {
			add(Overdue, f)
		}
	}
	for key, f := range before {
		if f.State != judge.Open {
			continue
		}
		if _, still := after[key]; still {
			continue
		}
		if unchecked[key] {
			add(Dark, f)
		} else {
			add(Resolved, f)
		}
	}
	wasEnding := map[string]judge.WaiverStatus{}
	for _, w := range prev.Waivers {
		wasEnding[waiverID(w.Waiver)] = w.Status
	}
	for _, w := range now.Waivers {
		was := wasEnding[waiverID(w.Waiver)]
		wv := w.Waiver
		switch {
		case w.Status == judge.WaiverEnding && was == judge.WaiverActive:
			out = append(out, Event{Type: Ending, Owner: wv.Owner, Waiver: &wv})
		case w.Status == judge.WaiverEnded && (was == judge.WaiverActive || was == judge.WaiverEnding):
			out = append(out, Event{Type: Ended, Owner: wv.Owner, Waiver: &wv})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return eventKey(out[i]) < eventKey(out[j])
	})
	return out
}

func waiverID(w estate.Waiver) string { return w.Key() + "\x00" + w.Match + "\x00" + w.From }

func eventKey(e Event) string {
	if e.Finding != nil {
		return e.Finding.Key()
	}
	return waiverID(*e.Waiver)
}
