package app

import (
	"fmt"
	"time"

	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/state"
)

const operationSettlingMaxAge = 15 * time.Minute

func beginOperationSettling(store OperationSettlingStore, service, action, source string) error {
	if store == nil || !rules.ActionType(action).IsOperation() {
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
	if store == nil || !rules.ActionType(action).IsOperation() {
		return nil
	}
	settleAfter := result.OK() || (activeAfterPostflightFailure && result.Status == operation.ResultPostflightFailed && rules.ActionType(action).CanRemainActiveAfterPostflightFailure())
	if opErr == nil && settleAfter && rules.ActionType(action).SettlesAfter() {
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
