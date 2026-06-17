package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"sermo/internal/app"
	"sermo/internal/assist"
	"sermo/internal/buildinfo"
	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/control"
	"sermo/internal/execx"
	"sermo/internal/locks"
	"sermo/internal/mountctl"
	"sermo/internal/notify"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
)

const (
	exitSuccess       = 0
	exitNotActive     = 1
	exitRuntimeError  = 2
	exitUsage         = 64
	exitBlocked       = 75
	exitConfigInvalid = 78
)

// BackendDetector detects the service manager backend.
type BackendDetector interface {
	Detect(ctx context.Context, requested servicemgr.Backend) (servicemgr.Detection, error)
}

// App contains dependencies for the sermoctl CLI.
type App struct {
	Detector   BackendDetector
	NewManager func(servicemgr.Backend) (servicemgr.Manager, error)
	LoadConfig func(globalPath string, opts ...config.Option) (*config.Config, error)
	Discover   func(selectors []process.Selector) ([]process.Process, []string)
	// Operate runs a start/stop/restart/reload/resume through the operation engine for a
	// resolved service. Injectable for testing; the error covers backend/wiring
	// failures (the Result carries operational outcomes).
	Operate func(ctx context.Context, opts options, cfg *config.Config, resolved config.Resolved, service, action string) (operation.Result, error)
	Env     func(string) string
	Stdout  io.Writer
	Stderr  io.Writer
	// Stdin is the interactive input source, used by `wizard`. Injectable for
	// testing; defaults to os.Stdin.
	Stdin io.Reader
	// wizardEnvFunc overrides the host facts (volumes/interfaces/notifiers) the
	// wizard offers. nil uses the real host; tests set it for hermetic runs.
	wizardEnvFunc func(*config.Config) assist.Env
	// Runner executes external commands (e.g. an app's version command for the
	// `apps` command). Injectable for testing; defaults to the real runner.
	Runner execx.Runner
	// FindPID locates running PIDs by program name, used by `reload` to find the
	// daemon when no pidfile is present. Injectable for testing; defaults to a
	// native /proc scan (process.PIDsByComm).
	FindPID func(name string) ([]int, error)
	// pidfileFallbacks lists absolute pidfile locations `reload` searches after
	// the configured runtime dir when resolving the daemon. nil selects the
	// production defaults; tests set it (often empty) to keep pidfile discovery
	// hermetic instead of reading the host's /run/sermo/sermod.pid.
	pidfileFallbacks []string
	// FetchEvents is injectable for `sermoctl events` (listing recent events via
	// the daemon web API). Defaults to fetching over HTTP using the config's web
	// address/port (and password for auth if present).
	FetchEvents func(ctx context.Context, opts options, service string, limit int) ([]event, error)
	// PruneEvents is injectable for `sermoctl events clear` and
	// `sermoctl activity clear`. Defaults to pruning the daemon's persisted event
	// feed over HTTP using the config's web address/port (and password for auth if
	// present).
	PruneEvents func(ctx context.Context, opts options, before time.Time) (int, error)
	// MountController builds the host mount controller for `sermoctl mount|umount`.
	// nil uses the real host commands and /proc readers.
	MountController func(*config.Config) mountctl.Controller
	// BuildNotifiers constructs delivery targets for ad-hoc CLI reports. nil
	// uses the configured notifiers without applying alert templates.
	BuildNotifiers func(*config.Config) (map[string]notify.Notifier, []string)
}

type options struct {
	backend   servicemgr.Backend
	json      bool
	quiet     bool
	noCascade bool // --no-cascade: act on exactly this service, skip also_apply
	help      bool
	timeout   time.Duration
	config    string
	command   string
	args      []string
	// lock command flags
	name        string
	reason      string
	ttl         time.Duration
	commandArgs []string // tokens after `--`
	// sla command flags
	series bool          // emit the per-minute availability series instead of a summary
	since  time.Duration // series lookback window (0 means the command's default)
	// apps/libs/services flags
	long        bool     // show the full raw version string instead of the short one
	notifyNames []string // --notify selection for `services` reports
	// events clear flag
	before string // --before for events clear (RFC3339 or duration)
	// events list flags
	eventLimit int
}

// event is a minimal struct for unmarshaling events from the daemon's /api/events
// (and per-service) endpoints. Matches the shape returned by web.Event / LoggedEvent.
type event struct {
	Time    string `json:"time"`
	Service string `json:"service"`
	Watch   string `json:"watch"`
	Kind    string `json:"kind"`
	Action  string `json:"action"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// globalPath returns the --config path, or the packaged default.
func (o options) globalPath() string {
	if o.config != "" {
		return o.config
	}
	return config.DefaultGlobalPath
}

// service returns the first positional argument after the command.
func (o options) service() string {
	if len(o.args) == 0 {
		return ""
	}
	return o.args[0]
}

// Main runs sermoctl using process IO.
func Main(ctx context.Context, args []string) int {
	app := App{
		Detector:   servicemgr.NewDetector(),
		NewManager: servicemgr.NewManager,
		LoadConfig: config.Load,
		Env:        os.Getenv,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		Stdin:      os.Stdin,
	}
	app.Operate = app.defaultOperate
	return app.Run(ctx, args)
}

// Run executes the CLI.
func (a App) Run(ctx context.Context, args []string) int {
	if a.Env == nil {
		a.Env = os.Getenv
	}
	if a.Stdout == nil {
		a.Stdout = io.Discard
	}
	if a.Stderr == nil {
		a.Stderr = io.Discard
	}
	if a.Detector == nil {
		a.Detector = servicemgr.NewDetector()
	}
	if a.NewManager == nil {
		a.NewManager = servicemgr.NewManager
	}
	if a.LoadConfig == nil {
		a.LoadConfig = config.Load
	}
	if a.Operate == nil {
		a.Operate = a.defaultOperate
	}
	if a.FetchEvents == nil {
		a.FetchEvents = a.fetchEvents
	}
	if a.PruneEvents == nil {
		a.PruneEvents = a.pruneDaemonEvents
	}
	if a.Runner == nil {
		a.Runner = execx.CommandRunner{}
	}
	if a.BuildNotifiers == nil {
		a.BuildNotifiers = buildReportNotifiers
	}

	for _, arg := range args {
		if arg == "--version" || arg == "-V" {
			fmt.Fprintln(a.Stdout, buildinfo.String())
			return exitSuccess
		}
	}
	if len(args) > 0 && args[0] == "version" {
		fmt.Fprintln(a.Stdout, buildinfo.String())
		return exitSuccess
	}

	opts, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(a.Stderr, "usage error: %v\n", err)
		writeUsage(a.Stderr)
		return exitUsage
	}
	if opts.help {
		if opts.command != "" {
			if !writeCommandUsage(a.Stdout, opts.command) {
				fmt.Fprintf(a.Stderr, "usage error: unknown help topic %q\n", opts.command)
				writeUsage(a.Stderr)
				return exitUsage
			}
		} else {
			writeUsage(a.Stdout)
		}
		return exitSuccess
	}
	if opts.timeout <= 0 {
		opts.timeout = defaultTimeout(opts.command)
	}
	if opts.backend == "" {
		envBackend, err := servicemgr.ParseBackend(a.Env("SERMO_BACKEND"))
		if err != nil {
			fmt.Fprintf(a.Stderr, "usage error: SERMO_BACKEND: %v\n", err)
			return exitUsage
		}
		opts.backend = envBackend
	}

	switch opts.command {
	case "help":
		return runHelp(a, opts)
	case "version":
		fmt.Fprintln(a.Stdout, buildinfo.String())
		return exitSuccess
	case "backend", "init":
		return a.runBackend(ctx, opts)
	case "status":
		return a.runStatus(ctx, opts)
	case "is-active":
		return a.runIsActive(ctx, opts)
	case "start", "stop", "restart", "resume":
		return a.runAction(ctx, opts, opts.command)
	case "mount":
		return a.runMount(ctx, opts)
	case "umount":
		return a.runUmount(ctx, opts)
	case "config":
		return a.runConfig(opts)
	case "locks":
		return a.runLocks(opts)
	case "processes":
		return a.runProcesses(opts)
	case "preflight":
		return a.runPreflight(ctx, opts)
	case "daemon":
		return a.runDaemon(ctx, opts)
	case "events":
		return a.runEvents(ctx, opts)
	case "activity":
		return a.runActivity(ctx, opts)
	case "apps":
		return a.runApps(ctx, opts)
	case "libs":
		return a.runLibs(ctx, opts)
	case "patterns":
		return a.runPatterns(opts)
	case "services":
		return a.runServices(ctx, opts)
	case "state":
		return a.runState(ctx, opts)
	case "lock":
		return a.runLock(ctx, opts)
	case "unmonitor":
		return a.runMonitor(opts, true)
	case "monitor":
		return a.runMonitor(opts, false)
	case "sla":
		return a.runSLA(opts)
	case "diagnose":
		return a.runDiagnose(opts)
	case "reload":
		if opts.service() == "" {
			return a.commandUsageError("reload", "reload requires a service name; use `sermoctl daemon reload` to reload sermod config")
		}
		return a.runAction(ctx, opts, "reload")
	case "wizard":
		return a.runWizard(ctx, opts)
	case "":
		fmt.Fprintln(a.Stderr, "usage error: missing command")
		writeUsage(a.Stderr)
		return exitUsage
	default:
		fmt.Fprintf(a.Stderr, "usage error: unknown command %q\n", opts.command)
		writeUsage(a.Stderr)
		return exitUsage
	}
}

func (a App) runBackend(ctx context.Context, opts options) int {
	if len(opts.args) > 0 {
		return a.commandUsageError(opts.command, fmt.Sprintf("%s takes no arguments", opts.command))
	}
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

	detection, err := a.Detector.Detect(ctx, opts.backend)
	if err != nil {
		if opts.json {
			writeJSON(a.Stdout, map[string]string{"error": err.Error()})
		} else {
			fmt.Fprintf(a.Stderr, "backend detection failed: %v\n", err)
		}
		return exitRuntimeError
	}

	if opts.json {
		writeJSON(a.Stdout, map[string]string{"backend": string(detection.Backend)})
		return exitSuccess
	}

	fmt.Fprintln(a.Stdout, detection.Backend)
	return exitSuccess
}

func (a App) runStatus(ctx context.Context, opts options) int {
	if opts.service() == "" {
		return a.commandUsageError("status", "status requires a service name")
	}
	if len(opts.args) > 1 {
		return a.commandUsageError("status", "status takes exactly one service name")
	}

	status, code := a.serviceStatus(ctx, opts)
	if code != exitSuccess {
		return code
	}

	mon := a.serviceMonitorState(opts)
	if opts.json {
		writeJSON(a.Stdout, statusToJSON(status, mon))
		return exitSuccess
	}

	state := app.ServiceState(mon.Enabled, mon.Monitored(), string(status.Status), "")
	fmt.Fprintf(a.Stdout, "%s state=%s backend=%s service=%s%s\n",
		status.Service, state, status.Backend, status.Unit, formatStateMetadata(mon))
	return exitSuccess
}

// monitorView is the persisted monitoring metadata shown by status and monitor.
type monitorView struct {
	Configured bool
	Enabled    bool
	Paused     bool
	Source     string
	ChangedAt  string // RFC3339 when set
}

func (m monitorView) Monitored() bool {
	return m.Configured && m.Enabled && !m.Paused
}

// serviceMonitorState reads a service's monitoring row from the state store. It is
// best-effort: status works without config, so a missing config or store yields
// an empty view (not paused).
func (a App) serviceMonitorState(opts options) monitorView {
	view := monitorView{Enabled: true}
	globalPath := opts.globalPath()
	cfg, err := a.LoadConfig(globalPath)
	if err != nil {
		return view
	}
	if _, ok := cfg.Services[opts.service()]; ok {
		view.Configured = true
		if resolved, errs := cfg.Resolve(opts.service()); len(errs) == 0 {
			if cfgval.Disabled(resolved.Tree) {
				view.Enabled = false
				view.Paused = true
			}
			if mode, _ := resolved.Tree["monitor"].(string); mode == config.MonitorDisabled {
				view.Paused = true
			}
		}
	}
	store, err := state.Open(filepath.Join(cfg.Global.StateDir(), state.Filename))
	if err != nil {
		return view
	}
	defer store.Close()
	rec, found, err := store.MonitorState(opts.service())
	if err != nil || !found {
		return view
	}
	view.Paused = !rec.Active
	view.Source = rec.Source
	if !rec.UpdatedAt.IsZero() {
		view.ChangedAt = rec.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return view
}

func formatStateMetadata(mon monitorView) string {
	return metaSuffix(mon.Source, mon.ChangedAt)
}

// metaSuffix renders the optional " source=… changed=…" trailer shared by the
// status line and the monitor pause/resume messages. Empty fields are omitted;
// an all-empty result is the empty string (no leading space).
func metaSuffix(source, changedAt string) string {
	var parts []string
	if source != "" {
		parts = append(parts, "source="+source)
	}
	if changedAt != "" {
		parts = append(parts, "changed="+changedAt)
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
}

func (a App) runIsActive(ctx context.Context, opts options) int {
	if opts.service() == "" {
		return a.commandUsageError("is-active", "is-active requires a service name")
	}
	if len(opts.args) > 1 {
		return a.commandUsageError("is-active", "is-active takes exactly one service name")
	}

	status, code := a.serviceStatus(ctx, opts)
	if code != exitSuccess {
		return code
	}

	switch {
	case opts.json:
		writeJSON(a.Stdout, statusToJSON(status, a.serviceMonitorState(opts)))
	case !opts.quiet:
		fmt.Fprintln(a.Stdout, status.Status)
	}

	if status.Status == servicemgr.StatusActive {
		return exitSuccess
	}
	return exitNotActive
}

// runAction performs a start/stop/restart/reload/resume through the safe operation engine
// (section 18): the resolved service is run under the internal operation lock,
// active named runtime locks, required preflight, guards, residual-process
// handling and postflight. Manual sermoctl actions are not rate limited, but are
// fully guarded (section 16).
func (a App) runAction(ctx context.Context, opts options, action string) int {
	if opts.service() == "" {
		return a.commandUsageError(action, fmt.Sprintf("%s requires a service name", action))
	}
	if len(opts.args) > 1 {
		return a.commandUsageError(action, fmt.Sprintf("%s takes exactly one service name", action))
	}
	service := opts.service()

	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	if code := a.requireService(opts, cfg, service); code != exitSuccess {
		return code
	}
	resolved, code := a.resolveService(opts, cfg, service)
	if code != exitSuccess {
		return code
	}

	result, err := a.operateWithCascade(ctx, opts, cfg, resolved, service, action)
	if err != nil {
		return a.fail(opts, err.Error())
	}

	if opts.json {
		writeJSON(a.Stdout, result)
	} else if !opts.quiet {
		a.printOperation(result)
	}
	return operationExit(result.Status)
}

// operateWithCascade runs the action on the primary service, and — unless
// --no-cascade — on the services it lists in also_apply, in dependency order
// (start/restart: primary first; stop: additionals first). Targets run through
// their own guarded operation; each target's result is printed. The primary's
// result is returned and drives the exit code.
func (a App) operateWithCascade(ctx context.Context, opts options, cfg *config.Config, resolved config.Resolved, service, action string) (operation.Result, error) {
	targets := config.CascadeTargets(resolved.Tree)
	// also_apply cascades only start/stop/restart, not reload/resume.
	if opts.noCascade || action == "reload" || action == "resume" || len(targets) == 0 {
		return a.Operate(ctx, opts, cfg, resolved, service, action)
	}
	lookup := func(svc string) []string {
		r, errs := cfg.Resolve(svc)
		if len(errs) > 0 {
			return nil
		}
		return config.CascadeTargets(r.Tree)
	}
	seq := app.OrderedGroup(service, action, lookup, map[string]bool{}, 0)
	var primary operation.Result
	var primaryErr error
	for _, svc := range seq {
		res := resolved
		if svc != service {
			r, errs := cfg.Resolve(svc)
			if len(errs) > 0 {
				fmt.Fprintf(a.Stderr, "cascade %s: skipped (%s)\n", svc, errs[0])
				continue
			}
			res = r
		}
		out, err := a.Operate(ctx, opts, cfg, res, svc, action)
		if svc == service {
			primary, primaryErr = out, err
			continue
		}
		if err != nil {
			fmt.Fprintf(a.Stderr, "cascade %s: %v\n", svc, err)
		} else if !opts.quiet {
			fmt.Fprintf(a.Stdout, "cascade %s: %s %s\n", svc, action, out.Status)
		}
	}
	return primary, primaryErr
}

// defaultOperate wires the real operation engine from a resolved service and
// runs the requested action.
func (a App) defaultOperate(ctx context.Context, opts options, cfg *config.Config, resolved config.Resolved, service, action string) (operation.Result, error) {
	detection, err := a.Detector.Detect(ctx, opts.backend)
	if err != nil {
		return operation.Result{}, fmt.Errorf("backend detection failed: %v", err)
	}
	manager, err := a.NewManager(detection.Backend)
	if err != nil {
		return operation.Result{}, fmt.Errorf("service manager unavailable: %v", err)
	}

	resolver := servicemgr.NewUnitResolver()
	resolver.Manager = manager
	target, err := control.Resolve(ctx, service, resolved.Tree, detection.Backend, manager, resolver)
	if err != nil {
		return operation.Result{}, err
	}

	runtime := cfg.Global.RuntimeDir()
	locker := locks.NewOperationLocker(filepath.Join(runtime, "ops"))
	locker.OnReclaim = func(service, reason string) {
		fmt.Fprintf(a.Stderr, "reclaimed stale operation lock for %s (%s)\n", service, reason)
	}
	discoverer := process.NewDiscovererWithUserLookup(app.EngineUserLookup(cfg, a.Runner))
	if backendPIDs := backendPIDsForTarget(target, a.Runner); backendPIDs != nil {
		discoverer.BackendPIDs = backendPIDs
	}
	engine := operation.New(operation.Config{
		Service:          service,
		Unit:             target.Unit,
		Backend:          string(target.Backend),
		Tree:             resolved.Tree,
		Manager:          target.Manager,
		Locker:           &locker,
		Scanner:          locks.NewScanner(filepath.Join(runtime, "locks")),
		Discoverer:       discoverer,
		ResolveUser:      discoverer.ResolveUser,
		CheckDeps:        checks.Deps{DefaultTimeout: engineDefaultTimeout(cfg)},
		OperationTimeout: operation.ResolveTimeout(opts.timeout, resolved.Tree),
	})

	opTimeout := operation.ResolveTimeout(opts.timeout, resolved.Tree)
	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	gate := app.NewOpGate(app.OpSlotsFromConfig(cfg), cfg.Global.RuntimeDir())
	result := gate.Run(opCtx, service, action, func(ctx context.Context) operation.Result {
		return engine.Do(ctx, action)
	})
	if result.Message == "unknown action" && result.Status == operation.ResultFailed {
		return operation.Result{}, fmt.Errorf("unknown action %q", action)
	}
	return result, nil
}

func (a App) printOperation(r operation.Result) {
	switch r.Status {
	case operation.ResultOK:
		fmt.Fprintf(a.Stdout, "%s %s ok\n", r.Service, r.Action)
		// A successful op may still carry a best-effort warning (an also_service
		// unit that failed to stop, a stale artifact left behind) folded into the
		// message after the bare "<action> ok"; surface it instead of dropping it.
		if note := strings.TrimSpace(strings.TrimPrefix(r.Message, r.Action+" ok")); note != "" {
			fmt.Fprintf(a.Stdout, "warning: %s\n", note)
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
		fmt.Fprintf(a.Stdout, "  residual pid=%d %s=%s\n", p.PID, key, value)
	}
}

// operationExit maps an operation result status to a process exit code (§23).
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

// runConfig dispatches the `config` subcommands.
func (a App) runConfig(opts options) int {
	if len(opts.args) == 0 {
		return a.commandUsageError("config", "config requires a subcommand (validate)")
	}

	sub := opts.args[0]
	rest := opts.args[1:]
	globalPath := opts.globalPath()

	switch sub {
	case "validate":
		return a.runConfigValidate(globalPath, rest, opts)
	default:
		return a.commandUsageError("config", fmt.Sprintf("unknown config subcommand %q", sub))
	}
}

func (a App) runConfigValidate(globalPath string, rest []string, opts options) int {
	if len(rest) > 0 {
		return a.commandUsageError("config", "config validate takes no service name; it validates the whole Sermo configuration")
	}

	cfg, err := a.LoadConfig(globalPath)
	if err != nil {
		return a.fail(opts, fmt.Sprintf("load config failed: %v", err))
	}

	issues := config.Validate(cfg)

	if len(issues) == 0 {
		switch {
		case opts.json:
			writeJSON(a.Stdout, map[string]any{"valid": true})
		case !opts.quiet:
			fmt.Fprintln(a.Stdout, "OK")
		}
		return exitSuccess
	}

	if opts.json {
		writeJSON(a.Stdout, map[string]any{"valid": false, "errors": issuesJSON(issues)})
	} else {
		a.printIssues(opts, issues)
	}
	return exitConfigInvalid
}

// printIssues writes validation findings in the section-30 ERROR format.
func (a App) printIssues(opts options, issues []config.Issue) {
	if opts.json {
		writeJSON(a.Stdout, map[string]any{"valid": false, "errors": issuesJSON(issues)})
		return
	}
	for _, is := range issues {
		fmt.Fprintf(a.Stderr, "ERROR %s:\n  %s\n", is.Scope, is.Msg)
	}
}

func scopedIssues(scope string, msgs []string) []config.Issue {
	issues := make([]config.Issue, 0, len(msgs))
	for _, m := range msgs {
		issues = append(issues, config.Issue{Scope: scope, Msg: m})
	}
	return issues
}

func issuesJSON(issues []config.Issue) []map[string]string {
	out := make([]map[string]string, 0, len(issues))
	for _, is := range issues {
		out = append(out, map[string]string{"scope": is.Scope, "error": is.Msg})
	}
	return out
}

// runPreflight resolves a service, builds its preflight checks and runs them
// under engine.default_timeout (section 19). A required check failure exits 1.
func (a App) runPreflight(ctx context.Context, opts options) int {
	if opts.service() == "" {
		return a.commandUsageError("preflight", "preflight requires a service name")
	}
	if len(opts.args) > 1 {
		return a.commandUsageError("preflight", "preflight takes exactly one service name")
	}
	service := opts.service()

	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	if code := a.requireService(opts, cfg, service); code != exitSuccess {
		return code
	}

	resolved, code := a.resolveService(opts, cfg, service)
	if code != exitSuccess {
		return code
	}

	section, _ := resolved.Tree["preflight"].(map[string]any)
	discoverer := process.NewDiscovererWithUserLookup(app.EngineUserLookup(cfg, a.Runner))
	deps := checks.Deps{
		Service:        service,
		DefaultTimeout: engineDefaultTimeout(cfg),
		Status:         a.statusFunc(opts, resolved.Tree, config.ServiceUnit(resolved.Tree, service)),
		Processes:      discoverer.ObserveState,
	}
	built, warnings := checks.Build(section, deps)
	for _, w := range warnings {
		fmt.Fprintf(a.Stderr, "warning: %s\n", w)
	}

	ctx, cancel := context.WithTimeout(ctx, app.PreflightDeadline(deps.DefaultTimeout))
	defer cancel()
	results := checks.Run(ctx, built, 0)
	outcome := checks.Evaluate(results)

	if opts.json {
		writeJSON(a.Stdout, map[string]any{"service": service, "ok": outcome.OK, "checks": results})
	} else {
		a.printPreflight(service, outcome)
	}

	if outcome.OK {
		return exitSuccess
	}
	return exitNotActive
}

func (a App) printPreflight(service string, outcome checks.Outcome) {
	overall := "OK"
	if !outcome.OK {
		overall = "FAIL"
	}
	if len(outcome.Results) == 0 {
		fmt.Fprintf(a.Stdout, "preflight %s: %s (no checks)\n", service, overall)
		return
	}
	fmt.Fprintf(a.Stdout, "preflight %s: %s\n", service, overall)
	for _, r := range outcome.Results {
		tag := "OK"
		if !r.OK {
			tag = "FAIL"
			if r.Optional {
				tag = "WARN"
			}
		}
		fmt.Fprintf(a.Stdout, "  %-4s %s: %s\n", tag, r.Check, r.Message)
	}
}

// statusFunc builds a lazy backend status query for `service` checks; it only
// detects the backend and resolves the unit (service candidates, section 11) when a service
// check actually runs.
func (a App) statusFunc(opts options, tree map[string]any, base string) func(context.Context) (servicemgr.Status, error) {
	return func(ctx context.Context) (servicemgr.Status, error) {
		detection, err := a.Detector.Detect(ctx, opts.backend)
		if err != nil {
			return "", err
		}
		manager, err := a.NewManager(detection.Backend)
		if err != nil {
			return "", err
		}
		resolver := servicemgr.NewUnitResolver()
		resolver.Manager = manager
		target, err := control.Resolve(ctx, base, tree, detection.Backend, manager, resolver)
		if err != nil {
			return "", err
		}
		status, err := target.Manager.Status(ctx, target.Unit)
		if err != nil {
			return "", err
		}
		return status.Status, nil
	}
}

func engineDefaultTimeout(cfg *config.Config) time.Duration {
	if engine, ok := cfg.Global.Raw["engine"].(map[string]any); ok {
		if s, _ := engine["default_timeout"].(string); s != "" {
			if d, err := time.ParseDuration(s); err == nil && d > 0 {
				return d
			}
		}
	}
	return 10 * time.Second
}

// runLocks reports the named runtime locks for a service (active, expired and
// stale), reading the runtime root from the loaded config (section 20).
func (a App) runLocks(opts options) int {
	if opts.service() == "" {
		return a.commandUsageError("locks", "locks requires a service name")
	}
	if len(opts.args) > 1 {
		return a.commandUsageError("locks", "locks takes exactly one service name")
	}

	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}

	dir := filepath.Join(cfg.Global.RuntimeDir(), "locks")
	report, err := locks.NewScanner(dir).Scan(opts.service())
	if err != nil {
		return a.fail(opts, fmt.Sprintf("scan locks failed: %v", err))
	}

	for _, w := range report.Warnings {
		fmt.Fprintf(a.Stderr, "warning: %s\n", w)
	}

	if opts.json {
		writeJSON(a.Stdout, map[string]any{
			"service": report.Service,
			"locks":   report.Locks,
		})
		return exitSuccess
	}

	if len(report.Locks) == 0 {
		if !opts.quiet {
			fmt.Fprintf(a.Stdout, "no named runtime locks for %s\n", report.Service)
		}
		return exitSuccess
	}
	for _, lock := range report.Locks {
		fmt.Fprintln(a.Stdout, formatLock(lock))
	}
	return exitSuccess
}

func formatLock(lock locks.Lock) string {
	id := lock.Service
	if lock.Name != "" {
		id += "." + lock.Name
	}
	line := fmt.Sprintf("%s %s owner_pid=%d", id, lock.State, lock.OwnerPID)
	if !lock.ExpiresAt.IsZero() {
		line += " expires_at=" + lock.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if lock.StaleReason != "" {
		line += " (" + lock.StaleReason + ")"
	}
	if lock.Reason != "" {
		line += fmt.Sprintf(" reason=%q", lock.Reason)
	}
	return line
}

// runProcesses discovers and reports the processes belonging to a service
// (section 21), reading the service's `processes` selectors from resolved config.
func (a App) runProcesses(opts options) int {
	if opts.service() == "" {
		return a.commandUsageError("processes", "processes requires a service name")
	}
	if len(opts.args) > 1 {
		return a.commandUsageError("processes", "processes takes exactly one service name")
	}
	service := opts.service()

	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	if code := a.requireService(opts, cfg, service); code != exitSuccess {
		return code
	}

	resolved, code := a.resolveService(opts, cfg, service)
	if code != exitSuccess {
		return code
	}

	selectors, warnings := process.ParseSelectors(resolved.Tree)
	procs, discWarnings := a.discoverProcesses(context.Background(), opts, cfg, resolved, service, selectors)
	warnings = append(warnings, discWarnings...)

	for _, w := range warnings {
		fmt.Fprintf(a.Stderr, "warning: %s\n", w)
	}

	if opts.json {
		writeJSON(a.Stdout, map[string]any{"service": service, "processes": procs})
		return exitSuccess
	}

	if len(procs) == 0 {
		if !opts.quiet {
			fmt.Fprintf(a.Stdout, "no processes found for %s\n", service)
		}
		return exitSuccess
	}
	for _, p := range procs {
		fmt.Fprintln(a.Stdout, formatProcess(p))
	}
	return exitSuccess
}

func (a App) discoverProcesses(ctx context.Context, opts options, cfg *config.Config, resolved config.Resolved, service string, selectors []process.Selector) ([]process.Process, []string) {
	if a.Discover != nil {
		return a.Discover(selectors)
	}
	discoverer := process.NewDiscovererWithUserLookup(app.EngineUserLookup(cfg, a.Runner))
	detection, err := a.Detector.Detect(ctx, opts.backend)
	if err != nil {
		return discoverer.Discover(selectors)
	}
	manager, err := a.NewManager(detection.Backend)
	if err != nil {
		return discoverer.Discover(selectors)
	}
	target, err := control.Resolve(ctx, service, resolved.Tree, detection.Backend, manager, servicemgr.UnitResolver{Runner: a.Runner, Manager: manager})
	if err != nil {
		return discoverer.Discover(selectors)
	}
	if backendPIDs := backendPIDsForTarget(target, a.Runner); backendPIDs != nil {
		discoverer.BackendPIDs = backendPIDs
	}
	return discoverer.Discover(selectors)
}

func backendPIDsForTarget(target control.Target, runner execx.Runner) func() []int {
	if target.BackendPIDs != nil {
		return target.BackendPIDs
	}
	switch target.Backend {
	case servicemgr.BackendSystemd, servicemgr.BackendOpenRC:
		return servicemgr.BackendPIDsFuncWithRunner(target.Backend, target.Unit, runner, nil)
	default:
		return nil
	}
}

func formatProcess(p process.Process) string {
	key, value := processDisplayField(p)
	line := fmt.Sprintf("pid=%d ppid=%d user=%s %s=%s source=%s", p.PID, p.PPID, orUnknown(p.User), key, value, p.Source)
	if p.Role != "" {
		line += " role=" + p.Role
	}
	return line
}

func processDisplayField(p process.Process) (key, value string) {
	if p.ExeOK && p.Exe != "" {
		return "exe", p.Exe
	}
	if cmd := strings.TrimSpace(strings.Join(p.Cmdline, " ")); cmd != "" {
		return "cmd", strconv.Quote(cmd)
	}
	if p.Exe != "" {
		return "exe", p.Exe
	}
	return "exe", "unknown"
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// serviceStatus resolves the backend, builds a manager and queries the service.
// On any failure it reports the error and returns a non-success exit code.
func (a App) serviceStatus(ctx context.Context, opts options) (servicemgr.ServiceStatus, int) {
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

	if status, code, ok := a.configuredServiceStatus(ctx, opts); ok {
		return status, code
	}
	if a.serviceNameRejectedByConfig(opts) {
		a.reportError(opts, fmt.Sprintf("unknown service %q", opts.service()))
		return servicemgr.ServiceStatus{}, exitRuntimeError
	}

	detection, err := a.Detector.Detect(ctx, opts.backend)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("backend detection failed: %v", err))
		return servicemgr.ServiceStatus{}, exitRuntimeError
	}

	manager, err := a.NewManager(detection.Backend)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("service manager unavailable: %v", err))
		return servicemgr.ServiceStatus{}, exitRuntimeError
	}

	status, err := manager.Status(ctx, opts.service())
	if err != nil {
		a.reportError(opts, fmt.Sprintf("status query failed: %v", err))
		return servicemgr.ServiceStatus{}, exitRuntimeError
	}
	return status, exitSuccess
}

func (a App) serviceNameRejectedByConfig(opts options) bool {
	cfg, err := a.LoadConfig(opts.globalPath())
	if err != nil {
		return false
	}
	_, ok := cfg.Services[opts.service()]
	return len(cfg.Services) > 0 && !ok
}

func (a App) configuredServiceStatus(ctx context.Context, opts options) (servicemgr.ServiceStatus, int, bool) {
	cfg, err := a.LoadConfig(opts.globalPath())
	if err != nil {
		return servicemgr.ServiceStatus{}, 0, false
	}
	if _, ok := cfg.Services[opts.service()]; !ok {
		return servicemgr.ServiceStatus{}, 0, false
	}
	resolved, errs := cfg.Resolve(opts.service())
	if len(errs) > 0 {
		a.reportError(opts, fmt.Sprintf("config resolve failed: %v", errs[0]))
		return servicemgr.ServiceStatus{}, exitRuntimeError, true
	}
	if _, controlled := resolved.Tree["control"]; !controlled {
		return servicemgr.ServiceStatus{}, 0, false
	}
	detection, err := a.Detector.Detect(ctx, opts.backend)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("backend detection failed: %v", err))
		return servicemgr.ServiceStatus{}, exitRuntimeError, true
	}
	manager, err := a.NewManager(detection.Backend)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("service manager unavailable: %v", err))
		return servicemgr.ServiceStatus{}, exitRuntimeError, true
	}
	resolver := servicemgr.NewUnitResolver()
	resolver.Manager = manager
	target, err := control.Resolve(ctx, opts.service(), resolved.Tree, detection.Backend, manager, resolver)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("control target failed: %v", err))
		return servicemgr.ServiceStatus{}, exitRuntimeError, true
	}
	status, err := target.Manager.Status(ctx, opts.service())
	if err != nil {
		a.reportError(opts, fmt.Sprintf("status query failed: %v", err))
		return servicemgr.ServiceStatus{}, exitRuntimeError, true
	}
	return status, exitSuccess, true
}

func (a App) reportError(opts options, msg string) {
	if opts.json {
		writeJSON(a.Stdout, map[string]string{"error": msg})
		return
	}
	fmt.Fprintln(a.Stderr, msg)
}

// fail reports msg and returns the runtime-error exit code — the pairing almost
// every command's error path uses. Commands whose error path returns extra
// values (or a different exit code) keep calling reportError directly.
func (a App) fail(opts options, msg string) int {
	a.reportError(opts, msg)
	return exitRuntimeError
}

type statusJSON struct {
	Service          string `json:"service"`
	State            string `json:"state"`
	Backend          string `json:"backend"`
	Status           string `json:"status"`
	Unit             string `json:"unit"`
	Paused           bool   `json:"paused"`
	MonitorSource    string `json:"monitor_source,omitempty"`
	MonitorChangedAt string `json:"monitor_changed_at,omitempty"`
}

// defaultTimeout returns the per-command outer deadline used when --timeout is
// not given. Backend actions can legitimately take much longer than a probe.
func defaultTimeout(command string) time.Duration {
	switch command {
	case "start", "stop", "restart", "reload", "resume", "mount", "umount", "state":
		return 90 * time.Second
	case "services":
		return 30 * time.Second
	default:
		return 2 * time.Second
	}
}

func statusToJSON(status servicemgr.ServiceStatus, mon monitorView) statusJSON {
	out := statusJSON{
		Service: status.Service,
		State:   app.ServiceState(mon.Enabled, mon.Monitored(), string(status.Status), ""),
		Backend: string(status.Backend),
		Status:  string(status.Status),
		Unit:    status.Unit,
		Paused:  mon.Paused,
	}
	if mon.Paused {
		out.MonitorSource = mon.Source
		out.MonitorChangedAt = mon.ChangedAt
	}
	return out
}

// runEvents dispatches the events subcommands.
// - `sermoctl events [SERVICE] [--limit N]` lists recent events (global or for a service) via the daemon's web API.
// - `sermoctl events clear [--before TIME]` clears (all or events before a given time).
func (a App) runEvents(ctx context.Context, opts options) int {
	args := opts.args
	if len(args) > 0 && args[0] == "clear" {
		if len(args) > 1 {
			return a.commandUsageError("events", "events clear accepts only optional --before TIME")
		}
		return a.runEventsClear(ctx, opts, "events")
	}
	if len(args) > 1 {
		return a.commandUsageError("events", "events accepts at most one service name")
	}

	// list mode: `sermoctl events [SERVICE] [--limit N]`
	limit := 50
	if opts.eventLimit > 0 {
		limit = opts.eventLimit
	}
	service := ""
	if len(args) > 0 {
		service = args[0]
	}

	evs, err := a.FetchEvents(ctx, opts, service, limit)
	if err != nil {
		return a.fail(opts, err.Error())
	}
	if opts.json {
		writeJSON(a.Stdout, evs)
		return exitSuccess
	}

	if len(evs) == 0 {
		if service != "" {
			fmt.Fprintf(a.Stdout, "no recent events for %s\n", service)
		} else {
			fmt.Fprintln(a.Stdout, "no recent events")
		}
		return exitSuccess
	}

	fmt.Fprintln(a.Stdout, "TIME                 TARGET           KIND       ACTION   MESSAGE")
	for _, e := range evs {
		ts := e.Time
		if len(ts) >= 19 {
			ts = ts[:19]
		}
		target := e.Service
		if target == "" {
			target = e.Watch
		}
		if target == "" {
			target = "-"
		}
		if len(target) > 15 {
			target = target[:15]
		}
		kind := e.Kind
		if len(kind) > 8 {
			kind = kind[:8]
		}
		action := e.Action
		if action == "" {
			action = e.Status
		}
		if len(action) > 7 {
			action = action[:7]
		}
		msg := e.Message
		if len(msg) > 60 {
			msg = msg[:57] + "..."
		}
		fmt.Fprintf(a.Stdout, "%s  %-15s  %-8s  %-7s  %s\n", ts, target, kind, action, msg)
	}
	return exitSuccess
}

// runActivity dispatches activity subcommands. Activity is the dashboard's
// recent-events view, so clearing it uses the same daemon event-prune path.
func (a App) runActivity(ctx context.Context, opts options) int {
	if len(opts.args) > 0 && opts.args[0] == "clear" {
		if len(opts.args) > 1 {
			return a.commandUsageError("activity", "activity clear accepts only optional --before TIME")
		}
		return a.runEventsClear(ctx, opts, "activity entries")
	}
	return a.commandUsageError("activity", "activity supports only: clear [--before TIME]")
}

func (a App) runEventsClear(ctx context.Context, opts options, noun string) int {
	before, err := parseBefore(opts.before, time.Now)
	if err != nil {
		return a.fail(opts, err.Error())
	}
	pruneEvents := a.PruneEvents
	if pruneEvents == nil {
		pruneEvents = a.pruneDaemonEvents
	}
	n, err := pruneEvents(ctx, opts, before)
	if err != nil {
		return a.fail(opts, err.Error())
	}
	if opts.json {
		writeJSON(a.Stdout, map[string]any{"pruned": n})
	} else if before.IsZero() {
		fmt.Fprintf(a.Stdout, "cleared %d %s\n", n, noun)
	} else {
		fmt.Fprintf(a.Stdout, "cleared %d %s before %s\n", n, noun, before.Format(time.RFC3339))
	}
	return exitSuccess
}

func parseBefore(value string, now func() time.Time) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if d, err := time.ParseDuration(value); err == nil {
		return now().Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid --before: use RFC3339 (e.g. 2026-06-13T12:00:00Z) or duration (e.g. 1h, 30m)")
}

// pruneDaemonEvents performs the HTTP call to the running sermod's web API
// to prune its event log. It reads the web: address/port and any
// admin password from the shared config so local sermoctl can authenticate
// the same way the operator would via the UI.
func (a App) pruneDaemonEvents(ctx context.Context, opts options, before time.Time) (int, error) {
	cfg, code := a.loadConfig(opts)
	if code != exitSuccess || cfg == nil {
		return 0, fmt.Errorf("failed to load config")
	}
	base, err := webAPIBase(cfg)
	if err != nil {
		return 0, err
	}
	u := base + "/api/events/clear"
	if !before.IsZero() {
		u += "?before=" + before.Format(time.RFC3339)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-Sermo-CSRF", "1")

	// If the config declares an admin password, send Basic auth (any user + pw).
	if wraw, ok := cfg.Global.Raw["web"].(map[string]any); ok {
		if pw := cfgval.String(wraw["password"]); pw != "" {
			cred := base64.StdEncoding.EncodeToString([]byte("admin:" + pw))
			req.Header.Set("Authorization", "Basic "+cred)
		}
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("talking to daemon web UI: %w (is sermod running with web.port set?)", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("clear failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var res struct {
		OK     bool `json:"ok"`
		Pruned int  `json:"pruned"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		// some responses may be plain
		return 0, fmt.Errorf("unexpected response: %s", body)
	}
	return res.Pruned, nil
}

// fetchEvents (the default for App.FetchEvents) calls the daemon web API to retrieve
// recent events. If service != "", uses the per-service endpoint.
func (a App) fetchEvents(ctx context.Context, opts options, service string, limit int) ([]event, error) {
	cfg, code := a.loadConfig(opts)
	if code != exitSuccess || cfg == nil {
		return nil, fmt.Errorf("failed to load config")
	}
	base, err := webAPIBase(cfg)
	if err != nil {
		return nil, err
	}

	var u string
	if service != "" {
		u = fmt.Sprintf("%s/api/services/%s/events?limit=%d", base, service, limit)
	} else {
		u = fmt.Sprintf("%s/api/events?limit=%d", base, limit)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	// no CSRF needed for GET; add auth if configured
	if wraw, ok := cfg.Global.Raw["web"].(map[string]any); ok {
		if pw := cfgval.String(wraw["password"]); pw != "" {
			cred := base64.StdEncoding.EncodeToString([]byte("admin:" + pw))
			req.Header.Set("Authorization", "Basic "+cred)
		}
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("talking to daemon web UI: %w (is sermod running with web.port set?)", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("events fetch failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var evs []event
	if err := json.NewDecoder(resp.Body).Decode(&evs); err != nil {
		return nil, fmt.Errorf("decode events: %w", err)
	}
	return evs, nil
}

func webAPIBase(cfg *config.Config) (string, error) {
	wraw, _ := cfg.Global.Raw["web"].(map[string]any)
	if wraw == nil {
		return "", fmt.Errorf("web UI is not enabled in config (no web: block or no port); the event API is exposed by the running daemon")
	}
	addr := cfgval.String(wraw["address"])
	if addr == "" {
		addr = "127.0.0.1"
	}
	p, ok := cfgval.Int(wraw["port"])
	if !ok || p <= 0 {
		return "", fmt.Errorf("web.port is not set in config")
	}
	port := p
	return fmt.Sprintf("http://%s:%d", addr, port), nil
}

// runReload asks the running sermod to reload its configuration (SIGHUP
// equivalent). It prefers a pidfile written by the daemon under the configured
// runtime dir (or the legacy OpenRC location). If no pidfile is found it falls
// back to pidof/pgrep discovery. This works whether or not the web UI is enabled.
func (a App) runReload(ctx context.Context, opts options) int {
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}

	runtimeDir := cfg.Global.RuntimeDir()
	if runtimeDir == "" {
		runtimeDir = "/run/sermo"
	}

	fallbacks := a.pidfileFallbacks
	if fallbacks == nil {
		fallbacks = []string{
			"/run/sermo/sermod.pid",
			"/run/sermod.pid", // legacy from OpenRC packaging
		}
	}
	candidates := append([]string{filepath.Join(runtimeDir, "sermod.pid")}, fallbacks...)

	var pid int
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && n > 0 {
			pid = n
			break
		}
	}

	if pid == 0 {
		// Fallback: find a running sermod by program name. This is a native
		// /proc scan (process.PIDsByComm), not a pidof/pgrep shell-out — it
		// reads the world-readable /proc/<pid>/comm so it locates a root-owned
		// daemon without external binaries.
		find := a.FindPID
		if find == nil {
			find = process.PIDsByComm
		}
		if pids, err := find("sermod"); err == nil {
			for _, p := range pids {
				if p > 0 {
					pid = p
					break
				}
			}
		}
	}

	if pid <= 0 {
		return a.fail(opts, "could not find running sermod pid (no pidfile and no running sermod process)")
	}

	// Send SIGHUP. On Linux this is reliable for the daemon's signal handler.
	if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
		return a.fail(opts, fmt.Sprintf("failed to signal pid %d: %v", pid, err))
	}

	if opts.json {
		writeJSON(a.Stdout, map[string]any{"ok": true, "pid": pid})
	} else {
		fmt.Fprintf(a.Stdout, "reload signal (HUP) sent to sermod pid %d\n", pid)
	}
	return exitSuccess
}

func parseArgs(args []string) (options, error) {
	opts := options{backend: ""}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			opts.help = true
		case arg == "--json":
			opts.json = true
		case arg == "--quiet" || arg == "-q":
			opts.quiet = true
		case arg == "--no-cascade":
			opts.noCascade = true
		case arg == "--series":
			opts.series = true
		case arg == "--long":
			opts.long = true
		case isFlag(arg, "--notify"):
			v, ni, err := flagValue(args, i, "--notify")
			if err != nil {
				return opts, err
			}
			i = ni
			opts.notifyNames = append(opts.notifyNames, splitFlagList(v)...)
		case isFlag(arg, "--since"):
			v, ni, err := flagValue(args, i, "--since")
			if err != nil {
				return opts, err
			}
			i = ni
			if opts.since, err = time.ParseDuration(v); err != nil {
				return opts, fmt.Errorf("--since: %w", err)
			}
		case isFlag(arg, "--before"):
			v, ni, err := flagValue(args, i, "--before")
			if err != nil {
				return opts, err
			}
			i = ni
			opts.before = v
		case isFlag(arg, "--limit"):
			v, ni, err := flagValue(args, i, "--limit")
			if err != nil {
				return opts, err
			}
			i = ni
			if opts.eventLimit, err = strconv.Atoi(v); err != nil || opts.eventLimit < 0 {
				return opts, fmt.Errorf("--limit must be a non-negative integer")
			}
		case isFlag(arg, "--backend"):
			v, ni, err := flagValue(args, i, "--backend")
			if err != nil {
				return opts, err
			}
			i = ni
			if opts.backend, err = servicemgr.ParseBackend(v); err != nil {
				return opts, err
			}
		case isFlag(arg, "--timeout"):
			v, ni, err := flagValue(args, i, "--timeout")
			if err != nil {
				return opts, err
			}
			i = ni
			if opts.timeout, err = time.ParseDuration(v); err != nil {
				return opts, fmt.Errorf("--timeout: %w", err)
			}
		case isFlag(arg, "--config"):
			v, ni, err := flagValue(args, i, "--config")
			if err != nil {
				return opts, err
			}
			i, opts.config = ni, v
		case isFlag(arg, "--name"):
			v, ni, err := flagValue(args, i, "--name")
			if err != nil {
				return opts, err
			}
			i, opts.name = ni, v
		case isFlag(arg, "--reason"):
			v, ni, err := flagValue(args, i, "--reason")
			if err != nil {
				return opts, err
			}
			i, opts.reason = ni, v
		case isFlag(arg, "--ttl"):
			v, ni, err := flagValue(args, i, "--ttl")
			if err != nil {
				return opts, err
			}
			i = ni
			if opts.ttl, err = time.ParseDuration(v); err != nil {
				return opts, fmt.Errorf("--ttl: %w", err)
			}
		case arg == "--":
			// Everything after `--` is a literal command (the lock wrapper).
			opts.commandArgs = append(opts.commandArgs, args[i+1:]...)
			return opts, nil
		case strings.HasPrefix(arg, "-"):
			return opts, fmt.Errorf("unknown flag %s", arg)
		case opts.command == "":
			opts.command = arg
		default:
			opts.args = append(opts.args, arg)
		}
	}
	return opts, nil
}

// isFlag reports whether arg is the named value flag in either form: exactly
// `--flag` (value follows as the next arg) or `--flag=value` (inline).
func isFlag(arg, flag string) bool {
	return arg == flag || strings.HasPrefix(arg, flag+"=")
}

// flagValue extracts a value flag's value at args[i], handling both
// `--flag=value` (inline) and `--flag value` (next arg). It returns the value
// and the index to continue the scan from (advanced past a consumed next arg),
// or an error when the space form has no following value.
func flagValue(args []string, i int, flag string) (string, int, error) {
	if v, ok := strings.CutPrefix(args[i], flag+"="); ok {
		return v, i, nil
	}
	if i+1 >= len(args) {
		return "", i, fmt.Errorf("%s requires a value", flag)
	}
	return args[i+1], i + 1, nil
}

func writeJSON(w io.Writer, value any) {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}
