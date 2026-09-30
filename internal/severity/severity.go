// Package severity owns the ordered scale Sermo grades findings, events and
// notifications on: debug < info < warning < error < critical.
//
// One vocabulary serves a check's `severity:`, a `levels:` tier, a rule's
// `severity:`, an event's grade and a notifier's `min_severity`, so an operator
// writes the same word wherever Sermo asks how grave something is. The package
// is a leaf (standard library only) so checks, rules, notify, config and the
// daemon can share it without importing each other.
package severity

import "slices"

// Level is one severity name. The zero value is "unset": nothing declared it,
// and it resolves to Error, so an unconfigured check keeps reporting a failure
// as an outage.
type Level string

// The severity levels, in ascending order of gravity.
const (
	// Debug is diagnostic chatter an operator opts into explicitly.
	Debug Level = "debug"
	// Info is a notice worth recording that needs no action.
	Info Level = "info"
	// Warning is a degradation worth seeing and not worth waking anyone.
	Warning Level = "warning"
	// Error is an outage to act on: the default grade of a failure.
	Error Level = "error"
	// Critical is an outage that needs immediate attention.
	Critical Level = "critical"
)

// levels is the immutable ascending catalog; Rank indexes it.
var levels = [...]Level{Debug, Info, Warning, Error, Critical}

// Summary names the accepted levels for error text.
const Summary = "debug, info, warning, error or critical"

// Parse returns the level s names. Matching is exact: a severity is a config
// keyword, not free text, and "ok" or an empty string is not a level.
func Parse(s string) (Level, bool) {
	level := Level(s)
	return level, level.Valid()
}

// Valid reports whether l names one of the five levels.
func (l Level) Valid() bool { return l.Rank() >= 0 }

// Rank orders the levels from 0 (debug) to 4 (critical). An unset or unknown
// level ranks -1, below every real one.
func (l Level) Rank() int { return slices.Index(levels[:], l) }

// String returns the level name; an unset level is the empty string.
func (l Level) String() string { return string(l) }

// Resolved returns l, or Error when l is unset or unknown.
func (l Level) Resolved() Level { return Resolve(l, "") }

// Advisory reports whether a failure graded l is below an outage: debug, info
// and warning still fire, evaluate their windows and run their actions, but
// stay out of aggregated health and the SLA. An unset level resolves to Error,
// so it is never advisory.
func (l Level) Advisory() bool { return l.Resolved().Rank() < Error.Rank() }

// AtLeast reports whether l, resolved, is at or above minimum. An unset minimum
// is Debug: it filters nothing.
func (l Level) AtLeast(minimum Level) bool {
	return l.Resolved().Rank() >= max(minimum.Rank(), Debug.Rank())
}

// Max returns the graver of a and b. Invalid values are ignored, so the result
// is unset only when neither is a level.
func Max(a, b Level) Level {
	if b.Rank() > a.Rank() {
		return b
	}
	if a.Valid() {
		return a
	}
	return ""
}

// Resolve layers one declaration over its fallback: a valid declaration wins,
// an empty or unusable one inherits, and a chain that declares nothing is an
// Error. A value that never passed validation therefore cannot silently demote
// a failure.
func Resolve(declared, fallback Level) Level {
	if declared.Valid() {
		return declared
	}
	if fallback.Valid() {
		return fallback
	}
	return Error
}
