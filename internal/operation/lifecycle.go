package operation

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"sermo/internal/config"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

var errProcessIdentity = errors.New("active service has no process matching configured exact exe/user selectors")

// observeProcesses is deliberately separate from best-effort monitoring. Missing
// capability cannot establish absence or turn a failed command into success.
func (e Engine) observeProcesses(ctx context.Context) (process.Observation, error) {
	if err := ctx.Err(); err != nil {
		return process.Observation{}, fmt.Errorf("observe service processes: %w", err)
	}
	if e.ObserveProcesses == nil {
		return process.Observation{}, nil
	}
	out, err := e.ObserveProcesses()
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return out, fmt.Errorf("observe service processes: %w", err)
	}
	return out, nil
}

// reconcileInitState handles both directions of init/process drift before a
// start or restart. ResetState is only issued after confirmed process absence.
func (e Engine) reconcileInitState(ctx context.Context, result *Result) (bool, []process.Process, error) {
	if e.Manager == nil {
		return false, nil, nil
	}
	status, err := e.Manager.Status(ctx, e.Unit)
	if err != nil {
		return false, nil, fmt.Errorf("query init state before %s: %w", result.Action, err)
	}
	if status.Status == servicemgr.StatusActive {
		return e.reconcileActive(ctx, result)
	}
	if status.Status != servicemgr.StatusInactive && status.Status != servicemgr.StatusFailed || e.Discover == nil {
		return false, nil, nil
	}
	outcome, err := e.clearResiduals(ctx, nil)
	if err != nil {
		return false, outcome.remaining, fmt.Errorf("process discovery: %w", err)
	}
	if !outcome.found {
		return false, nil, nil
	}
	if len(outcome.remaining) > 0 {
		return false, outcome.remaining, nil
	}
	if err := e.resetStopped(ctx, false); err != nil {
		return false, nil, err
	}
	return true, nil, nil
}

func (e Engine) reconcileActive(ctx context.Context, result *Result) (bool, []process.Process, error) {
	if e.Lifecycle.ProcessMode != config.ServiceProcessResident {
		return false, nil, nil
	}
	observation, err := e.observeProcesses(ctx)
	if err != nil {
		return false, nil, err
	}
	remaining := nonDelegatedResiduals(observation.Processes)
	if len(remaining) > 0 {
		if observation.IdentityRequired && !observation.Trusted && !observation.ReplacedExecutable {
			return false, remaining, errProcessIdentity
		}
		return false, nil, nil
	}
	if !observation.AbsenceKnown {
		if !observation.IdentityRequired {
			return false, nil, nil
		}
		return false, nil, errors.New("cannot prove absence of the active service process")
	}
	// OpenRC's zap clears the stale started marker. systemd needs a stop job;
	// reset-failed alone cannot deactivate an active unit.
	if e.Backend == string(servicemgr.BackendSystemd) {
		// Restart already owns the following stop phase. Running it here too
		// doubles graceful waits and consumes the same operation deadline.
		if result.Action == actionRestart {
			return true, nil, nil
		}
		stopped, _ := e.stopService(ctx, result)
		if !stopped {
			return false, result.Processes, errors.New(result.Message)
		}
		return true, nil, nil
	}
	if e.Backend != string(servicemgr.BackendOpenRC) {
		return false, nil, errors.New("backend cannot reconcile an active service without a process")
	}
	if err := e.resetStopped(ctx, false); err != nil {
		return false, nil, err
	}
	return true, nil, nil
}

// resetStopped revalidates absence immediately before clearing init bookkeeping,
// then verifies the backend actually converged. No failure is silently discarded.
func (e Engine) resetStopped(ctx context.Context, requireAbsence bool) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("reconcile stopped state: %w", err)
	}
	observation, err := e.observeProcesses(ctx)
	if err != nil {
		return err
	}
	if len(nonDelegatedResiduals(observation.Processes)) > 0 {
		return errors.New("processes appeared before init state reconciliation")
	}
	if (observation.IdentityRequired || requireAbsence) && !observation.AbsenceKnown {
		return errors.New("cannot prove process absence before init state reconciliation")
	}
	if err := e.Manager.ResetState(ctx, e.Unit); err != nil {
		return fmt.Errorf("reset stopped init state: %w", err)
	}
	status, err := e.Manager.Status(ctx, e.Unit)
	if err != nil {
		return fmt.Errorf("verify reconciled init state: %w", err)
	}
	if status.Status != servicemgr.StatusInactive {
		return fmt.Errorf("init state after reconciliation is %s, expected inactive", status.Status)
	}
	return nil
}

func (e Engine) stopService(ctx context.Context, result *Result) (stopped, reactivated bool) {
	before, err := e.observeProcesses(ctx)
	if err != nil {
		result.Status, result.Message = ResultFailed, err.Error()
		return false, false
	}
	if ctx.Err() != nil {
		_ = failPhase(ctx, result, timeoutDuring("stop"), "stop: ", ctx.Err())
		return false, false
	}
	stopErr := e.Manager.Stop(ctx, e.Unit)
	if stopErr != nil {
		result.Warnings = append(result.Warnings, "stop command: "+stopErr.Error())
	}
	if ctx.Err() != nil {
		_ = failPhase(ctx, result, timeoutDuring("stop"), "stop: ", ctx.Err())
		return false, false
	}
	for _, unit := range slices.Backward(e.Lifecycle.AuxiliaryUnits) {
		if err := e.Manager.Stop(ctx, unit); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("also_service stop %s: %v", unit, err))
		}
	}
	if err := e.waitGracefulStop(ctx, before, result.Action); err != nil {
		result.Status, result.Message = ResultFailed, err.Error()
		if ctx.Err() != nil {
			_ = failWait(ctx, result, "graceful stop wait")
		}
		return false, false
	}
	residuals, err := e.clearResiduals(ctx, func(procs []process.Process) (bool, error) {
		return e.systemdReactivated(ctx, result.Action, before, procs)
	})
	if err != nil {
		result.Status, result.Message, result.Processes = ResultFailed, "process discovery: "+err.Error(), residuals.remaining
		return false, false
	}
	if residuals.accepted {
		return true, true
	}
	if len(residuals.remaining) > 0 {
		result.Processes = residuals.remaining
		result.Status, result.Message = ResultOrphanProcesses, residualsRemain(residuals.remaining, "after stop")
		if timedOut(ctx) {
			result.Status, result.Message = ResultFailed, timeoutDuring("residual process handling")
		}
		return false, false
	}
	if err := e.resetStopped(ctx, stopErr != nil); err != nil {
		result.Status, result.Message = ResultFailed, err.Error()
		return false, false
	}
	result.Warnings = append(result.Warnings, e.verifyStopped()...)
	return true, false
}

// waitGracefulStop treats graceful_timeout as a maximum, not a fixed pause after
// a synchronous backend stop. Only fresh, complete evidence may end it early.
func (e Engine) waitGracefulStop(ctx context.Context, before process.Observation, action string) error {
	if e.KillPolicy.GracefulTimeout <= 0 || e.ObserveProcesses == nil {
		if err := process.Wait(ctx, e.Sleep, e.KillPolicy.GracefulTimeout); err != nil {
			return fmt.Errorf("wait for graceful stop: %w", err)
		}
		return nil
	}
	_, err := process.WaitUntil(ctx, e.Sleep, e.KillPolicy.GracefulTimeout, func() (bool, error) {
		if e.Lifecycle.ProcessMode == config.ServiceProcessNone {
			status, err := e.Manager.Status(ctx, e.Unit)
			if err != nil {
				return false, fmt.Errorf("query init state during stop: %w", err)
			}
			return status.Status == servicemgr.StatusInactive, nil
		}
		observation, err := e.observeProcesses(ctx)
		if err != nil {
			return false, err
		}
		remaining := nonDelegatedResiduals(observation.Processes)
		if len(remaining) == 0 {
			return observation.AbsenceKnown, nil
		}
		// A replacement may start while the old generation is still exiting.
		// Keep waiting until that exception is fully verified; clearResiduals
		// will verify it again before deciding whether to skip primary start.
		accepted, _ := e.systemdReactivated(ctx, action, before, remaining)
		return accepted, nil
	})
	if err != nil {
		return fmt.Errorf("wait for graceful stop: %w", err)
	}
	return nil
}

// systemdReactivated accepts only a verified new generation of this same unit.
// A cancelled stop job and an unchanged live daemon are not a restart.
func (e Engine) systemdReactivated(ctx context.Context, action string, before process.Observation, residuals []process.Process) (bool, error) {
	if action != actionRestart || e.Backend != string(servicemgr.BackendSystemd) || len(residuals) == 0 {
		return false, nil
	}
	for _, proc := range residuals {
		if proc.Source != process.SourceBackend {
			return false, nil
		}
	}
	status, err := e.Manager.Status(ctx, e.Unit)
	if err != nil {
		return false, fmt.Errorf("verify systemd reactivation: %w", err)
	}
	if status.Status != servicemgr.StatusActive {
		return false, nil
	}
	after, err := e.observeProcesses(ctx)
	if err != nil {
		return false, err
	}
	if (!before.Trusted && !before.ReplacedExecutable) || !after.Trusted {
		return false, errors.New("cannot verify active systemd process generation after stop")
	}
	old := nonDelegatedResiduals(before.Processes)
	replaced, err := replacedGeneration(old, nonDelegatedResiduals(after.Processes))
	if err != nil || !replaced {
		return false, err
	}
	if err := after.VerifyExited(old); err != nil {
		return false, fmt.Errorf("verify systemd reactivation: %w", err)
	}
	return true, nil
}

func replacedGeneration(before, after []process.Process) (bool, error) {
	if len(before) == 0 || len(after) == 0 {
		return false, errors.New("missing process generation during systemd reactivation")
	}
	retained, replaced := false, false
	for _, proc := range after {
		if proc.Source != process.SourceBackend || proc.StartTicks == 0 {
			return false, errors.New("unverified process generation during systemd reactivation")
		}
		same := false
		for _, old := range before {
			if old.StartTicks == 0 {
				return false, errors.New("unverified process generation before stop")
			}
			if old.PID == proc.PID && old.StartTicks == proc.StartTicks {
				same = true
			}
		}
		retained = retained || same
		replaced = replaced || !same
	}
	if retained && replaced {
		return false, errors.New("old and new systemd process generations coexist after stop")
	}
	return replaced, nil
}
