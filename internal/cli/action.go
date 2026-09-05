package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"sermo/internal/app"
	"sermo/internal/config"
	"sermo/internal/operation"
	"sermo/internal/state"
)

// runAction performs a start/stop/restart/reload/resume/repair through the safe operation engine
// : the resolved service is run under the internal operation lock,
// active named runtime locks, required preflight, guards, residual-process
// handling and postflight. Manual sermoctl actions are not rate limited, but are
// fully guarded.
func (a App) runAction(ctx context.Context, opts options, action string) int {
	if code := a.requireSingleServiceName(opts.service() != "", len(opts.args), action, action); code != exitSuccess {
		return code
	}
	service := opts.service()

	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	service, code = a.canonicalService(opts, cfg, service)
	if code != exitSuccess {
		return code
	}
	if action == actionReload {
		if issues := config.Validate(cfg); len(issues) > 0 {
			a.printIssues(opts, issues)
			return exitConfigInvalid
		}
	}
	resolved, code := a.resolveService(opts, cfg, service)
	if code != exitSuccess {
		return code
	}
	var (
		actionStore *state.Store
		err         error
	)
	if operation.IsServiceAction(action) {
		actionStore, err = openStateStore(ctx, cfg)
		if err != nil {
			a.recordAccess(cfg, action, service, accessStatusError, err.Error())
			return a.fail(opts, fmt.Sprintf("operation state unavailable: %v", err))
		}
	}
	if actionStore != nil {
		defer func() { _ = actionStore.Close() }()
	}
	runner, closeRunner, err := a.prepareManualOperationRunner(ctx, opts, cfg, resolved, service, action, actionStore)
	if err != nil {
		return a.fail(opts, err.Error())
	}
	defer closeRunner()
	result, err := a.operateWithCascade(ctx, opts, cfg, resolved, service, action, actionStore, runner)
	if err != nil {
		a.recordAccess(cfg, action, service, accessStatusError, err.Error())
		return a.fail(opts, err.Error())
	}
	a.notifyInteractiveBlockedAction(ctx, result)

	status := accessStatusOK
	if result.Status != operation.ResultOK {
		status = accessStatusError
	}
	a.recordAccess(cfg, action, service, status, result.Message)

	if opts.json {
		writeJSON(a.Stdout, result)
	} else if !opts.quiet {
		a.printOperation(opts, result)
	}
	return operationExit(result.Status)
}

// operateWithCascade runs the action on the primary service, and — unless
// --no-cascade — on the services it lists in also_apply, in dependency order
// (start/restart: primary first; stop: additionals first). Targets run through
// their own guarded operation; each target's result is printed. The primary's
// result is returned and drives the exit code.
func (a App) operateWithCascade(ctx context.Context, opts options, cfg *config.Config, resolved config.Resolved, service, action string, actionStore *state.Store, runner manualOperationRunner) (operation.Result, error) {
	targets := config.CascadeTargets(resolved.Tree)
	// also_apply cascades only lifecycle actions that change running state. Manual
	// repair and reap always act on the one service the operator named.
	if opts.noCascade || !operation.CascadesAlsoApply(action) || len(targets) == 0 {
		return a.operateWithManualState(ctx, opts, cfg, resolved, service, action, actionStore, runner)
	}
	resolvedByService := map[string]config.Resolved{service: resolved}
	resolveErrors := map[string]error{}
	resolve := func(svc string) (config.Resolved, error) {
		if res, ok := resolvedByService[svc]; ok {
			return res, nil
		}
		if err, ok := resolveErrors[svc]; ok {
			return config.Resolved{}, err
		}
		res, errs := cfg.Resolve(svc)
		if len(errs) > 0 {
			err := fmt.Errorf("resolve cascade target %s: %s", svc, errs[0])
			resolveErrors[svc] = err
			return config.Resolved{}, err
		}
		resolvedByService[svc] = res
		return res, nil
	}
	lookup := func(svc string) []string {
		res, err := resolve(svc)
		if err != nil {
			return nil
		}
		return config.CascadeTargets(res.Tree)
	}
	var cascadeEventErr error
	cascadeCfg := app.CascadeConfig{
		Lookup: lookup,
		Operate: func(ctx context.Context, svc, action string) (operation.Result, error) {
			res, err := resolve(svc)
			if err != nil {
				return operation.Result{}, err
			}
			return a.operateWithManualState(ctx, opts, cfg, res, svc, action, actionStore, runner)
		},
		Target: func(svc string, out operation.Result, err error) {
			if actionStore != nil {
				if recordErr := recordManualActionEvent(ctx, actionStore, app.CascadeEventRecord(service, out)); recordErr != nil && cascadeEventErr == nil {
					cascadeEventErr = fmt.Errorf("record cascade event for %s: %w", svc, recordErr)
				}
			}
			if err != nil {
				fmt.Fprintf(a.Stderr, "cascade %s: %v\n", svc, err)
			} else if !opts.quiet {
				fmt.Fprintf(a.Stdout, "cascade %s: %s %s\n", svc, action, out.Status)
			}
			if err == nil {
				a.notifyInteractiveBlockedAction(ctx, out)
			}
		},
	}
	primary, primaryErr := app.RunCascade(ctx, service, action, cascadeCfg)
	if primaryErr == nil && cascadeEventErr != nil {
		primaryErr = cascadeEventErr
	}
	return primary, primaryErr
}

// operateWithManualState runs one manual service action and records its
// monitoring and settling transition. The direct and cascade paths share it so
// every action, including manual-only repair, has identical post-operation
// state handling.
func (a App) operateWithManualState(ctx context.Context, opts options, cfg *config.Config, resolved config.Resolved, service, action string, actionStore *state.Store, runner manualOperationRunner) (operation.Result, error) {
	a.beginManualOperationSettling(cfg, actionStore, service, action)
	result, err := runner.operate(ctx, opts, cfg, resolved, service, action)
	activeAfterPostflightFailure := runner.activeAfter(ctx, opts, cfg, resolved, service, action, result, err)
	a.finishManualOperationSettling(cfg, actionStore, service, action, result, err, activeAfterPostflightFailure)
	return result, err
}

// openStateStore opens the persistent state store under paths.state. It passes
// the engine's cache and retention settings so sermoctl reads history through the
// same resolution ladder the daemon writes it with.
func openStateStore(ctx context.Context, cfg *config.Config) (*state.Store, error) {
	//nolint:wrapcheck // each command prefixes its own "<verb> failed:" context.
	return state.OpenContextWith(
		ctx,
		filepath.Join(cfg.Global.StateDir(), state.Filename),
		app.EngineStateOptions(cfg),
	)
}

// withStateStore opens the store, runs fn, and always closes the store. onOpenErr
// maps an open failure to a command exit code (typically a.fail with a prefix).
// Prefer this over openStateStore + defer Close at each call site.
func withStateStore(ctx context.Context, cfg *config.Config, onOpenErr func(error) int, fn func(*state.Store) int) int {
	store, err := openStateStore(ctx, cfg)
	if err != nil {
		return onOpenErr(err)
	}
	defer func() { _ = store.Close() }()
	return fn(store)
}

func (a App) beginManualOperationSettling(cfg *config.Config, store *state.Store, service, action string) {
	if store == nil {
		return
	}
	if err := app.BeginOperationSettling(store, service, action); err != nil {
		msg := err.Error()
		fmt.Fprintf(a.Stderr, cliWarningFormat, msg)
		a.recordAccess(cfg, action+"-settling", service, accessStatusError, msg)
	}
}

func (a App) finishManualOperationSettling(cfg *config.Config, store *state.Store, service, action string, result operation.Result, opErr error, activeAfterPostflightFailure bool) {
	if store == nil {
		return
	}
	change, err := app.CompleteManualOperation(store, store, service, action, result, opErr,
		app.ManualOperationSources{Stop: state.SourceCLIManualStop, Restore: state.SourceCLI}, activeAfterPostflightFailure)
	if err != nil {
		msg := err.Error()
		fmt.Fprintf(a.Stderr, cliWarningFormat, msg)
		a.recordAccess(cfg, action+"-settling", service, accessStatusError, msg)
	}
	if change.Changed {
		a.recordAccess(cfg, change.Action, service, accessStatusOK, change.Message)
	}
}

// Manual operations share the state database with a running sermod; on a
// loaded host the daemon's write transactions can hold the SQLite writer lock
// beyond one busy-timeout window, and a single failed insert used to turn a
// completed operation into a bare "database is locked" error. The audit write
// therefore retries inside a bounded window; each attempt already waits the
// store's own busy timeout.
const (
	manualAuditRetryWindow = 30 * time.Second
	manualAuditRetryPause  = 2 * time.Second
)

// manualActionRecorder is the slice of the state store the manual audit path
// needs; it keeps the retry logic testable without a real database.
type manualActionRecorder interface {
	RecordEvent(record state.EventRecord) (int64, error)
}

// recordManualActionEvent writes one manual operation's audit record, retrying
// through transient contention with the daemon. Persistent failure is still an
// error: an executed action without its auditable outcome must fail loudly.
func recordManualActionEvent(ctx context.Context, store manualActionRecorder, rec state.EventRecord) error {
	deadline := time.Now().Add(manualAuditRetryWindow)
	for {
		_, err := store.RecordEvent(rec)
		if err == nil {
			return nil
		}
		if !state.IsSQLiteContention(err) {
			return fmt.Errorf("record manual action event: %w", err)
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			return fmt.Errorf("record manual action event after retries: %w", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("record manual action event after retries: %w", err)
		case <-time.After(manualAuditRetryPause):
		}
	}
}

// runEngineAction dispatches one CLI action on a built engine. Everything the
// rule vocabulary knows goes through Engine.Do; reap does not, deliberately —
// it is a manual-only verb with its own --apply gate, so it must be named here
// rather than reachable from an action string a rule could produce.
func runEngineAction(ctx context.Context, engine operation.Engine, opts options, action string) operation.Result {
	if action == actionReap {
		return engine.Reap(ctx, opts.apply)
	}
	return engine.Do(ctx, action)
}

func (a App) printOperation(opts options, r operation.Result) {
	if r.Action == actionReap {
		a.printReap(opts, r)
		return
	}
	switch r.Status {
	case operation.ResultOK:
		fmt.Fprintf(a.Stdout, "%s %s ok\n", r.Service, r.Action)
		// A successful op may still carry a best-effort warning (an also_service
		// unit that failed to stop, a stale artifact left behind) folded into the
		// message after the bare "<action> ok"; surface it instead of dropping it.
		if note := strings.TrimSpace(strings.TrimPrefix(r.Message, r.Action+" ok")); note != "" {
			fmt.Fprintf(a.Stdout, cliWarningFormat, note)
		}
	case operation.ResultBlocked:
		fmt.Fprintf(a.Stdout, "BLOCKED %s %s\n", r.Service, r.Action)
		if r.Message != "" {
			fmt.Fprintf(a.Stdout, "reason: %s\n", r.Message)
		}
	default:
		fmt.Fprintf(a.Stdout, "%s %s %s\n", r.Service, r.Action, r.Status)
		if r.Message != "" {
			fmt.Fprintf(a.Stdout, "reason: %s\n", r.Message)
		}
	}
	for _, c := range r.Checks {
		if !c.OK {
			fmt.Fprintf(a.Stdout, "  check %s failed: %s\n", c.Check, c.Message)
		}
	}
	for _, p := range r.Processes {
		key, value := processDisplayField(p)
		// Flag the strays: an operator staring at "residual pid=… exe=…" would
		// otherwise go looking for the selector that failed to cover it, and for a
		// stray there is none to find.
		stray := ""
		if p.Stray {
			stray = " stray=true"
		}
		fmt.Fprintf(a.Stdout, "  residual pid=%d %s=%s%s\n", p.PID, key, value, stray)
	}
}

// printReap renders a reap. Its listed processes are strays, not residuals of a
// stop, and without --apply nothing was touched — so it says so, and says how to
// ask for it, instead of reading like a completed operation.
func (a App) printReap(opts options, r operation.Result) {
	if r.Status == operation.ResultBlocked {
		fmt.Fprintf(a.Stdout, "BLOCKED %s %s\n", r.Service, r.Action)
	} else {
		fmt.Fprintf(a.Stdout, "%s %s %s\n", r.Service, r.Action, r.Status)
	}
	if r.Message != "" {
		fmt.Fprintf(a.Stdout, "reason: %s\n", r.Message)
	}
	for _, p := range r.Processes {
		key, value := processDisplayField(p)
		fmt.Fprintf(a.Stdout, "  stray pid=%d user=%s %s=%s\n", p.PID, orUnknown(p.User), key, value)
	}
	if !opts.apply && len(r.Processes) > 0 {
		fmt.Fprintf(a.Stdout, "nothing was signalled; run `sermoctl %s %s --%s` to signal the authorized strays\n",
			commandReap, r.Service, cliFlagApply)
	}
}

// operationExit maps an operation result status to a process exit code.
func operationExit(status operation.ResultStatus) int {
	switch status {
	case operation.ResultOK:
		return exitSuccess
	case operation.ResultBlocked:
		return exitBlocked
	case operation.ResultFailed:
		return exitRuntimeError
	default: // preflight_failed, postflight_failed, orphan_processes
		return exitNotActive
	}
}
