// Package emission owns the YAML vocabulary for automatic event and notification
// cadence. It is shared by config validation, rule parsing and daemon builders.
package emission

import "sermo/internal/cfgval"

// YAML section and field keys.
const (
	Section   = "emission"
	KeyEvents = "events"
	KeyNotify = "notify"
)

// Mode values.
const (
	ModeOnChange   = "on_change"
	ModeEveryCycle = "every_cycle"
	ModeSummary    = ModeOnChange + ", " + ModeEveryCycle
)

// Policy describes when automatic condition events and notifications are emitted.
// Empty fields mean "inherit from the caller's fallback".
type Policy struct {
	Events string
	Notify string
}

// Default returns the built-in emission policy.
func Default() Policy {
	return Policy{Events: ModeOnChange, Notify: ModeOnChange}
}

// Resolve overlays a typed policy onto fallback. Empty values inherit.
func Resolve(policy, fallback Policy) Policy {
	p := fallback
	if ValidMode(policy.Events) {
		p.Events = policy.Events
	}
	if ValidMode(policy.Notify) {
		p.Notify = policy.Notify
	}
	return p
}

// Merge overlays raw `emission:` values onto fallback. Invalid values are ignored
// at runtime; config validation reports them before the daemon accepts a config.
func Merge(raw any, fallback Policy) Policy {
	p := fallback
	m, ok := raw.(map[string]any)
	if !ok {
		return p
	}
	if mode := cfgval.String(m[KeyEvents]); ValidMode(mode) {
		p.Events = mode
	}
	if mode := cfgval.String(m[KeyNotify]); ValidMode(mode) {
		p.Notify = mode
	}
	return Resolve(p, fallback)
}

// ValidMode reports whether mode is a supported emission mode.
func ValidMode(mode string) bool {
	switch mode {
	case ModeOnChange, ModeEveryCycle:
		return true
	default:
		return false
	}
}

// ShouldRepeat reports whether an emission with mode should happen for this
// firing cycle. rising is true only when the condition entered a firing episode.
func ShouldRepeat(mode string, rising bool) bool {
	return mode == ModeEveryCycle || rising
}
