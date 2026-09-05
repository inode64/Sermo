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
	ActionRepair = "repair"
	// actionCloseSession is intentionally not a rule action: closing an
	// interactive SSH terminal always requires an explicit web request and is
	// never eligible for automatic remediation.
	actionCloseSession = "close_session"
	// actionCloseTerminalSource is intentionally not a rule action: closing an
	// empty tmux server always requires an explicit web request.
	actionCloseTerminalSource = "close_terminal_source"
	// actionReap is intentionally not a rule action: a stray is a process Sermo
	// cannot name, so clearing one always requires an operator who decided that
	// the service's reap.kill_only_if selector describes it.
	actionReap = process.SectionReap

	// postflightMaxAttempts lets a daemon finish binding its ready socket after
	// its init manager reports a successful start. The retries remain within the
	// operation's bounded context, so they never turn a failed readiness probe
	// into an unbounded wait.
	postflightMaxAttempts   = 5
	postflightRetryInterval = time.Second
)

// Manager is the subset of servicemgr.Manager the engine uses. Staged restart
// uses Stop+Start so residual processes can be handled between the phases;
// services explicitly configured for native restart use Restart atomically.
type Manager interface {
	Start(ctx context.Context, service string) error
	Stop(ctx context.Context, service string) error
	Restart(ctx context.Context, service string) error
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
	// Lifecycle is the resolved service contract shared by monitoring and
	// operations. Its zero value preserves staged restart with no auxiliaries for
	// directly-built test engines.
	Lifecycle config.ServiceLifecycle
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
	// SessionVerifier re-discovers a manual SSH-session target immediately before
	// signalling it. Nil means this service does not offer session closing.
	SessionVerifier func(ctx context.Context, target SessionTarget) error
	SessionSignaler process.Signaler
	// ManagedSessionCloser revalidates and terminates one exact login-manager
	// session. It never falls through to direct PID signalling.
	ManagedSessionCloser func(ctx context.Context, target SessionTarget) error
	// TerminalSessionCloser revalidates and closes one tmux/screen session
	// through its configured client. It remains a manual-only operation.
	TerminalSessionCloser func(ctx context.Context, target TerminalSessionTarget) error
	// EmptyTerminalSessionCloser revalidates and closes one configured empty
	// tmux server through its own client. It remains a manual-only operation.
	EmptyTerminalSessionCloser func(ctx context.Context, target TerminalSessionSourceTarget) error
	// ReloadFunc reloads the service's config in place. When nil the engine falls
	// back to Manager.Reload (the backend per-unit reload). A `reload:` block
	// builds a richer closure: a native signal/command that either overrides the
	// backend reload (`when: always`) or stands in for it when the init has no
	// reload of its own (`when: auto`).
	ReloadFunc func(ctx context.Context) error
	ResumeFunc func(ctx context.Context) error
	Discover   func() ([]process.Process, error)
	Reaper     process.Reaper
	KillPolicy process.KillPolicy
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
	action               string
	preflight            bool
	reconcile            bool
	stop                 bool
	start                bool
	nativeRestart        bool
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

// Restart executes the configured restart strategy and verifies health. Staged
// mode stops, clears residuals and starts; outside stale-init reconciliation,
// native mode delegates one atomic restart to the init backend. Both modes first
// reconcile an init state that has drifted from reality.
func (e Engine) Restart(ctx context.Context) Result {
	if e.Lifecycle.RestartMode == config.RestartModeNative {
		return e.run(ctx, plan{action: actionRestart, preflight: true, reconcile: true, nativeRestart: true, postflight: true})
	}
	return e.run(ctx, plan{action: actionRestart, preflight: true, reconcile: true, stop: true, start: true, postflight: true})
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

// CloseSession gracefully terminates one operator-selected SSH session. It
// shares the service operation lock, named locks, guards, timeout and event
// path with normal service actions, but deliberately skips service pre/post
// flight because the SSH daemon itself remains running. Direct process closes
// send only SIGTERM; managed closes use the independently verified login manager.
// Neither path escalates to SIGKILL for an interactive user session.
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
	return e.run(ctx, plan{action: ActionRepair, preflight: true, repair: true, start: true, postflight: true})
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
	reaper.Rediscover = e.rediscoverStrays // re-evaluate identity each round
	reaper.Sleep = e.Sleep
	outcome := reaper.Reap(ctx, strays, process.KillPolicy{
		ForceKill: true,
		// Escalation timing is the service's own stop policy: a stray is one of its
		// processes, and there is no second place to tune how long it may take to die.
		TermTimeout: e.KillPolicy.TermTimeout,
		KillTimeout: e.KillPolicy.KillTimeout,
		KillOnlyIf:  e.ReapSelector,
	})
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
	if len(outcome.Remaining) > 0 {
		if timedOut(ctx) {
			result.Status, result.Message = ResultFailed, timeoutDuring(actionReap)
			return
		}
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

// rediscoverStrays is the reaper's per-round view. A discovery error between
// rounds returns no survivors to signal rather than a stale set, so escalation
// stops instead of acting on processes it can no longer verify; the surviving
// process is then reported by the following round's result.
func (e Engine) rediscoverStrays() []process.Process {
	strays, err := e.discoverStrays()
	if err != nil {
		return nil
	}
	return strays
}

// authorizedStrays returns the strays the service's reap selector allows to be
// signalled. Killable is the same gate every other kill decision passes through,
// so a delegated process, an unresolvable exe, PID 1 and kernel threads are
// refused here for free — and an unconfigured selector refuses everything.
func (e Engine) authorizedStrays(strays []process.Process) []process.Process {
	resolve := e.Reaper.ResolveUser
	if resolve == nil {
		resolve = process.DefaultUserLookup().ResolveUser
	}
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

	// Best-effort failures stopping also_service units; folded into the final
	// success message (a successful stop is not failed by an auxiliary unit).
	var alsoStopErrs []string
	// Stale stopped-state artifacts (pidfile/files still present after a clean
	// stop); folded into the success message as a warning, like alsoStopErrs.
	var staleWarn []string
	var repairedPIDFiles []string

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

	var stopped, systemdReactivated bool
	if p.stop {
		alsoStopErrs, staleWarn, stopped, systemdReactivated = e.stopService(ctx, &result)
		if !stopped {
			return result
		}
	}

	if p.start && !systemdReactivated && !e.startService(ctx, &result) {
		return result
	}
	if p.nativeRestart && !e.restartService(ctx, &result) {
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
	if len(alsoStopErrs) > 0 {
		result.Message += " (also_service: " + strings.Join(alsoStopErrs, "; ") + ")"
	}
	if len(staleWarn) > 0 {
		result.Message += " (stale: " + strings.Join(staleWarn, "; ") + ")"
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

// runReconciliation applies the restart-only stale-init guard and translates
// its outcome into the operation result. Keeping this phase-shaped helper next
// to run makes the top-level operation sequence readable without duplicating the
// fail-closed result contract.
func (e Engine) runReconciliation(ctx context.Context, p plan, result *Result) (reconciled, proceed bool) {
	if !p.reconcile {
		return false, true
	}
	reconciled, remaining, err := e.reconcileInitState(ctx)
	if err != nil {
		result.Status = ResultFailed
		result.Message = err.Error()
		result.Processes = remaining
		return false, false
	}
	if len(remaining) > 0 {
		result.Status = ResultOrphanProcesses
		result.Message = residualsRemain(remaining, "before restart")
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
	var verify func(context.Context) error
	if e.SessionVerifier != nil {
		verify = func(ctx context.Context) error { return e.SessionVerifier(ctx, target) }
	}
	if !runSessionCloser(ctx, result, verify, "SSH session close is unavailable for this service", prefix) {
		return false
	}
	// The verifier may not honor ctx; never signal after cancellation.
	if err := ctx.Err(); err != nil {
		return failSession(result, prefix, err)
	}
	signaler := e.SessionSignaler
	if signaler == nil {
		signaler = process.OSSignaler{}
	}
	if err := signaler.Signal(target.PID, syscall.SIGTERM); err != nil {
		return failSession(result, prefix, err)
	}
	return true
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
	reload := e.ReloadFunc
	if reload == nil {
		reload = func(ctx context.Context) error { return e.Manager.Reload(ctx, e.Unit) }
	}
	return e.runBackendAction(ctx, result, actionReload, reload)
}

func (e Engine) restartService(ctx context.Context, result *Result) bool {
	return e.runBackendAction(ctx, result, actionRestart, func(ctx context.Context) error {
		return e.Manager.Restart(ctx, e.Unit)
	})
}

// runBackendAction centralizes the result contract shared by backend actions:
// timeout-aware errors followed by a settle-aware status check. Higher-level
// safety gates and postflight remain in run, around this primitive. The check
// retries within the same bounded window postflight uses, because a backend
// can accept a start and report the settled state a moment later — OpenRC
// answers `inactive` until a starting service's readiness callback runs.
func (e Engine) runBackendAction(ctx context.Context, result *Result, action string, run func(context.Context) error) bool {
	if err := run(ctx); err != nil {
		return failPhase(ctx, result, timeoutDuring(action), action+": ", err)
	}
	for attempt := range postflightMaxAttempts {
		final := attempt+1 == postflightMaxAttempts
		healthy, settled := e.ensureServiceHealthy(ctx, result, action, final)
		if settled {
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
		return true, true
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
	return true, true
}

func (e Engine) runPostflight(ctx context.Context, p plan, result *Result) bool {
	if !p.postflight || e.Postflight == nil {
		return true
	}
	var out checks.Outcome
	postflightReady := false
	for attempt := range postflightMaxAttempts {
		if p.start || p.nativeRestart || p.resume {
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

func (e Engine) startService(ctx context.Context, result *Result) bool {
	for _, unit := range e.Lifecycle.AuxiliaryUnits {
		if err := e.Manager.Start(ctx, unit); err != nil {
			return failPhase(ctx, result, "operation timed out starting also_service "+unit, "start "+unit+": ", err)
		}
	}
	return e.runBackendAction(ctx, result, actionStart, func(ctx context.Context) error {
		return e.Manager.Start(ctx, e.Unit)
	})
}

func (e Engine) stopService(ctx context.Context, result *Result) (alsoStopErrs, staleWarn []string, stopped, systemdReactivated bool) {
	if err := e.Manager.Stop(ctx, e.Unit); err != nil {
		_ = failPhase(ctx, result, timeoutDuring("stop"), "stop: ", err)
		return nil, nil, false, false
	}
	for _, unit := range slices.Backward(e.Lifecycle.AuxiliaryUnits) {
		if err := e.Manager.Stop(ctx, unit); err != nil {
			alsoStopErrs = append(alsoStopErrs, fmt.Sprintf("stop %s: %v", unit, err))
		}
	}
	if err := process.Wait(ctx, e.Sleep, e.KillPolicy.GracefulTimeout); err != nil {
		_ = failWait(ctx, result, "graceful stop wait")
		return alsoStopErrs, nil, false, false
	}
	residuals, err := e.clearResiduals(ctx, func(residuals []process.Process) bool {
		return e.systemdReactivated(ctx, result.Action, residuals)
	})
	remaining, systemdReactivated := residuals.remaining, residuals.accepted
	if err != nil {
		result.Status, result.Message, result.Processes = ResultFailed, "process discovery: "+err.Error(), remaining
		return alsoStopErrs, nil, false, false
	}
	if len(remaining) > 0 {
		if systemdReactivated {
			return alsoStopErrs, nil, true, true
		}
		result.Processes = remaining
		if timedOut(ctx) {
			result.Status, result.Message = ResultFailed, timeoutDuring("residual process handling")
		} else {
			result.Status, result.Message = ResultOrphanProcesses, residualsRemain(remaining, "after stop")
		}
		return alsoStopErrs, nil, false, false
	}
	_ = e.Manager.ResetState(ctx, e.Unit)
	return alsoStopErrs, e.verifyStopped(), true, false
}

// systemdReactivated reports whether an isolated systemd restart has already
// been completed by systemd after the primary unit stopped. This happens for
// socket-activated units: the socket remains untouched by the isolated stop and
// systemd starts the same service again as soon as it receives work.
//
// Accept only backend-attributed processes and an active unit. A selector-only
// residual, an inactive/unknown unit, or any non-systemd backend remains an
// orphan so this exception cannot authorize an unrelated process or a second
// service action.
func (e Engine) systemdReactivated(ctx context.Context, action string, residuals []process.Process) bool {
	if action != actionRestart || e.Backend != string(servicemgr.BackendSystemd) || len(residuals) == 0 {
		return false
	}
	for _, residual := range residuals {
		if residual.Source != process.SourceBackend {
			return false
		}
	}
	status, err := e.Manager.Status(ctx, e.Unit)
	return err == nil && status.Status == servicemgr.StatusActive
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
	if p.action != actionRestart || e.RestartIdentity == nil {
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

func cleanStopPath(path CleanPath) []string {
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
		if dir == "/" || dir == "." || dir == filepath.Dir(dir) {
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

// reconcileInitState clears an init state that has drifted from reality before a
// restart acts on it: the backend reports the unit as not active while the
// service's own processes are still running, so the init has lost track of a live
// daemon. Neither restart mode recovers from that alone — a native restart asks
// the init to signal a PID it no longer knows, and the replacement daemon then
// collides with the survivor over its port, socket or lock; a staged restart only
// gets there through the reaper.
//
// It signals nothing a stop would not have signalled: the same discovery, the
// same stop_policy, and delegated processes excluded, so a workload tree the unit
// deliberately keeps alive survives. Unknown and transitional backend states are
// not proof of drift and never enter the reaper. A discovery/reset error or any
// survivor fails closed before the restart can launch a second daemon.
func (e Engine) reconcileInitState(ctx context.Context) (bool, []process.Process, error) {
	if e.Discover == nil || e.Manager == nil {
		return false, nil, nil
	}
	status, err := e.Manager.Status(ctx, e.Unit)
	if err != nil {
		return false, nil, fmt.Errorf("query init state before restart: %w", err)
	}
	if status.Status != servicemgr.StatusInactive && status.Status != servicemgr.StatusFailed {
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
	if err := e.Manager.ResetState(ctx, e.Unit); err != nil {
		return false, nil, fmt.Errorf("reset init state before restart: %w", err)
	}
	return true, nil, nil
}

// clearResiduals discovers residual processes after a stop and applies signal
// escalation, returning one outcome that preserves whether any were initially
// found. accept may acknowledge an already reactivated backend-owned process set
// before the reaper can signal it.
func (e Engine) clearResiduals(ctx context.Context, accept func([]process.Process) bool) (residualOutcome, error) {
	if e.Discover == nil {
		return residualOutcome{}, nil
	}
	var discoverErr error
	discover := func() []process.Process {
		procs, err := e.Discover()
		if err != nil && discoverErr == nil {
			discoverErr = err
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
	if accept != nil && accept(residuals) {
		outcome.accepted = true
		return outcome, nil
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
	var out []locks.Lock
	for _, l := range in {
		if l.Active() {
			out = append(out, l)
		}
	}
	return out
}
