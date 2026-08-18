package operation

import "sermo/internal/rules"

// IsServiceAction reports whether action changes a service through the operation
// engine. Repair is intentionally included here but not in rules: it is a
// manual-only recovery action and can never be emitted by remediation rules.
func IsServiceAction(action string) bool {
	return rules.ActionType(action).IsOperation() || action == ActionRepair
}

// CascadesAlsoApply reports whether an action applies to a service's
// also_apply targets. Only lifecycle actions that change the unit's running
// state cascade; reload, resume and manual repair always affect just the named
// service.
func CascadesAlsoApply(action string) bool {
	switch action {
	case actionStart, actionStop, actionRestart:
		return true
	default:
		return false
	}
}

// SettlesAfter reports whether an action needs an observation settling window
// after a successful operation.
func SettlesAfter(action string) bool {
	return rules.ActionType(action).SettlesAfter() || action == ActionRepair
}

// CanRemainActiveAfterPostflightFailure reports whether a failed postflight can
// still leave the service running and needing observation.
func CanRemainActiveAfterPostflightFailure(action string) bool {
	return rules.ActionType(action).CanRemainActiveAfterPostflightFailure() || action == ActionRepair
}
