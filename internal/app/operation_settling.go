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

func manualStartLikeAction(action string) bool {
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

// BeginOperationSettlingForCLI marks a CLI service operation as running.
func BeginOperationSettlingForCLI(store OperationSettlingStore, service, action string) error {
	return beginOperationSettling(store, service, action, state.SourceCLI)
}

func finishOperationSettling(store OperationSettlingStore, service, action, source string, result operation.Result, opErr error) error {
	return finishOperationSettlingWithActive(store, service, action, source, result, opErr, false)
}

func finishOperationSettlingWithActive(store OperationSettlingStore, service, action, source string, result operation.Result, opErr error, activeAfterStart bool) error {
	if store == nil || !isServiceOperationAction(action) {
		return nil
	}
	settleAfter := result.OK() || (activeAfterStart && result.Status == operation.ResultPostflightFailed && manualStartLikeAction(action))
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

// FinishOperationSettlingForCLIWithActive keeps a CLI postflight-failed start
// settling when a backend status check proves that the service is active.
func FinishOperationSettlingForCLIWithActive(store OperationSettlingStore, service, action string, result operation.Result, opErr error, activeAfterStart bool) error {
	return finishOperationSettlingWithActive(store, service, action, state.SourceCLI, result, opErr, activeAfterStart)
}
