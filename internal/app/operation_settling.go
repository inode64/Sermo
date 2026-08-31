package app

import (
	"fmt"
	"time"

	"sermo/internal/operation"
	"sermo/internal/state"
)

const operationSettlingMaxAge = 15 * time.Minute

func beginOperationSettling(store OperationSettlingStore, service, action string) error {
	if store == nil || !operation.IsServiceAction(action) {
		return nil
	}
	if err := store.SetOperationSettling(service, state.OperationSettlingRunning); err != nil {
		return fmt.Errorf("mark operation settling for %s: %w", service, err)
	}
	return nil
}

// BeginOperationSettling marks a service operation as running for its caller.
func BeginOperationSettling(store OperationSettlingStore, service, action string) error {
	return beginOperationSettling(store, service, action)
}

func finishOperationSettling(store OperationSettlingStore, service, action string, result operation.Result, opErr error, activeAfterPostflightFailure bool) error {
	if store == nil || !operation.IsServiceAction(action) {
		return nil
	}
	settleAfter := result.OK() || (activeAfterPostflightFailure && result.Status == operation.ResultPostflightFailed && operation.CanRemainActiveAfterPostflightFailure(action))
	if opErr == nil && settleAfter && operation.SettlesAfter(action) {
		if err := store.SetOperationSettling(service, state.OperationSettlingSettling); err != nil {
			return fmt.Errorf("mark post-operation settling for %s: %w", service, err)
		}
		return nil
	}
	if err := store.ClearOperationSettling(service); err != nil {
		return fmt.Errorf("clear operation settling for %s: %w", service, err)
	}
	return nil
}
