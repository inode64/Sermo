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

// operationSettlingUnread is the prior of an attempt that could not read the
// marker it would replace; no stored phase has this value. Such an attempt
// cannot tell no operation from one in progress or a relaunch the worker has
// yet to read, so it neither writes nor clears the marker.
const operationSettlingUnread = "unread"

// BeginOperationSettling marks a service operation as running for its caller.
// prior is the phase of the marker it replaced, "" for none, which this attempt
// hands to finishOperationSettling: what it replaced may not be its to drop.
func BeginOperationSettling(store OperationSettlingStore, service, action string) (prior string, err error) {
	if store == nil || !operation.IsServiceAction(action) {
		return "", nil
	}
	rec, found, readErr := store.OperationSettling(service)
	if readErr != nil {
		return operationSettlingUnread, fmt.Errorf("read operation settling for %s: %w", service, readErr)
	}
	if found {
		prior = rec.Phase
	}
	// A running marker keeps its time: this attempt may yet be refused, and one
	// its caller died with has to reach the age limit. The operation lock covers
	// an attempt that does run past that age.
	if prior == state.OperationSettlingRunning {
		return prior, nil
	}
	if err := store.SetOperationSettling(service, state.OperationSettlingRunning); err != nil {
		return prior, fmt.Errorf("mark operation settling for %s: %w", service, err)
	}
	return prior, nil
}

// finishOperationSettling closes the marker of one attempt. settled is the phase
// an operation that needs an observation cycle leaves it in.
func finishOperationSettling(store OperationSettlingStore, service, action string, result operation.Result, opErr error, activeAfterPostflightFailure bool, prior, settled string) error {
	if store == nil || !operation.IsServiceAction(action) || prior == operationSettlingUnread {
		return nil
	}
	blocked := result.Status == operation.ResultBlocked
	// A blocked attempt never ran, and the marker it found is the operation in
	// its way: clearing that would resume monitoring in the middle of it.
	if blocked && prior == state.OperationSettlingRunning {
		return nil
	}
	// A relaunch the worker has not read yet happened, whatever this attempt did,
	// a stop included. Dropping it would make the worker repeat the restart. An
	// attempt that never ran leaves the last operation's observation due as well.
	if prior == state.OperationSettlingRelaunched || (blocked && prior == state.OperationSettlingSettling) {
		if err := store.SetOperationSettling(service, prior); err != nil {
			return fmt.Errorf("keep pending %s marker for %s: %w", prior, service, err)
		}
		return nil
	}
	settleAfter := result.OK() || activeAfterPostflightFailure
	if opErr == nil && settleAfter && operation.SettlesAfter(action) {
		if err := store.SetOperationSettling(service, settled); err != nil {
			return fmt.Errorf("mark post-operation settling for %s: %w", service, err)
		}
		return nil
	}
	if err := store.ClearOperationSettling(service); err != nil {
		return fmt.Errorf("clear operation settling for %s: %w", service, err)
	}
	return nil
}

// manualSettledPhase is the phase an operator's operation leaves its marker in.
// The worker acknowledges its own remediation at once; an operator's relaunch can
// come from another process, and the marker is how it reaches the worker.
func manualSettledPhase(result operation.Result) string {
	if result.OK() && result.Relaunched {
		return state.OperationSettlingRelaunched
	}
	return state.OperationSettlingSettling
}
