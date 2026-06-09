package operation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"sermo/internal/checks"
	"sermo/internal/locks"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// Manager is the subset of servicemgr.Manager the engine uses. Restart is built
// from Stop+Start (not Manager.Restart) so residual processes can be handled
// between the two phases (section 18).
type Manager interface {
	Start(ctx context.Context, service string) error
	Stop(ctx context.Context, service string) error
	Status(ctx context.Context, service string) (servicemgr.ServiceStatus, error)
	// ResetState reconciles the init's recorded state with reality after a clean
	// stop (systemd reset-failed, OpenRC zap).
	ResetState(ctx context.Context, service string) error
}

// Engine performs the section-18 flow for one service over injected capability
// closures. A nil closure means that capability is absent (e.g. no preflight
// section), which is treated as a pass.
type Engine struct {
	Service string // config service name
	Unit    string // backend unit, passed to Manager
	Backend string

	ConfigError      error
	Manager          Manager
	AcquireLock      func(ttl time.Duration) (release func() error, err error)
	LockTTL          time.Duration
	NamedLocks       func() ([]locks.Lock, error)
	Guard            func(ctx context.Context, action string) (blocked bool, reason string, err error)
	Preflight        func(ctx context.Context) checks.Outcome
	Postflight       func(ctx context.Context) checks.Outcome
	Discover         func() ([]process.Process, error)
	Reaper           process.Reaper
	KillPolicy       process.KillPolicy
	Sleep            func(time.Duration)
	OperationTimeout time.Duration
	Emit             func(Result)
}

type plan struct {
	action     string
	preflight  bool
	stop       bool
	start      bool
	postflight bool
}

// Restart stops the service, clears residuals, starts it again and verifies
// health (section 18).
func (e Engine) Restart(ctx context.Context) Result {
	return e.run(ctx, plan{action: "restart", preflight: true, stop: true, start: true, postflight: true})
}

// Start runs preflight, starts the service and verifies health.
func (e Engine) Start(ctx context.Context) Result {
	return e.run(ctx, plan{action: "start", preflight: true, start: true, postflight: true})
}

// Stop stops the service and clears residuals. Stop runs no preflight or
// postflight (section 19) but still honors locks and guards.
func (e Engine) Stop(ctx context.Context) Result {
	return e.run(ctx, plan{action: "stop", stop: true})
}

func (e Engine) run(ctx context.Context, p plan) (result Result) {
	result = Result{Service: e.Service, Action: p.action, Backend: e.Backend, Status: ResultOK}

	ctx, cancel := boundContext(ctx, e.OperationTimeout)
	defer cancel()

	// Step 2: exactly one event per operation, on every exit path including a
	// failed lock acquisition. Registered first.
	defer func() {
		if e.Emit != nil {
			e.Emit(result)
		}
	}()

	if e.ConfigError != nil {
		result.Status = ResultFailed
		result.Message = "config: " + e.ConfigError.Error()
		return result
	}

	// Step 3: acquire the internal operation lock; fail fast if held.
	release, err := e.AcquireLock(e.LockTTL)
	if err != nil {
		applyLockError(&result, err)
		return result
	}
	// Step 4: release only after a successful acquire.
	defer func() { _ = release() }()

	// Step 5: active named runtime locks block the action automatically.
	if e.NamedLocks != nil {
		active, err := e.NamedLocks()
		if err != nil {
			result.Status = ResultFailed
			result.Message = "lock scan: " + err.Error()
			return result
		}
		if active = activeOnly(active); len(active) > 0 {
			result.Status = ResultBlocked
			result.Message = "blocked by active runtime lock"
			result.Locks = active
			return result
		}
	}

	// Step 6: required preflight (start/restart only).
	if p.preflight && e.Preflight != nil {
		out := e.Preflight(ctx)
		result.Checks = append(result.Checks, out.Results...)
		if !out.OK {
			result.Status = ResultPreflightFailed
			result.Message = "preflight failed"
			return result
		}
	}

	// Step 7: guards.
	if e.Guard != nil {
		blocked, reason, err := e.Guard(ctx, p.action)
		if err != nil {
			result.Status = ResultFailed
			result.Message = "guard: " + err.Error()
			return result
		}
		if blocked {
			result.Status = ResultBlocked
			result.Message = reason
			return result
		}
	}

	// Steps 8-11: stop and residual-process handling.
	if p.stop {
		if err := e.Manager.Stop(ctx, e.Unit); err != nil {
			result.Status = ResultFailed
			if timedOut(ctx) {
				result.Message = "operation timed out during stop"
			} else {
				result.Message = "stop: " + err.Error()
			}
			return result
		}
		if err := wait(ctx, e.Sleep, e.KillPolicy.GracefulTimeout); err != nil {
			result.Status = ResultFailed
			result.Message = "operation timed out during graceful stop wait"
			return result
		}
		remaining, err := e.clearResiduals(ctx)
		if err != nil {
			result.Status = ResultFailed
			result.Message = "process discovery: " + err.Error()
			result.Processes = remaining
			return result
		}
		if len(remaining) > 0 {
			if timedOut(ctx) {
				result.Status = ResultFailed
				result.Message = "operation timed out during residual process handling"
				result.Processes = remaining
				return result
			}
			result.Status = ResultOrphanProcesses
			result.Processes = remaining
			result.Message = fmt.Sprintf("%d residual process(es) remain after stop", len(remaining))
			return result // do NOT start
		}
		// Reconcile the init's recorded state with reality: after a clean stop
		// with no residuals the service is genuinely down, so clear any lingering
		// failed/stuck marker (systemd reset-failed, OpenRC zap) — otherwise the
		// init keeps reporting a state that no longer matches the processes. Best
		// effort: a stop that already succeeded must not fail on reconciliation.
		_ = e.Manager.ResetState(ctx, e.Unit)
	}

	// Steps 12-13: start and verify status.
	if p.start {
		if err := e.Manager.Start(ctx, e.Unit); err != nil {
			result.Status = ResultFailed
			if timedOut(ctx) {
				result.Message = "operation timed out during start"
			} else {
				result.Message = "start: " + err.Error()
			}
			return result
		}
		if st, err := e.Manager.Status(ctx, e.Unit); err == nil && st.Status == servicemgr.StatusFailed {
			result.Status = ResultFailed
			result.Message = "service failed after start"
			return result
		}
	}

	// Step 14: required postflight (start/restart only).
	if p.postflight && e.Postflight != nil {
		out := e.Postflight(ctx)
		result.Checks = append(result.Checks, out.Results...)
		if !out.OK {
			result.Status = ResultPostflightFailed
			result.Message = "postflight failed"
			return result
		}
	}

	result.Message = p.action + " ok"
	return result
}

// clearResiduals discovers residual processes after a stop and applies signal
// escalation (section 22), returning whatever remains.
func (e Engine) clearResiduals(ctx context.Context) ([]process.Process, error) {
	if e.Discover == nil {
		return nil, nil
	}
	var discoverErr error
	discover := func() []process.Process {
		procs, err := e.Discover()
		if err != nil && discoverErr == nil {
			discoverErr = err
		}
		return procs
	}
	residuals := discover()
	if discoverErr != nil {
		return residuals, discoverErr
	}
	if len(residuals) == 0 {
		return nil, nil
	}
	reaper := e.Reaper
	reaper.Rediscover = discover // re-evaluate identity each round
	reaper.Sleep = e.Sleep
	remaining := reaper.Reap(ctx, residuals, e.KillPolicy).Remaining
	if discoverErr != nil {
		return remaining, discoverErr
	}
	return remaining, nil
}

func applyLockError(r *Result, err error) {
	var held *locks.HeldError
	if errors.As(err, &held) {
		r.Status = ResultBlocked
		r.Message = held.Error()
		if held.Lock.Path != "" {
			r.Locks = []locks.Lock{held.Lock}
		}
		return
	}
	r.Status = ResultFailed
	r.Message = "lock: " + err.Error()
}

func activeOnly(in []locks.Lock) []locks.Lock {
	var out []locks.Lock
	for _, l := range in {
		if l.Active() {
			out = append(out, l)
		}
	}
	return out
}
