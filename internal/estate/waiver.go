package estate

import (
	"fmt"
	"time"
)

// Category is why a finding is set aside.
type Category string

const (
	// Accepted is a risk the owner takes on until a date: debt, counted as
	// such.
	Accepted Category = "accepted"
	// Mitigated is a rule whose intent another control meets, reviewed on a
	// date.
	Mitigated Category = "mitigated"
)

// Waiver is an exception a person granted to one rule on one subject. It
// counts only while it is valid: it names a rule, a subject, a reason, and
// an owner, and it ends. An accepted risk ends on its expiry; a mitigated
// one names the control that stands in for the rule and ends at its review.
type Waiver struct {
	Rule     string   `json:"rule"`
	Subject  string   `json:"subject"`
	Reason   string   `json:"reason"`
	Owner    string   `json:"owner"`
	Category Category `json:"category,omitempty"`
	// GrantedAt is when it was granted, so its length can be held to the
	// estate's limit; when unknown, the length counts from now.
	GrantedAt time.Time `json:"granted_at,omitzero"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	ReviewAt  time.Time `json:"review_at,omitzero"`
	Control   string    `json:"control,omitempty"`
	// Match narrows the waiver to findings whose detail holds it, so a
	// waiver for one accepted change never hides another on the same rule
	// and subject.
	Match string `json:"match,omitempty"`
	// From is the source that read it.
	From string `json:"from,omitempty"`
	// ID is the waiver's own id where it was granted, such as a policy
	// engine's exemption id, so a person can find the grant; "" when the
	// store keeps none.
	ID string `json:"id,omitempty"`
}

// Key is the finding key the waiver covers: rule/subject.
func (w Waiver) Key() string { return FindingKey(w.Rule, w.Subject) }

// Kind is the waiver's category, from what it names when it says none.
func (w Waiver) Kind() Category {
	switch {
	case w.Category != "":
		return w.Category
	case w.Control != "":
		return Mitigated
	default:
		return Accepted
	}
}

// Ends is the day the waiver stops counting.
func (w Waiver) Ends() time.Time {
	if w.Kind() == Mitigated && !w.ReviewAt.IsZero() {
		return w.ReviewAt
	}
	return w.ExpiresAt
}

// Problem says why a waiver does not count, or "" when it is valid.
func (w Waiver) Problem() string {
	switch {
	case w.Rule == "" || w.Subject == "":
		return "it names no rule or no subject"
	case w.Reason == "":
		return "it gives no reason"
	case w.Owner == "":
		return "it names no owner"
	case w.Category != "" && w.Category != Accepted && w.Category != Mitigated:
		return fmt.Sprintf("its category %q is not accepted or mitigated", w.Category)
	case w.Kind() == Mitigated && w.Control == "":
		return "it is mitigated but names no control"
	case w.Ends().IsZero():
		return "it has no end: an accepted risk needs expires_at, a mitigated one review_at"
	}
	return ""
}

// Policy is what an estate allows of a waiver: the longest it may run for
// a rule of each severity, and how early its end is announced.
type Policy struct {
	MaxDays  map[Severity]int `yaml:"max_days" json:"max_days,omitempty"`
	WarnDays int              `yaml:"warn_days" json:"warn_days"`
}

// TooLong says why a waiver runs past the policy's limit for sev, or "".
func (p Policy) TooLong(w Waiver, sev Severity, now time.Time) string {
	max, ok := p.MaxDays[sev]
	if !ok || max <= 0 {
		return ""
	}
	from := w.GrantedAt
	if from.IsZero() {
		from = now
	}
	if days := int(w.Ends().Sub(from).Hours() / 24); days > max {
		return fmt.Sprintf("it runs %d days; a waiver on a %s rule may run %d", days, sev, max)
	}
	return ""
}

// Ending says whether the waiver ends within the policy's warning.
func (p Policy) Ending(w Waiver, now time.Time) bool {
	return p.WarnDays > 0 && w.Ends().Sub(now) <= time.Duration(p.WarnDays)*24*time.Hour
}

// FindingKey is how a finding and the waiver for it are keyed.
func FindingKey(rule, subject string) string { return rule + "/" + subject }
