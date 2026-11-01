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
// inFlight reports a running marker it found there: another operation's, unless
// that one's caller died, which finishOperationSettling must leave alone if this
// attempt turns out to be blocked.
func BeginOperationSettling(store OperationSettlingStore, service, action string) (inFlight bool, err error) {
	if store == nil || !operation.IsServiceAction(action) {
		return false, nil
	}
	rec, found, readErr := store.OperationSettling(service)
	inFlight = readErr == nil && found && rec.Phase == state.OperationSettlingRunning
	if err := store.SetOperationSettling(service, state.OperationSettlingRunning); err != nil {
		return inFlight, fmt.Errorf("mark operation settling for %s: %w", service, err)
	}
	return inFlight, nil
}

func finishOperationSettling(store OperationSettlingStore, service, action string, result operation.Result, opErr error, activeAfterPostflightFailure, inFlight bool) error {
	if store == nil || !operation.IsServiceAction(action) {
		return nil
	}
	// A blocked attempt never ran, and the marker it found is the operation in
	// its way: clearing that would resume monitoring in the middle of it.
	if inFlight && result.Status == operation.ResultBlocked {
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
