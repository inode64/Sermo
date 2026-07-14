package operation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/locks"
	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
)

// Operation action names, derived from the canonical rule action vocabulary so
// the dispatch cannot drift from the actions rules emit.
const (
	actionStart   = string(rules.ActionStart)
	actionStop    = string(rules.ActionStop)
	actionRestart = string(rules.ActionRestart)
	actionReload  = string(rules.ActionReload)
	actionResume  = string(rules.ActionResume)
)

// Manager is the subset of servicemgr.Manager the engine uses. Restart is built
// from Stop+Start (not Manager.Restart) so residual processes can be handled
// between the two phases.
type Manager interface {
	Start(ctx context.Context, service string) error
	Stop(ctx context.Context, service string) error
	Reload(ctx context.Context, service string) error
	// SupportsReload reports whether the init backend can reload the unit in place,
	// so the reload step can fall back to a native signal/command when it cannot.
	SupportsReload(ctx context.Context, service string) (bool, error)
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
	// AlsoUnits are auxiliary init units (from `also_service`) acted on alongside
	// the primary in wrap order: started before the primary (strict — a failure
	// aborts before the primary starts) and stopped after it (best-effort). Empty
	// for most services.
	AlsoUnits []string
	// StopArtifacts are stopped-state invariants verified after a clean stop.
	StopArtifacts StopArtifacts

	ConfigError error
	Manager     Manager
	AcquireLock func(ttl time.Duration) (release func() error, err error)
	LockTTL     time.Duration
	NamedLocks  func() ([]locks.Lock, error)
	Guard       func(ctx context.Context, action string) (blocked bool, reason string, err error)
	Preflight   func(ctx context.Context) checks.Outcome
	Postflight  func(ctx context.Context) checks.Outcome
	// RestartIdentity verifies that an active service still has at least one
	// trusted process identity before a restart stops it. Nil means no extra
	// identity gate is available.
	RestartIdentity func(ctx context.Context) (ok bool, reason string, err error)
	// ReloadFunc reloads the service's config in place. When nil the engine falls
	// back to Manager.Reload (the backend per-unit reload). A `reload:` block
	// builds a richer closure: a native signal/command that either overrides the
	// backend reload (`when: always`) or stands in for it when the init has no
	// reload of its own (`when: auto`).
	ReloadFunc       func(ctx context.Context) error
	ResumeFunc       func(ctx context.Context) error
	Discover         func() ([]process.Process, error)
	Reaper           process.Reaper
	KillPolicy       process.KillPolicy
	Sleep            func(time.Duration)
	OperationTimeout time.Duration
	Emit             func(Result)
}

// StopArtifacts are the stopped-state invariants verified after a clean stop: the
// pidfile path(s) and the files/globs that must no longer exist. A still-present
// artifact is always a warning folded into the result message, not a failure.
// CleanEnabled is the master opt-in (`clean_after_stop`) for all active deletion:
// when set, lingering pidfile/files artifacts are deleted and the Clean list is
// removed; when unset nothing is deleted (verify-and-warn only). Clean lists the
// `clean_on_stop` files and directories deleted when CleanEnabled is set
// (recursive for directory trees).
type StopArtifacts struct {
	PidfilePaths []string
	Files        []string
	CleanEnabled bool
	Clean        []CleanPath
}

// CleanPath is one `clean_on_stop` entry: a path (or glob, when not recursive)
// deleted after a clean stop. It is an alias for config.CleanPath so the resolved
// form flows straight into the engine without a parallel struct or a copy step.
type CleanPath = config.CleanPath

type plan struct {
	action     string
	preflight  bool
	stop       bool
	start      bool
	resume     bool
	reload     bool
	postflight bool
}

// Restart stops the service, clears residuals, starts it again and verifies
// health.
func (e Engine) Restart(ctx context.Context) Result {
	return e.run(ctx, plan{action: actionRestart, preflight: true, stop: true, start: true, postflight: true})
}

// Start runs preflight, starts the service and verifies health.
func (e Engine) Start(ctx context.Context) Result {
	return e.run(ctx, plan{action: actionStart, preflight: true, start: true, postflight: true})
}

// Stop stops the service and clears residuals. Stop runs no preflight or
// postflight but still honors locks and guards.
func (e Engine) Stop(ctx context.Context) Result {
	return e.run(ctx, plan{action: actionStop, stop: true})
}

// Reload runs preflight (the config check), asks the init system to reload the
// service's configuration in place (no stop/start), and verifies health. It is
// the non-disruptive remediation for daemons that reload rather than restart.
func (e Engine) Reload(ctx context.Context) Result {
	return e.run(ctx, plan{action: actionReload, preflight: true, reload: true, postflight: true})
}

// Resume runs preflight, resumes a paused service and verifies health.
func (e Engine) Resume(ctx context.Context) Result {
	return e.run(ctx, plan{action: actionResume, preflight: true, resume: true, postflight: true})
}

// Do dispatches one action name to the matching operation, returning its Result.
// It is the single action-dispatch point shared by the CLI, the daemon worker and
// the web UI; an unrecognized action yields a failed Result without running
// anything.
func (e Engine) Do(ctx context.Context, action string) Result {
	switch action {
	case actionStart:
		return e.Start(ctx)
	case actionStop:
		return e.Stop(ctx)
	case actionRestart:
		return e.Restart(ctx)
	case actionReload:
		return e.Reload(ctx)
	case actionResume:
		return e.Resume(ctx)
	default:
		return Result{Service: e.Service, Action: action, Status: ResultFailed, Message: "unknown action " + action}
	}
}

func (e Engine) run(ctx context.Context, p plan) (result Result) {
	result = Result{Service: e.Service, Action: p.action, Backend: e.Backend, Status: ResultOK}

	// Best-effort failures stopping also_service units; folded into the final
	// success message (a successful stop is not failed by an auxiliary unit).
	var alsoStopErrs []string
	// Stale stopped-state artifacts (pidfile/files still present after a clean
	// stop); folded into the success message as a warning, like alsoStopErrs.
	var staleWarn []string

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

	if !e.checkNamedLocks(&result) || !e.runPreflight(ctx, p, &result) || !e.checkGuards(ctx, p, &result) {
		return result
	}

	var stopped bool
	if p.stop {
		alsoStopErrs, staleWarn, stopped = e.stopService(ctx, &result)
		if !stopped {
			return result
		}
	}

	if p.start && !e.startService(ctx, &result) {
		return result
	}

	if p.resume && !e.resumeService(ctx, &result) {
		return result
	}
	if p.reload && !e.reloadService(ctx, &result) {
		return result
	}
	if !e.runPostflight(ctx, p, &result) {
		return result
	}

	result.Message = p.action + " ok"
	if len(alsoStopErrs) > 0 {
		result.Message += " (also_service: " + strings.Join(alsoStopErrs, "; ") + ")"
	}
	if len(staleWarn) > 0 {
		result.Message += " (stale: " + strings.Join(staleWarn, "; ") + ")"
	}
	return result
}

func (e Engine) resumeService(ctx context.Context, result *Result) bool {
	if e.ResumeFunc == nil {
		result.Status, result.Message = ResultFailed, "resume: operation unsupported by backend"
		return false
	}
	if err := e.ResumeFunc(ctx); err != nil {
		result.Status = ResultFailed
		if timedOut(ctx) {
			result.Message = "operation timed out during resume"
		} else {
			result.Message = "resume: " + err.Error()
		}
		return false
	}
	return e.ensureServiceHealthy(ctx, result, "resume")
}

func (e Engine) reloadService(ctx context.Context, result *Result) bool {
	reload := e.ReloadFunc
	if reload == nil {
		reload = func(ctx context.Context) error { return e.Manager.Reload(ctx, e.Unit) }
	}
	if err := reload(ctx); err != nil {
		result.Status = ResultFailed
		if timedOut(ctx) {
			result.Message = "operation timed out during reload"
		} else {
			result.Message = "reload: " + err.Error()
		}
		return false
	}
	return e.ensureServiceHealthy(ctx, result, "reload")
}

func (e Engine) ensureServiceHealthy(ctx context.Context, result *Result, action string) bool {
	if status, err := e.Manager.Status(ctx, e.Unit); err == nil && status.Status == servicemgr.StatusFailed {
		result.Status, result.Message = ResultFailed, "service failed after "+action
		return false
	}
	return true
}

func (e Engine) runPostflight(ctx context.Context, p plan, result *Result) bool {
	if !p.postflight || e.Postflight == nil {
		return true
	}
	out := e.Postflight(ctx)
	result.Checks = append(result.Checks, out.Results...)
	if out.OK {
		return true
	}
	result.Status, result.Message = ResultPostflightFailed, "postflight failed"
	return false
}

func (e Engine) startService(ctx context.Context, result *Result) bool {
	for _, unit := range e.AlsoUnits {
		if err := e.Manager.Start(ctx, unit); err != nil {
			result.Status = ResultFailed
			if timedOut(ctx) {
				result.Message = "operation timed out starting also_service " + unit
			} else {
				result.Message = "start " + unit + ": " + err.Error()
			}
			return false
		}
	}
	if err := e.Manager.Start(ctx, e.Unit); err != nil {
		result.Status = ResultFailed
		if timedOut(ctx) {
			result.Message = "operation timed out during start"
		} else {
			result.Message = "start: " + err.Error()
		}
		return false
	}
	if status, err := e.Manager.Status(ctx, e.Unit); err == nil && status.Status == servicemgr.StatusFailed {
		result.Status, result.Message = ResultFailed, "service failed after start"
		return false
	}
	return true
}

func (e Engine) stopService(ctx context.Context, result *Result) (alsoStopErrs, staleWarn []string, stopped bool) {
	if err := e.Manager.Stop(ctx, e.Unit); err != nil {
		result.Status = ResultFailed
		if timedOut(ctx) {
			result.Message = "operation timed out during stop"
		} else {
			result.Message = "stop: " + err.Error()
		}
		return nil, nil, false
	}
	for _, unit := range slices.Backward(e.AlsoUnits) {
		if err := e.Manager.Stop(ctx, unit); err != nil {
			alsoStopErrs = append(alsoStopErrs, fmt.Sprintf("stop %s: %v", unit, err))
		}
	}
	if err := process.Wait(ctx, e.Sleep, e.KillPolicy.GracefulTimeout); err != nil {
		result.Status, result.Message = ResultFailed, "operation timed out during graceful stop wait"
		return alsoStopErrs, nil, false
	}
	remaining, err := e.clearResiduals(ctx)
	if err != nil {
		result.Status, result.Message, result.Processes = ResultFailed, "process discovery: "+err.Error(), remaining
		return alsoStopErrs, nil, false
	}
	if len(remaining) > 0 {
		result.Processes = remaining
		if timedOut(ctx) {
			result.Status, result.Message = ResultFailed, "operation timed out during residual process handling"
		} else {
			result.Status, result.Message = ResultOrphanProcesses, fmt.Sprintf("%d residual process(es) remain after stop", len(remaining))
		}
		return alsoStopErrs, nil, false
	}
	_ = e.Manager.ResetState(ctx, e.Unit)
	return alsoStopErrs, e.verifyStopped(), true
}

func (e Engine) checkNamedLocks(result *Result) bool {
	if e.NamedLocks == nil {
		return true
	}
	active, err := e.NamedLocks()
	if err != nil {
		result.Status = ResultFailed
		result.Message = "lock scan: " + err.Error()
		return false
	}
	if active = activeOnly(active); len(active) > 0 {
		result.Status = ResultBlocked
		result.Message = "blocked by active runtime lock"
		result.Locks = active
		return false
	}
	return true
}

func (e Engine) runPreflight(ctx context.Context, p plan, result *Result) bool {
	if !p.preflight || e.Preflight == nil {
		return true
	}
	out := e.Preflight(ctx)
	result.Checks = append(result.Checks, out.Results...)
	if out.OK {
		return true
	}
	result.Status = ResultPreflightFailed
	result.Message = "preflight failed"
	return false
}

func (e Engine) checkGuards(ctx context.Context, p plan, result *Result) bool {
	if e.Guard != nil {
		blocked, reason, err := e.Guard(ctx, p.action)
		if err != nil {
			result.Status = ResultFailed
			result.Message = "guard: " + err.Error()
			return false
		}
		if blocked {
			result.Status = ResultBlocked
			result.Message = reason
			return false
		}
	}
	if !p.stop || !p.start || e.RestartIdentity == nil {
		return true
	}
	ok, reason, err := e.RestartIdentity(ctx)
	if err != nil {
		result.Status = ResultFailed
		result.Message = "restart identity: " + err.Error()
		return false
	}
	if !ok {
		result.Status = ResultBlocked
		result.Message = reason
		return false
	}
	return true
}

// verifyStopped checks the stopped-state invariants after a clean stop: every
// declared pidfile path and every files_absent glob must no longer exist. With
// StopArtifacts.CleanEnabled set (`clean_after_stop`), a lingering file is deleted
// and only re-flagged if the delete fails, and the clean_on_stop list is deleted
// too; otherwise nothing is deleted and a still-present artifact is warned about.
// Returns one warning per still-present (or unremovable) artifact, for folding
// into the result message.
func (e Engine) verifyStopped() []string {
	warns := e.stoppedArtifactWarnings()
	if !e.StopArtifacts.CleanEnabled {
		return warns
	}
	return append(warns, e.cleanOnStopWarnings()...)
}

func (e Engine) stoppedArtifactWarnings() []string {
	var warns []string
	for _, p := range e.StopArtifacts.PidfilePaths {
		warns = append(warns, e.stoppedPathWarnings(p, false)...)
	}
	for _, g := range e.StopArtifacts.Files {
		warns = append(warns, e.stoppedPathWarnings(g, true)...)
	}
	return warns
}

func (e Engine) stoppedPathWarnings(path string, isGlob bool) []string {
	matches, warns := stoppedPathMatches(path, isGlob)
	for _, match := range matches {
		if e.StopArtifacts.CleanEnabled {
			if err := os.Remove(match); err != nil {
				warns = append(warns, fmt.Sprintf("could not remove stale %s: %v", match, err))
			}
			continue
		}
		warns = append(warns, "stale "+match)
	}
	return warns
}

func stoppedPathMatches(path string, isGlob bool) ([]string, []string) {
	if isGlob {
		matches, err := filepath.Glob(path)
		if err != nil {
			return nil, []string{fmt.Sprintf("bad files_absent pattern %q: %v", path, err)}
		}
		return matches, nil
	}
	if _, err := os.Stat(path); err == nil {
		return []string{path}, nil
	}
	return nil, nil
}

func (e Engine) cleanOnStopWarnings() []string {
	var warns []string
	for _, c := range e.StopArtifacts.Clean {
		warns = append(warns, cleanStopPath(c)...)
	}
	return warns
}

func cleanStopPath(path CleanPath) []string {
	if path.Recursive {
		if err := os.RemoveAll(path.Path); err != nil {
			return []string{fmt.Sprintf("could not clean %s: %v", path.Path, err)}
		}
		return nil
	}
	matches, err := filepath.Glob(path.Path)
	if err != nil {
		return []string{fmt.Sprintf("bad clean_on_stop pattern %q: %v", path.Path, err)}
	}
	if matches == nil {
		if _, err := os.Stat(path.Path); err == nil {
			matches = []string{path.Path}
		}
	}
	var warns []string
	for _, match := range matches {
		if err := os.Remove(match); err != nil {
			warns = append(warns, fmt.Sprintf("could not clean %s: %v", match, err))
		}
	}
	return warns
}

// clearResiduals discovers residual processes after a stop and applies signal
// escalation, returning whatever remains.
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
	if held, ok := errors.AsType[*locks.HeldError](err); ok {
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
