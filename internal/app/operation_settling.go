package app

import (
	"fmt"
	"time"

	"sermo/internal/operation"
	"sermo/internal/state"
)

const (
	// operationSettlingMinAge is how long an operation-settling marker is
	// trusted at least, so one left behind by a crashed caller stops
	// suppressing the service's monitoring after a bounded time.
	operationSettlingMinAge = 15 * time.Minute
	// operationSettlingAgeMargin covers the lock, preflight and bookkeeping
	// around an operation that runs right up to its deadline.
	operationSettlingAgeMargin = time.Minute
)

// operationSettlingMaxAge returns how old a service's marker may get before it
// is treated as abandoned: never less than the service's resolved operation
// deadline plus a margin, because stop_policy can raise that deadline past
// the floor and a slow deliberate stop must stay suppressed until it ends.
func operationSettlingMaxAge(operationTimeout time.Duration) time.Duration {
	return max(operationSettlingMinAge, operationTimeout+operationSettlingAgeMargin)
}

// BeginOperationSettling marks a service operation as running for its caller.
func BeginOperationSettling(store OperationSettlingStore, service, action string) error {
	if store == nil || !operation.IsServiceAction(action) {
		return nil
	}
	if err := store.SetOperationSettling(service, state.OperationSettlingRunning); err != nil {
		return fmt.Errorf("mark operation settling for %s: %w", service, err)
	}
	return nil
}

func finishOperationSettling(store OperationSettlingStore, service, action string, result operation.Result, opErr error, activeAfterPostflightFailure bool) error {
	if store == nil || !operation.IsServiceAction(action) {
		return nil
	}
	settleAfter := result.OK() || activeAfterPostflightFailure
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
