// Package trend is what history adds to one pass: how each verdict, each
// finding, and each pack moved over the recent window, small enough to ride
// along with the pass to the sinks. history fills it; nothing here reads a
// database.
package trend

import "time"

// Point is one value at one time.
type Point struct {
	At    time.Time `json:"at"`
	Value float64   `json:"value"`
}

// Verdict is how one subject's tier in one pack moved.
type Verdict struct {
	// Points are the tier ranks it changed to, oldest first: the rank held
	// from each point until the next. The first point is at or before the
	// window's start.
	Points []Point `json:"points"`
	// Since is when the tier it holds now began.
	Since time.Time `json:"since"`
	// Best is the highest rank held in the window.
	Best int `json:"best"`
	// Direction is better, worse, or steady over the window.
	Direction string `json:"direction"`
}

// Finding is one finding's past.
type Finding struct {
	// Opened is when it first opened in the window kept, and Since when the
	// span open now began.
	Opened time.Time `json:"opened"`
	Since  time.Time `json:"since"`
	// Reopened is how many times it opened again after closing.
	Reopened int `json:"reopened"`
	// Cause is why the open span began: rule when its pack's version changed
	// in that pass, subject otherwise.
	Cause string `json:"cause,omitempty"`
}

// Pack is one pack's share of conformant subjects, one point per day.
type Pack struct {
	Points []Point `json:"points"`
	// Change7 and Change30 are the change in share, in points, over 7 and
	// 30 days; nil when there is no day that old.
	Change7  *float64 `json:"change_7d,omitempty"`
	Change30 *float64 `json:"change_30d,omitempty"`
}

// Trends are a pass's trends, keyed as the board keys them: a verdict by
// pack/kind/subject, a finding by rule/subject, a pack by its id.
type Trends struct {
	Window   time.Duration      `json:"window"`
	Verdicts map[string]Verdict `json:"verdicts"`
	Findings map[string]Finding `json:"findings"`
	Packs    map[string]Pack    `json:"packs"`
}

// VerdictKey keys a verdict's trend.
func VerdictKey(pack, kind, subject string) string { return pack + "/" + kind + "/" + subject }

// FindingKey keys a finding's trend.
func FindingKey(rule, subject string) string { return rule + "/" + subject }

// DirectionOf compares the first and last rank.
func DirectionOf(points []Point) string {
	if len(points) < 2 {
		return "steady"
	}
	first, last := points[0].Value, points[len(points)-1].Value
	switch {
	case last > first:
		return "better"
	case last < first:
		return "worse"
	}
	return "steady"
}
