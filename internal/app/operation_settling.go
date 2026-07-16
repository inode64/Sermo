package app

import (
	"fmt"
	"time"

	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/state"
)

const operationSettlingMaxAge = 15 * time.Minute

func operationActionSettlesAfter(action string) bool {
	switch rules.ActionType(action) {
	case rules.ActionStart, rules.ActionRestart, rules.ActionReload, rules.ActionResume:
		return true
	default:
		return false
	}
}

// ManualActionCanRemainActiveAfterPostflightFailure reports whether a failed
// postflight can still leave the service running and needing observation.
func ManualActionCanRemainActiveAfterPostflightFailure(action string) bool {
	switch rules.ActionType(action) {
	case rules.ActionStart, rules.ActionRestart, rules.ActionResume:
		return true
	default:
		return false
	}
}

func beginOperationSettling(store OperationSettlingStore, service, action, source string) error {
	if store == nil || !isServiceOperationAction(action) {
		return nil
	}
	if err := store.SetOperationSettling(service, action, state.OperationSettlingRunning, source); err != nil {
		return fmt.Errorf("mark operation settling for %s: %w", service, err)
	}
	return nil
}

// BeginOperationSettling marks a service operation as running for its caller.
func BeginOperationSettling(store OperationSettlingStore, service, action, source string) error {
	return beginOperationSettling(store, service, action, source)
}

func finishOperationSettling(store OperationSettlingStore, service, action, source string, result operation.Result, opErr error, activeAfterPostflightFailure bool) error {
	if store == nil || !isServiceOperationAction(action) {
		return nil
	}
	settleAfter := result.OK() || (activeAfterPostflightFailure && result.Status == operation.ResultPostflightFailed && ManualActionCanRemainActiveAfterPostflightFailure(action))
	if opErr == nil && settleAfter && operationActionSettlesAfter(action) {
		if err := store.SetOperationSettling(service, action, state.OperationSettlingSettling, source); err != nil {
			return fmt.Errorf("mark post-operation settling for %s: %w", service, err)
		}
		return nil
	}
	if err := store.ClearOperationSettling(service); err != nil {
		return fmt.Errorf("clear operation settling for %s: %w", service, err)
	}
	return nil
}
