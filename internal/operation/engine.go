package operation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
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
	// ActionRepair is a manual-only recovery action. It never becomes a rule
	// action: an operator must explicitly request removal of a proven-stale
	// runtime pidfile before the normal guarded start path runs.
	ActionRepair = string(rules.ActionRepair)
	// actionCloseSession is intentionally not a rule action: closing an
	// interactive SSH terminal always requires an explicit web request and is
	// never eligible for automatic remediation.
	actionCloseSession = string(rules.ActionCloseSession)
	// actionCloseTerminalSource is intentionally not a rule action: closing an
	// empty tmux server always requires an explicit web request.
	actionCloseTerminalSource = string(rules.ActionCloseTerminalSource)
	// actionReap is intentionally not a rule action: a stray is a process Sermo
	// cannot name, so clearing one always requires an operator who decided that
	// the service's reap.kill_only_if selector describes it.
	actionReap = string(rules.ActionReap)

	// postflightMaxAttempts lets a daemon finish binding its ready socket after
	// its init manager reports a successful start. The retries remain within the
	// operation's bounded context, so they never turn a failed readiness probe
	// into an unbounded wait.
	postflightMaxAttempts   = 5
	postflightRetryInterval = time.Second
)

// Engine performs the section-18 flow for one service over injected capability
// closures. A nil closure means that capability is absent (e.g. no preflight
// section), which is treated as a pass.
type Engine struct {
	Service string // config service name
	Unit    string // backend unit, passed to Manager
	Backend string
	// Lifecycle is the resolved service contract shared by monitoring and
	// operations. Its zero value preserves stop/start with no auxiliaries for
	// directly-built test engines.
	Lifecycle config.ServiceLifecycle
	// StopArtifacts are stopped-state invariants verified after a clean stop.
	StopArtifacts config.StopArtifacts

	ConfigError error
	Manager     servicemgr.Manager
	AcquireLock func(ttl time.Duration) (release func() error, err error)
	LockTTL     time.Duration
	NamedLocks  func() ([]locks.Lock, error)
	Guard       func(ctx context.Context, action string) (blocked bool, reason string, err error)
	Preflight   func(ctx context.Context) checks.Outcome
	Postflight  func(ctx context.Context) checks.Outcome
	// SessionVerifier re-discovers a manual SSH-session target immediately before
	// signalling it. Nil means this service does not offer session closing.
	SessionVerifier func(ctx context.Context, target SessionTarget) (SessionBoundary, error)
	SessionSignaler process.Signaler
	SessionExited   func(int, uint64) (bool, error)
	// ManagedSessionCloser revalidates and terminates one exact login-manager
	// session. It never falls through to direct PID signalling.
	ManagedSessionCloser func(ctx context.Context, target SessionTarget) error
	// TerminalSessionCloser revalidates and closes one tmux/screen session
	// through its configured client. It remains a manual-only operation.
	TerminalSessionCloser func(ctx context.Context, target TerminalSessionTarget) error
	// EmptyTerminalSessionCloser revalidates and closes one configured empty
	// tmux server through its own client. It remains a manual-only operation.
	EmptyTerminalSessionCloser func(ctx context.Context, target TerminalSessionSourceTarget) error
	// ReloadFunc reloads the service's config in place. A `reload:` block builds
	// a closure: a native signal/command that either overrides the backend reload
	// (`when: always`) or stands in for it when the init has no reload of its own
	// (`when: auto`). A nil closure means reload is unavailable.
	ReloadFunc       func(ctx context.Context) error
	ResumeFunc       func(ctx context.Context) error
	ObserveProcesses func() (process.Observation, error)
	Discover         func() ([]process.Process, error)
	Reaper           process.Reaper
	KillPolicy       process.KillPolicy
	// ReapSelector is the service's `reap.kill_only_if` authorization: the only
	// thing that can turn a stray process into a signal target. The zero value is
	// unconfigured and matches nothing, so a service that declares no `reap:`
	// block reports its strays and signals none of them.
	ReapSelector process.KillSelector
	// RepairStalePIDFiles removes only the proven-dead runtime pidfiles that
	// prevent a failed or inactive service from starting. Build wires it from
	// the service's declared pidfile selectors; keeping it injectable makes the
	// engine's action ordering independently testable.
	RepairStalePIDFiles func(context.Context) ([]string, error)
	Sleep               func(time.Duration)
	OperationTimeout    time.Duration
	Emit                func(Result)
}

type plan struct {
	action               string
	preflight            bool
	reconcile            bool
	stop                 bool
	start                bool
	resume               bool
	reload               bool
	postflight           bool
	closeSession         *SessionTarget
	closeTerminalSession *TerminalSessionTarget
	closeTerminalSource  *TerminalSessionSourceTarget
	reap                 bool
	repair               bool
}

// SessionTarget is a freshly displayed SSH terminal session. StartTicks binds
// its PID to one process generation so a PID that has exited and been reused is
// rejected before it can be closed. ManagedByLogind selects the independently
// verified systemd-logind path and never authorizes direct signalling.
type SessionTarget struct {
	PID             int
	StartTicks      uint64
	Terminal        string
	ManagedByLogind bool
}

// SessionBoundary is freshly verified server-side evidence, never client input.
type SessionBoundary struct {
	Residual          bool
	MonitorPID        int
	MonitorStartTicks uint64
	Exe               string
	UID               uint32
}

// TerminalSessionTarget identifies one exact multiplexer session generation
// from a configured terminal_sessions check.
type TerminalSessionTarget struct {
	Check       string
	Multiplexer string
	Name        string
	User        string
	Identity    string
}

// TerminalSessionSourceTarget identifies one configured terminal_sessions
// source. The source configuration remains server-side and is never accepted
// from a browser request.
type TerminalSessionSourceTarget struct {
	Check string
}

// Restart composes verified stop and start under one operation lock and timeout.
func (e Engine) Restart(ctx context.Context) Result {
	return e.run(ctx, plan{action: actionRestart, preflight: true, reconcile: true, stop: true, start: true, postflight: true})
}

// Start runs preflight, starts the service and verifies health.
func (e Engine) Start(ctx context.Context) Result {
	return e.run(ctx, plan{action: actionStart, preflight: true, reconcile: true, start: true, postflight: true})
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

// CloseSession gracefully terminates one operator-selected SSH session. It
// shares the service operation lock, named locks, guards, timeout and event
// path with normal service actions, but deliberately skips service pre/post
// flight because the SSH daemon itself remains running. Connected process closes
// send only SIGTERM; residual sudo terminals require explicit reap authorization.
// Managed closes use the independently verified login manager.
// Connected sessions never escalate; authorized residuals reuse the reaper.
func (e Engine) CloseSession(ctx context.Context, target SessionTarget) Result {
	return e.run(ctx, plan{action: actionCloseSession, closeSession: &target})
}

// CloseTerminalSession closes one operator-selected tmux or screen session
// through the same lock, guard, timeout and event path as SSH session closes.
func (e Engine) CloseTerminalSession(ctx context.Context, target TerminalSessionTarget) Result {
	return e.run(ctx, plan{action: actionCloseSession, closeTerminalSession: &target})
}

// CloseEmptyTerminalSession closes one freshly revalidated empty tmux server
// through the same lock, guard, timeout and event path as other manual closes.
func (e Engine) CloseEmptyTerminalSession(ctx context.Context, target TerminalSessionSourceTarget) Result {
	return e.run(ctx, plan{action: actionCloseTerminalSource, closeTerminalSource: &target})
}

// Reap reports the service's stray processes — the members of its init unit's
// control group that no selector claims and that no longer hang off its principal
// process — and, with apply set, signals the ones the service's
// reap.kill_only_if selector authorizes.
//
// Without apply it is a read-only preview: no operation lock, no guards, no
// event. Listing what a service cannot account for must stay as available as
// `sermoctl processes`, and an operation already in flight is no reason to refuse
// a read. Signalling is the action, and only the action takes the audited path:
// the operation lock, named runtime locks, guards and exactly one event.
func (e Engine) Reap(ctx context.Context, apply bool) Result {
	if !apply {
		return e.previewReap()
	}
	return e.run(ctx, plan{action: actionReap, reap: true})
}

// Repair clears a proven-stale runtime pidfile, then starts a failed or inactive
// service through the same preflight, locks, guards and postflight as Start.
// It is deliberately manual-only; rules cannot dispatch this recovery action.
func (e Engine) Repair(ctx context.Context) Result {
	return e.run(ctx, plan{action: ActionRepair, preflight: true, repair: true, reconcile: true, start: true, postflight: true})
}

// previewReap lists the strays and says which ones the service authorized,
// without touching any of them.
func (e Engine) previewReap() Result {
	result := Result{Service: e.Service, Action: actionReap, Backend: e.Backend, Status: ResultOK}
	if e.ConfigError != nil {
		result.Status, result.Message = ResultFailed, "config: "+e.ConfigError.Error()
		return result
	}
	strays, err := e.discoverStrays()
	if err != nil {
		result.Status, result.Message = ResultFailed, actionReap+": "+err.Error()
		return result
	}
	result.Processes = strays
	if len(strays) == 0 {
		result.Message = "no stray processes"
		return result
	}
	// The message names no CLI flag: the engine states what it would do, and each
	// front end says how to ask for it.
	result.Message = fmt.Sprintf("preview: %d of %d stray process(es) would be signalled", len(e.authorizedStrays(strays)), len(strays))
	return result
}

// reapStrays signals the authorized strays and records the outcome. It is
// terminal for the operation either way, so the caller's deferred event emission
// remains the single audit path.
func (e Engine) reapStrays(ctx context.Context, result *Result) {
	strays, err := e.discoverStrays()
	if err != nil {
		result.Status, result.Message = ResultFailed, actionReap+": "+err.Error()
		return
	}
	if len(strays) == 0 {
		result.Message = actionReap + " ok (no stray processes)"
		return
	}
	result.Processes = strays
	if len(e.authorizedStrays(strays)) == 0 {
		// The fail-safe: an undeclared or non-matching selector reports every stray
		// and signals none. Blocked rather than failed — nothing went wrong, the
		// service simply never authorized this.
		result.Status, result.Message = ResultBlocked, fmt.Sprintf("%s: %d stray process(es) reported, none authorized by %s",
			actionReap, len(strays), process.ReapKillOnlyIfPath)
		return
	}

	reaper := e.Reaper
	var discoveryErr error
	reaper.Rediscover = func() []process.Process {
		var current []process.Process
		current, discoveryErr = e.discoverStrays()
		if discoveryErr != nil {
			return nil
		}
		return current
	}
	reaper.Sleep = e.Sleep
	outcome := reaper.Reap(ctx, strays, process.KillPolicy{
		ForceKill: true,
		// Escalation timing is the service's own stop policy: a stray is one of its
		// processes, and there is no second place to tune how long it may take to die.
		TermTimeout: e.KillPolicy.TermTimeout,
		KillTimeout: e.KillPolicy.KillTimeout,
		KillOnlyIf:  e.ReapSelector,
	})
	if discoveryErr != nil {
		result.Status, result.Message = ResultFailed, actionReap+": "+discoveryErr.Error()
		return
	}
	result.Processes = outcome.Remaining
	applyReapOutcome(ctx, result, len(strays), outcome)
}

// applyReapOutcome turns one reap escalation into the operation result: ok only
// when no stray is left, orphan_processes when any survives or was never
// authorized, and a signal-delivery failure reported rather than swallowed.
func applyReapOutcome(ctx context.Context, result *Result, found int, outcome process.ReapResult) {
	signalled := fmt.Sprintf("signalled %d of %d stray process(es)", len(outcome.Signalled), found)
	if len(outcome.Failed) > 0 {
		failures := make([]string, 0, len(outcome.Failed))
		for _, failure := range outcome.Failed {
			failures = append(failures, fmt.Sprintf("pid %d: %v", failure.PID, failure.Err))
		}
		result.Status = ResultFailed
		result.Message = fmt.Sprintf("%s: %s; %s", actionReap, signalled, strings.Join(failures, "; "))
		return
	}
	if timedOut(ctx) {
		result.Status, result.Message = ResultFailed, timeoutDuring(actionReap)
		return
	}
	if len(outcome.Remaining) > 0 {
		result.Status = ResultOrphanProcesses
		result.Message = fmt.Sprintf("%s: %s; %d remain", actionReap, signalled, len(outcome.Remaining))
		return
	}
	result.Message = fmt.Sprintf("%s ok (%s)", actionReap, signalled)
}

// discoverStrays re-reads live /proc through the engine's discovery closure and
// keeps only the strays. Reading live is what makes escalation safe (safety
// invariants 1, 4 and 12): a stale process table would target PIDs that already
// exited and may have been reused.
func (e Engine) discoverStrays() ([]process.Process, error) {
	if e.Discover == nil {
		return nil, errors.New("process discovery is unavailable for this service")
	}
	procs, err := e.Discover()
	if err != nil {
		return nil, fmt.Errorf("process discovery: %w", err)
	}
	return process.Strays(procs), nil
}

func (e Engine) reapResolver() process.UserResolver {
	if e.Reaper.ResolveUser != nil {
		return e.Reaper.ResolveUser
	}
	return process.DefaultUserLookup().ResolveUser
}

// authorizedStrays returns the strays the service's reap selector allows to be
// signalled. Killable is the same gate every other kill decision passes through,
// so a delegated process, an unresolvable exe, PID 1 and kernel threads are
// refused here for free — and an unconfigured selector refuses everything.
func (e Engine) authorizedStrays(strays []process.Process) []process.Process {
	resolve := e.reapResolver()
	var authorized []process.Process
	for _, stray := range strays {
		if e.ReapSelector.Killable(stray, resolve) {
			authorized = append(authorized, stray)
		}
	}
	return authorized
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
	case ActionRepair:
		return e.Repair(ctx)
	default:
		return Result{Service: e.Service, Action: action, Status: ResultFailed, Message: "unknown action " + action}
	}
}

func (e Engine) run(ctx context.Context, p plan) (result Result) {
	result = Result{Service: e.Service, Action: p.action, Backend: e.Backend, Status: ResultOK}

	var repairedPIDFiles []string

	ctx, cancel := context.WithTimeout(ctx, e.OperationTimeout)
	defer cancel()

	// Step 2: exactly one event per operation, on every exit path including a
	// failed lock acquisition. Registered first.
	defer func() {
		if len(result.Warnings) > 0 {
			result.Message += " (warnings: " + strings.Join(result.Warnings, "; ") + ")"
		}
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
	// It runs before the event defer above, so a lock that could not be
	// released (and will block this service until its TTL) is reported in the
	// operation's one audited result instead of being silently discarded.
	defer releaseOperationLock(release, &result)

	if !e.checkNamedLocks(&result) || !e.runPreflight(ctx, p, &result) || !e.checkGuards(ctx, p, &result) {
		return result
	}
	if e.runCloseAction(ctx, p, &result) {
		return result
	}
	if p.reap {
		e.reapStrays(ctx, &result)
		return result
	}
	if p.repair {
		var repaired bool
		repairedPIDFiles, repaired = e.runRepair(ctx, &result)
		if !repaired {
			return result
		}
	}

	reconciled, proceed := e.runReconciliation(ctx, p, &result)
	if !proceed {
		return result
	}
	if p.repair && !e.resetRepairedState(ctx, &result) {
		return result
	}

	var stopped, systemdReactivated bool
	if p.stop {
		stopped, systemdReactivated = e.stopService(ctx, &result)
		if !stopped {
			return result
		}
	}

	if p.start && !e.startService(ctx, &result, systemdReactivated) {
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
	if reconciled {
		result.Message += " (reconciled stale init state)"
	}
	if systemdReactivated {
		result.Message += " (systemd reactivated the same unit)"
	}
	if len(repairedPIDFiles) > 0 {
		result.Message += " (removed stale pidfile: " + strings.Join(repairedPIDFiles, ", ") + ")"
	}
	return result
}

// runRepair prepares a manual recovery before the normal start phase. It keeps
// repair-specific failure wording out of the top-level operation state machine,
// where the common lock, guard, preflight and postflight sequence remains easy
// to audit.
func (e Engine) runRepair(ctx context.Context, result *Result) ([]string, bool) {
	if e.RepairStalePIDFiles == nil {
		result.Status = ResultFailed
		result.Message = "repair is unavailable for this service"
		return nil, false
	}
	removed, err := e.RepairStalePIDFiles(ctx)
	if err != nil {
		result.Status = ResultFailed
		result.Message = "repair: " + err.Error()
		return nil, false
	}
	return removed, true
}

// releaseOperationLock releases the operation lock and keeps a failure as a
// warning on the operation's result.
func releaseOperationLock(release func() error, result *Result) {
	if err := release(); err != nil {
		result.Warnings = append(result.Warnings, "release operation lock: "+err.Error())
	}
}

// resetRepairedState clears a failed init marker after reconciliation handled
// any survivors. It goes through resetStopped, which revalidates process
// absence immediately before the reset and verifies the backend converged to
// inactive, so a repair never zaps the bookkeeping of a live daemon and then
// starts a second instance beside it.
func (e Engine) resetRepairedState(ctx context.Context, result *Result) bool {
	if e.Manager == nil {
		return true
	}
	status, err := e.Manager.Status(ctx, e.Unit)
	if err != nil {
		result.Status, result.Message = ResultFailed, "repair: query init state: "+err.Error()
		return false
	}
	if status.Status != servicemgr.StatusFailed {
		return true
	}
	if err := e.resetStopped(ctx, false); err != nil {
		result.Status, result.Message = ResultFailed, "repair: "+err.Error()
		return false
	}
	return true
}

// runCloseAction executes one manual session-close variant when the plan carries
// one. Both successful and failed closes are terminal for the operation; the
// caller's deferred event emission therefore remains the single audit path.
func (e Engine) runCloseAction(ctx context.Context, p plan, result *Result) bool {
	if p.closeSession != nil {
		if e.closeSession(ctx, *p.closeSession, result) {
			result.Message = "close SSH session ok"
		}
		return true
	}
	if p.closeTerminalSession != nil {
		if e.closeTerminalSession(ctx, *p.closeTerminalSession, result) {
			result.Message = "close terminal session ok"
		}
		return true
	}
	if p.closeTerminalSource != nil {
		if e.closeTerminalSource(ctx, *p.closeTerminalSource, result) {
			result.Message = "close empty terminal session source ok"
		}
		return true
	}
	return false
}

// runReconciliation applies the start/restart stale-init guard and translates
// its outcome into the operation result. Keeping this phase-shaped helper next
// to run makes the top-level operation sequence readable without duplicating the
// fail-closed result contract.
func (e Engine) runReconciliation(ctx context.Context, p plan, result *Result) (reconciled, proceed bool) {
	if !p.reconcile {
		return false, true
	}
	reconciled, remaining, err := e.reconcileInitState(ctx, result)
	if err != nil {
		result.Status = ResultFailed
		if errors.Is(err, errProcessIdentity) {
			result.Status = ResultBlocked
		}
		result.Message = err.Error()
		result.Processes = remaining
		return false, false
	}
	if len(remaining) > 0 {
		result.Status = ResultOrphanProcesses
		result.Message = residualsRemain(remaining, "before "+p.action)
		result.Processes = remaining
		return false, false
	}
	return reconciled, true
}

// residualsRemain is the operator-facing wording for residual processes the stop
// could not clear, in the given phase. One owner for both phases, so a new one
// cannot invent a second spelling — the same reason timeoutDuring exists.
//
// It calls out how many are strays. A stray survives a stop precisely because
// nothing in the configuration accounts for it, so "residual process" alone sends
// the operator looking for a selector that was never written; naming it, and the
// verb that can clear it, is the difference between a dead end and a next step.
func residualsRemain(remaining []process.Process, phase string) string {
	message := fmt.Sprintf("%d residual process(es) remain %s", len(remaining), phase)
	strays := len(process.Strays(remaining))
	if strays == 0 {
		return message
	}
	return fmt.Sprintf("%s (%d stray, unaccounted for by any selector; `sermoctl %s` lists them and, with %s declared, clears them)",
		message, strays, actionReap, process.ReapKillOnlyIfPath)
}

func (e Engine) closeSession(ctx context.Context, target SessionTarget, result *Result) bool {
	const prefix = "close SSH session: "
	if target.ManagedByLogind {
		var closer func(context.Context) error
		if e.ManagedSessionCloser != nil {
			closer = func(ctx context.Context) error { return e.ManagedSessionCloser(ctx, target) }
		}
		return runSessionCloser(ctx, result, closer, "managed SSH session close is unavailable for this service", prefix)
	}
	if e.SessionVerifier == nil {
		return failSession(result, prefix, errors.New("SSH session close is unavailable for this service"))
	}
	boundary, err := e.SessionVerifier(ctx, target)
	if err != nil {
		return failSession(result, prefix, err)
	}
	// The verifier may not honor ctx; never proceed after cancellation.
	if err := ctx.Err(); err != nil {
		return failSession(result, prefix, err)
	}
	if boundary.Residual {
		return e.closeResidualSession(ctx, target, boundary, result)
	}
	signaler := e.SessionSignaler
	if signaler == nil {
		signaler = process.OSSignaler{}
	}
	proc := process.Process{PID: target.PID, StartTicks: target.StartTicks, Exe: boundary.Exe, ExeOK: true, UID: boundary.UID}
	if err := process.SignalProcess(ctx, signaler, proc, syscall.SIGTERM); err != nil {
		return failSession(result, prefix, err)
	}
	return e.waitSessionExit(ctx, target, result)
}

func (e Engine) closeTerminalSession(ctx context.Context, target TerminalSessionTarget, result *Result) bool {
	var closer func(context.Context) error
	if e.TerminalSessionCloser != nil {
		closer = func(ctx context.Context) error { return e.TerminalSessionCloser(ctx, target) }
	}
	return runSessionCloser(ctx, result, closer, "terminal session close is unavailable for this service", "close terminal session: ")
}

// runSessionCloser runs one session-close step: a nil closer reports the
// unavailable message, a cancelled context or a closer error reports
// errorPrefix plus the cause.
func runSessionCloser(ctx context.Context, result *Result, closer func(context.Context) error, unavailable, errorPrefix string) bool {
	if closer == nil {
		return failUnavailable(result, unavailable)
	}
	if err := ctx.Err(); err != nil {
		return failSession(result, errorPrefix, err)
	}
	if err := closer(ctx); err != nil {
		return failSession(result, errorPrefix, err)
	}
	return true
}

func (e Engine) closeTerminalSource(ctx context.Context, target TerminalSessionSourceTarget, result *Result) bool {
	const prefix = "close empty terminal session source: "
	if e.EmptyTerminalSessionCloser == nil {
		return failUnavailable(result, "empty terminal session source close is unavailable for this service")
	}
	if target.Check == "" {
		return failUnavailable(result, prefix+"invalid terminal session source")
	}
	closer := func(ctx context.Context) error { return e.EmptyTerminalSessionCloser(ctx, target) }
	return runSessionCloser(ctx, result, closer, "", prefix)
}

// failSession marks result failed with prefix plus err and returns false, the
// shape every session-close step reports through.
func failSession(result *Result, prefix string, err error) bool {
	result.Status = ResultFailed
	result.Message = prefix + err.Error()
	return false
}

// failUnavailable marks result failed with a fixed message and returns false.
func failUnavailable(result *Result, message string) bool {
	result.Status = ResultFailed
	result.Message = message
	return false
}

// failPhase marks result failed with the phase's timeout message when the
// context expired, else errPrefix plus the backend error. It always returns
// false so callers can `return failPhase(...)` from a bool-shaped step.
func failPhase(ctx context.Context, result *Result, timeoutMsg, errPrefix string, err error) bool {
	result.Status = ResultFailed
	if timedOut(ctx) {
		result.Message = timeoutMsg
	} else {
		result.Message = errPrefix + err.Error()
	}
	return false
}

// timeoutDuring and cancelledDuring are the two operator-facing wordings for an
// operation the context ended in a named phase. One owner each, so a new phase
// cannot invent a second spelling of either.
func timeoutDuring(phase string) string   { return "operation timed out during " + phase }
func cancelledDuring(phase string) string { return "operation cancelled during " + phase }

// failWait marks result failed for a bounded wait the context ended, telling a
// real deadline apart from a cancellation. A config reload (SIGHUP) or shutdown
// cancels the operation context, and every `--with-config` deployment reloads the
// daemon — so reporting that as a timeout sends the operator looking for a slow
// service that does not exist. The phase names the wait in the operator's terms.
func failWait(ctx context.Context, result *Result, phase string) bool {
	result.Status = ResultFailed
	if timedOut(ctx) {
		result.Message = timeoutDuring(phase)
	} else {
		result.Message = cancelledDuring(phase)
	}
	return false
}

func (e Engine) resumeService(ctx context.Context, result *Result) bool {
	if e.ResumeFunc == nil {
		result.Status, result.Message = ResultFailed, "resume: operation unsupported by backend"
		return false
	}
	return e.runBackendAction(ctx, result, actionResume, e.ResumeFunc)
}

func (e Engine) reloadService(ctx context.Context, result *Result) bool {
	if e.ReloadFunc == nil {
		result.Status, result.Message = ResultFailed, "reload: operation unsupported by service configuration"
		return false
	}
	return e.runBackendAction(ctx, result, actionReload, e.ReloadFunc)
}

// runBackendAction centralizes the result contract shared by backend actions:
// timeout-aware errors followed by a settle-aware status check. Higher-level
// safety gates and postflight remain in run, around this primitive. The check
// retries within the same bounded window postflight uses, because a backend
// can accept a start and report the settled state a moment later — OpenRC
// answers `inactive` until a starting service's readiness callback runs.
func (e Engine) runBackendAction(ctx context.Context, result *Result, action string, run func(context.Context) error) bool {
	if err := ctx.Err(); err != nil {
		return failPhase(ctx, result, timeoutDuring(action), action+": ", err)
	}
	actionErr := run(ctx)
	if actionErr != nil && (action != actionStart || ctx.Err() != nil) {
		return failPhase(ctx, result, timeoutDuring(action), action+": ", actionErr)
	}
	for attempt := range postflightMaxAttempts {
		final := attempt+1 == postflightMaxAttempts
		healthy, settled := e.ensureServiceHealthy(ctx, result, action, final)
		if settled {
			if actionErr != nil {
				observation, err := e.observeProcesses(ctx)
				if !healthy || err != nil || !observation.Trusted {
					return failPhase(ctx, result, timeoutDuring(action), action+": ", actionErr)
				}
				result.Warnings = append(result.Warnings, action+" command: "+actionErr.Error()+"; running process and init state verified")
			}
			return healthy
		}
		if err := process.Wait(ctx, e.Sleep, postflightRetryInterval); err != nil {
			return failWait(ctx, result, action+" settle wait")
		}
	}
	return false
}

// ensureServiceHealthy judges the backend state after a start-like action.
// final marks the last postflight attempt: until then a not-yet-active status
// only reports settling=false so the bounded window keeps waiting — OpenRC
// holds a starting service in `inactive` until its readiness callback runs,
// and failing on that instant verdict aborted restarts of services that were
// coming up fine. A failed status still fails fast on every attempt.
func (e Engine) ensureServiceHealthy(ctx context.Context, result *Result, action string, final bool) (healthy, settled bool) {
	status, err := e.Manager.Status(ctx, e.Unit)
	if err != nil {
		return failPhase(ctx, result, timeoutDuring(action+" status"), "status after "+action+": ", err), true
	}
	if status.Status == servicemgr.StatusFailed {
		result.Status, result.Message = ResultFailed, "service failed after "+action
		return false, true
	}
	if (action == actionStart || action == actionRestart || action == actionResume) && status.Status != servicemgr.StatusActive {
		if !final {
			return false, false
		}
		result.Status, result.Message = ResultFailed, "service not active after "+action
		return false, true
	}
	if e.Lifecycle.ProcessMode == config.ServiceProcessResident {
		observation, err := e.observeProcesses(ctx)
		if err != nil {
			result.Status, result.Message = ResultFailed, err.Error()
			return false, true
		}
		if len(nonDelegatedResiduals(observation.Processes)) == 0 || observation.IdentityRequired && !observation.Trusted {
			if !final {
				return false, false
			}
			result.Status, result.Message = ResultFailed, "init reports active but service process is absent"
			return false, true
		}
	}
	return true, true
}

func (e Engine) runPostflight(ctx context.Context, p plan, result *Result) bool {
	if !p.postflight || e.Postflight == nil {
		return true
	}
	var out checks.Outcome
	postflightReady := false
	for attempt := range postflightMaxAttempts {
		if p.start || p.resume {
			healthy, settled := e.ensureServiceHealthy(ctx, result, result.Action, attempt+1 == postflightMaxAttempts)
			if settled && !healthy {
				return false
			}
			if !settled {
				// Not active yet: spend this attempt on the settle wait below
				// instead of judging checks against a still-starting service.
				postflightReady = false
			}
		}
		if !postflightReady {
			out = e.Postflight(ctx)
			postflightReady = out.OK
		}
		// A service may report active immediately after systemd accepts start and
		// fail a moment later. Keep the bounded postflight window open so the
		// returned operation result matches the backend's settled state.
		if postflightReady && attempt+1 == postflightMaxAttempts {
			result.Checks = append(result.Checks, out.Results...)
			return true
		}
		if attempt+1 == postflightMaxAttempts {
			break
		}
		if err := process.Wait(ctx, e.Sleep, postflightRetryInterval); err != nil {
			result.Checks = append(result.Checks, out.Results...)
			return failWait(ctx, result, "postflight")
		}
	}
	result.Checks = append(result.Checks, out.Results...)
	result.Status, result.Message = ResultPostflightFailed, "postflight failed"
	return false
}

func (e Engine) startService(ctx context.Context, result *Result, primaryActive bool) bool {
	for _, unit := range e.Lifecycle.AuxiliaryUnits {
		if err := ctx.Err(); err != nil {
			return failPhase(ctx, result, timeoutDuring(actionStart), "start: ", err)
		}
		if err := e.Manager.Start(ctx, unit); err != nil {
			return failPhase(ctx, result, "operation timed out starting also_service "+unit, "start "+unit+": ", err)
		}
	}
	if primaryActive {
		return true
	}
	return e.runBackendAction(ctx, result, actionStart, func(ctx context.Context) error {
		return e.Manager.Start(ctx, e.Unit)
	})
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
	warns := make([]string, 0, len(e.StopArtifacts.PidfilePaths)+len(e.StopArtifacts.Files))
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
	warns := make([]string, 0, len(e.StopArtifacts.Clean))
	for _, c := range e.StopArtifacts.Clean {
		warns = append(warns, cleanStopPath(c)...)
	}
	return warns
}

func cleanStopPath(path config.CleanPath) []string {
	if path.Recursive {
		// The config validator proves the configured path is safe at load time,
		// but a symlink planted in an ancestor afterwards would redirect the
		// recursive delete elsewhere (e.g. an ancestor pointing at /etc). Refuse
		// to delete through any symlinked component — fail safe rather than
		// remove the wrong tree as root.
		if link, err := firstSymlinkAncestor(path.Path); err != nil {
			return []string{fmt.Sprintf("could not clean %s: %v", path.Path, err)}
		} else if link != "" {
			return []string{fmt.Sprintf("refusing to clean %s: %s is a symlink", path.Path, link)}
		}
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

// firstSymlinkAncestor returns the first ancestor of path (from root down,
// excluding path itself) that is a symlink, or "" if none is. A missing
// ancestor is not a symlink; a stat error other than not-exist is returned so
// the caller fails safe. Checking ancestors (not path itself) is what matters
// for a recursive delete: os.RemoveAll does not follow a symlink AT path, but
// it does traverse symlinked parents.
func firstSymlinkAncestor(path string) (string, error) {
	clean := filepath.Clean(path)
	var ancestors []string
	for dir := filepath.Dir(clean); ; dir = filepath.Dir(dir) {
		ancestors = append(ancestors, dir)
		if dir == filepath.Dir(dir) {
			break
		}
	}
	// Walk root-first so the report names the highest symlink.
	for _, dir := range slices.Backward(ancestors) {
		info, err := os.Lstat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("lstat %s: %w", dir, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return dir, nil
		}
	}
	return "", nil
}

// nonDelegatedResiduals drops the processes a service declared delegated. They
// belong to the service and stay visible in monitoring, but the init unit keeps
// them alive on purpose across a daemon restart, so they are never a residual of
// a stop and never a reaper target. When no process is delegated, the original
// slice is returned without allocating.
func nonDelegatedResiduals(procs []process.Process) []process.Process {
	for i, proc := range procs {
		if !proc.Delegated {
			continue
		}
		kept := make([]process.Process, 0, len(procs)-1)
		kept = append(kept, procs[:i]...)
		for _, candidate := range procs[i+1:] {
			if !candidate.Delegated {
				kept = append(kept, candidate)
			}
		}
		return kept
	}
	return procs
}

// residualOutcome is the result of one residual-handling pass. found preserves
// whether the initial live discovery saw a non-delegated residual even when the
// reaper cleared it; accepted records the narrow systemd-reactivation exception.
// Keeping these facts together avoids ambiguous parallel return values and lets
// reconciliation reuse the first authoritative discovery.
type residualOutcome struct {
	remaining []process.Process
	found     bool
	accepted  bool
}

// clearResiduals discovers residual processes after a stop and applies signal
// escalation, returning one outcome that preserves whether any were initially
// found. accept may acknowledge an already reactivated backend-owned process set
// before the reaper can signal it.
func (e Engine) clearResiduals(ctx context.Context, accept func([]process.Process) (bool, error)) (residualOutcome, error) {
	if e.Discover == nil {
		return residualOutcome{}, nil
	}
	var discoverErr error
	discover := func() []process.Process {
		if discoverErr != nil {
			return nil
		}
		procs, err := e.Discover()
		if err != nil {
			discoverErr = err
			return nil
		}
		return nonDelegatedResiduals(procs)
	}
	residuals := discover()
	outcome := residualOutcome{remaining: residuals, found: len(residuals) > 0}
	if discoverErr != nil {
		return outcome, discoverErr
	}
	if !outcome.found {
		return outcome, nil
	}
	if accept != nil {
		accepted, err := accept(residuals)
		outcome.accepted = accepted
		if accepted || err != nil {
			return outcome, err
		}
	}
	reaper := e.Reaper
	reaper.Rediscover = discover // re-evaluate identity each round
	reaper.Sleep = e.Sleep
	outcome.remaining = reaper.Reap(ctx, residuals, e.KillPolicy).Remaining
	if discoverErr != nil {
		return outcome, discoverErr
	}
	return outcome, nil
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
	out := slices.DeleteFunc(slices.Clone(in), func(l locks.Lock) bool { return !l.Active() })
	if len(out) == 0 {
		return nil
	}
	return out
}
