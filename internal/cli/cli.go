package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/pflag"

	"sermo/internal/app"
	"sermo/internal/assist"
	"sermo/internal/buildinfo"
	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/control"
	"sermo/internal/execx"
	"sermo/internal/httpx"
	"sermo/internal/locks"
	"sermo/internal/metrics"
	"sermo/internal/mountctl"
	"sermo/internal/notify"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/rules"
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

// Service action names dispatched by the CLI (each routes through the operation engine).
const (
	actionStart   = string(rules.ActionStart)
	actionStop    = string(rules.ActionStop)
	actionRestart = string(rules.ActionRestart)
	actionReload  = string(rules.ActionReload)
	actionResume  = string(rules.ActionResume)
)

const (
	reloadCapabilityTimeout    = 3 * time.Second
	defaultProbeCommandTimeout = 2 * time.Second
	defaultListCommandTimeout  = 30 * time.Second
	daemonWebClientTimeout     = 10 * time.Second
	defaultEventsListLimit     = 50
)

const (
	daemonProcessName            = "sermod"
	daemonWebSchemeHTTP          = checks.URLSchemeHTTP
	daemonWebAuthUserPrefix      = "admin:"
	daemonWebCSRFHeader          = "X-Sermo-Csrf"
	daemonWebCSRFValue           = "1"
	daemonWebHeaderAuthorization = httpx.HeaderAuthorization
	daemonWebBasicAuthPrefix     = "Basic "
	daemonAPIPathRoot            = "/api"
	daemonAPIPathApplications    = daemonAPIPathRoot + "/applications"
	daemonAPIPathEvents          = daemonAPIPathRoot + "/events"
	daemonAPIPathEventsClear     = daemonAPIPathEvents + "/clear"
	daemonAPIPathServices        = daemonAPIPathRoot + "/services"
	daemonAPIPathWatches         = daemonAPIPathRoot + "/watches"
	daemonAPIPathServiceEvents   = "/events"
	daemonAPIQueryBefore         = "before"
	daemonAPIQueryLimit          = "limit"
	cliUnknownServiceFormat      = "unknown service %q"
	cliWarningFormat             = "warning: %s\n"
	pflagUnknownFlagPrefix       = "unknown flag: "
)

const (
	cliFlagSetName   = "sermoctl"
	cliFlagBackend   = commandBackend
	cliFlagBefore    = daemonAPIQueryBefore
	cliFlagConfig    = commandConfig
	cliFlagConfirm   = "confirm"
	cliFlagForce     = "force"
	cliFlagHelp      = commandHelp
	cliFlagJSON      = "json"
	cliFlagKill      = "kill-blockers"
	cliFlagLazy      = "lazy"
	cliFlagLimit     = daemonAPIQueryLimit
	cliFlagLong      = "long"
	cliFlagName      = config.EntryKeyName
	cliFlagNoCascade = "no-cascade"
	cliFlagNotify    = rules.RuleFieldNotify
	cliFlagQuiet     = "quiet"
	cliFlagReason    = "reason"
	cliFlagSeries    = "series"
	cliFlagSince     = "since"
	cliFlagTimeout   = checks.CheckKeyTimeout
	cliFlagTTL       = "ttl"
	cliFlagVersion   = commandVersion
)

const (
	cliTextFail = "FAIL"
	cliTextOK   = "OK"
	cliTextWarn = "WARN"
)

const (
	eventsTableTimestampWidth = 19
	eventsTableTargetWidth    = 15
	eventsTableKindWidth      = 8
	eventsTableActionWidth    = 7
	eventsTableMessageWidth   = 60
	eventsTableEllipsisWidth  = 3
	eventsTableEllipsis       = "..."
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
	// FindPID locates running PIDs by program name, used by `daemon reload` to
	// find the daemon when no pidfile is present. Injectable for testing;
	// defaults to a native /proc scan (process.PIDsByComm).
	FindPID func(name string) ([]int, error)
	// pidfileFallbacks lists absolute pidfile locations `daemon reload` searches
	// after the configured runtime dir when resolving the daemon. nil selects
	// the production defaults; tests set it (often empty) to keep pidfile
	// discovery hermetic instead of reading the host's /run/sermo/sermod.pid.
	pidfileFallbacks []string
	// FetchEvents is injectable for `sermoctl events` (listing recent events via
	// the daemon web API). Defaults to fetching over HTTP using the config's web
	// address/port (and password for auth if present).
	FetchEvents func(ctx context.Context, opts options, service string, limit int) ([]event, error)
	// FetchDaemonServiceState returns the daemon-computed service state when
	// sermod is running and the web API is reachable. ok is false when unavailable.
	FetchDaemonServiceState func(ctx context.Context, opts options, service string) (string, bool)
	// FetchDaemonWatchState returns the daemon-computed watch state when sermod is
	// running and the web API is reachable. ok is false when unavailable.
	FetchDaemonWatchState func(ctx context.Context, opts options, watch string) (string, bool)
	// FetchDaemonWatchDetail returns current daemon-published readings for one
	// watch. It is optional so status retains its state-only fallback.
	FetchDaemonWatchDetail func(ctx context.Context, opts options, watch string) (daemonWatchDetail, bool)
	// ProbeDaemonWatch asks the active daemon to run and record one safe manual
	// host-watch sample through the authenticated Web API.
	ProbeDaemonWatch func(ctx context.Context, opts options, watch string) (daemonWatchProbe, error)
	// FetchDaemonApplicationStates returns daemon-computed application states keyed
	// by catalog name. An empty map means the web API was unavailable.
	FetchDaemonApplicationStates func(ctx context.Context, opts options) map[string]string
	// PruneEvents is injectable for `sermoctl events clear` and
	// `sermoctl activity clear`. Defaults to pruning the daemon's persisted event
	// feed over HTTP using the config's web address/port (and password for auth if
	// present).
	PruneEvents func(ctx context.Context, opts options, before time.Time) (int, error)
	// MountController builds the host mount controller for `sermoctl mount|umount`.
	// nil uses the real host commands and /proc readers.
	MountController func(*config.Config) mountctl.Controller
	// BuildNotifiers constructs delivery targets for explicit CLI notifier tests.
	// nil uses the configured notifier settings, including an optional template.
	BuildNotifiers func(*config.Config) (map[string]notify.Notifier, []string)
	// BuildReportNotifiers constructs delivery targets for ad-hoc CLI reports.
	// nil uses the configured notifiers without applying alert templates.
	BuildReportNotifiers func(*config.Config) (map[string]notify.Notifier, []string)
	// InteractiveUser reports the local logged-in user for an interactive
	// terminal session. Nil uses the process stdin and environment.
	InteractiveUser func() (string, bool)
	// NotifyBlockedAction delivers best-effort terminal notices for blocked
	// manual actions that should alert the interactive operator.
	NotifyBlockedAction func(context.Context, operation.Result, string) error
}

type options struct {
	backend    servicemgr.Backend
	json       bool
	quiet      bool
	noCascade  bool // --no-cascade: act on exactly this service, skip also_apply
	force      bool // --force: allow umount -f during `sermoctl umount`
	lazy       bool // --lazy: allow umount -l during `sermoctl umount`
	kill       bool // --kill-blockers: allow policy-gated signalling during `sermoctl umount`
	help       bool
	version    bool // --version / -V
	timeout    time.Duration
	timeoutSet bool
	config     string
	command    string
	args       []string
	// lock command flags
	name        string
	reason      string
	confirm     string
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
	cliApp := App{
		Detector:   servicemgr.NewDetector(),
		NewManager: servicemgr.NewManager,
		LoadConfig: config.Load,
		Env:        os.Getenv,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		Stdin:      os.Stdin,
	}
	cliApp.Operate = cliApp.defaultOperate
	return cliApp.Run(ctx, args)
}

func (a App) withDefaults() App {
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
	if a.FetchDaemonServiceState == nil {
		a.FetchDaemonServiceState = a.fetchDaemonServiceState
	}
	if a.FetchDaemonWatchState == nil {
		a.FetchDaemonWatchState = a.fetchDaemonWatchState
	}
	if a.FetchDaemonWatchDetail == nil {
		a.FetchDaemonWatchDetail = a.fetchDaemonWatchDetail
	}
	if a.ProbeDaemonWatch == nil {
		a.ProbeDaemonWatch = a.probeDaemonWatch
	}
	if a.FetchDaemonApplicationStates == nil {
		a.FetchDaemonApplicationStates = a.fetchDaemonApplicationStates
	}
	if a.PruneEvents == nil {
		a.PruneEvents = a.pruneDaemonEvents
	}
	if a.Runner == nil {
		a.Runner = execx.CommandRunner{}
	}
	if a.BuildNotifiers == nil {
		a.BuildNotifiers = buildConfiguredNotifiers
	}
	if a.BuildReportNotifiers == nil {
		a.BuildReportNotifiers = buildReportNotifiers
	}
	return a
}

// Run executes the CLI.
func (a App) Run(ctx context.Context, args []string) int {
	return a.withDefaults().run(ctx, args)
}

func (a App) run(ctx context.Context, args []string) int {
	opts, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(a.Stderr, "usage error: %v\n", err)
		writeUsage(a.Stderr)
		return exitUsage
	}
	// `--version`/`-V` is parsed as a global flag (so it is never mistaken for the
	// *value* of another flag, e.g. `lock svc --reason -V`); the `version`
	// subcommand is handled in the command switch below.
	if opts.version {
		fmt.Fprintln(a.Stdout, buildinfo.String())
		return exitSuccess
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
		envBackend, err := servicemgr.ParseBackend(a.Env(config.EnvBackendOverride))
		if err != nil {
			fmt.Fprintf(a.Stderr, "usage error: %s: %v\n", config.EnvBackendOverride, err)
			return exitUsage
		}
		opts.backend = envBackend
	}
	if opts.command != commandUmount && (opts.force || opts.lazy || opts.kill) {
		return a.commandUsageError(opts.command, "--force, --lazy and --kill-blockers are only supported by umount")
	}

	switch opts.command {
	case commandHelp:
		return runHelp(a, opts)
	case commandVersion:
		fmt.Fprintln(a.Stdout, buildinfo.String())
		return exitSuccess
	case commandBackend:
		return a.runBackend(ctx, opts)
	case commandStatus:
		return a.runStatus(ctx, opts)
	case commandIsActive:
		return a.runIsActive(ctx, opts)
	case commandStart, commandStop, commandRestart, commandResume:
		return a.runAction(ctx, opts, opts.command)
	case commandMount:
		return a.runMount(ctx, opts)
	case commandUmount:
		return a.runUmount(ctx, opts)
	case commandConfig:
		return a.runConfig(opts)
	case commandLocks:
		return a.runLocks(opts)
	case commandProcesses:
		return a.runProcesses(ctx, opts)
	case commandPreflight:
		return a.runPreflight(ctx, opts)
	case commandDaemon:
		return a.runDaemon(ctx, opts)
	case commandNotifier:
		return a.runNotifier(ctx, opts)
	case commandWatch:
		return a.runWatch(ctx, opts)
	case commandEvents:
		return a.runEvents(ctx, opts)
	case commandActivity:
		return a.runActivity(ctx, opts)
	case commandApps:
		return a.runApps(ctx, opts)
	case commandLibs:
		return a.runLibs(ctx, opts)
	case commandPatterns:
		return a.runPatterns(opts)
	case commandServices:
		return a.runServices(ctx, opts)
	case commandState:
		return a.runState(ctx, opts)
	case commandLock:
		return a.runLock(ctx, opts)
	case commandUnmonitor:
		return a.runMonitor(ctx, opts, true)
	case commandMonitor:
		return a.runMonitor(ctx, opts, false)
	case commandPanic:
		return a.runPanic(ctx, opts)
	case commandSLA:
		return a.runSLA(ctx, opts)
	case commandReload:
		if opts.service() == "" {
			return a.commandUsageError(commandReload, "reload requires a service name; use `sermoctl daemon reload` to reload sermod config")
		}
		return a.runAction(ctx, opts, commandReload)
	case commandWizard:
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
		return a.commandUsageError(opts.command, opts.command+" takes no arguments")
	}
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

	detection, err := a.Detector.Detect(ctx, opts.backend)
	if err != nil {
		if opts.json {
			writeJSON(a.Stdout, map[string]string{cliJSONKeyError: err.Error()})
		} else {
			fmt.Fprintf(a.Stderr, "backend detection failed: %v\n", err)
		}
		return exitRuntimeError
	}

	if opts.json {
		writeJSON(a.Stdout, map[string]string{cliJSONKeyBackend: string(detection.Backend)})
		return exitSuccess
	}

	fmt.Fprintln(a.Stdout, detection.Backend)
	return exitSuccess
}

func (a App) runStatus(ctx context.Context, opts options) int {
	if opts.service() == "" {
		return a.commandUsageError(commandStatus, "status requires a service name")
	}
	if len(opts.args) > 1 {
		return a.commandUsageError(commandStatus, "status takes exactly one service name")
	}

	status, code := a.serviceStatus(ctx, opts)
	if code != exitSuccess {
		return code
	}

	mon := a.serviceMonitorState(ctx, opts)
	displayState := a.serviceDisplayState(ctx, opts, status, mon)
	if opts.json {
		writeJSON(a.Stdout, statusToJSON(status, mon, displayState))
		return exitSuccess
	}

	fmt.Fprintf(a.Stdout, "%s state=%s backend=%s service=%s%s\n",
		status.Service, displayState, status.Backend, status.Unit, formatStateMetadata(mon))
	return exitSuccess
}

// serviceDisplayState returns the operator-facing state for status output.
// When sermod is up it prefers the daemon's settled view (including starting);
// otherwise it derives state from the local backend query only.
func (a App) serviceDisplayState(ctx context.Context, opts options, status servicemgr.ServiceStatus, mon monitorView) string {
	if a.FetchDaemonServiceState != nil {
		service := opts.service()
		if service == "" {
			service = status.Service
		}
		if st, ok := a.FetchDaemonServiceState(ctx, opts, service); ok && st != "" {
			return st
		}
	}
	return app.ServiceState(mon.Enabled, mon.Monitored(), string(status.Status), "", true, false)
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
func (a App) serviceMonitorState(ctx context.Context, opts options) monitorView {
	view := monitorView{Enabled: true}
	globalPath := opts.globalPath()
	cfg, err := a.LoadConfig(globalPath)
	if err != nil {
		return view
	}
	service := opts.service()
	if canonical, ok := cfg.CanonicalServiceName(service); ok {
		service = canonical
		view.Configured = true
		if resolved, errs := cfg.Resolve(service); len(errs) == 0 {
			if cfgval.Disabled(resolved.Tree) {
				view.Enabled = false
				view.Paused = true
			}
			if mode, _ := resolved.Tree[config.EntryKeyMonitor].(string); mode == config.MonitorDisabled {
				view.Paused = true
			}
		}
	}
	store, err := state.OpenContext(ctx, filepath.Join(cfg.Global.StateDir(), state.Filename))
	if err != nil {
		return view
	}
	defer store.Close()
	rec, found, err := store.MonitorState(service) //nolint:contextcheck // ctx bound via OpenContext above
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
		return a.commandUsageError(commandIsActive, "is-active requires a service name")
	}
	if len(opts.args) > 1 {
		return a.commandUsageError(commandIsActive, "is-active takes exactly one service name")
	}

	status, code := a.serviceStatus(ctx, opts)
	if code != exitSuccess {
		return code
	}

	switch {
	case opts.json:
		mon := a.serviceMonitorState(ctx, opts)
		writeJSON(a.Stdout, statusToJSON(status, mon, a.serviceDisplayState(ctx, opts, status, mon)))
	case !opts.quiet:
		fmt.Fprintln(a.Stdout, status.Status)
	}

	if status.Status == servicemgr.StatusActive {
		return exitSuccess
	}
	return exitNotActive
}

// runAction performs a start/stop/restart/reload/resume through the safe operation engine
// : the resolved service is run under the internal operation lock,
// active named runtime locks, required preflight, guards, residual-process
// handling and postflight. Manual sermoctl actions are not rate limited, but are
// fully guarded.
func (a App) runAction(ctx context.Context, opts options, action string) int {
	if opts.service() == "" {
		return a.commandUsageError(action, action+" requires a service name")
	}
	if len(opts.args) > 1 {
		return a.commandUsageError(action, action+" takes exactly one service name")
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
	if action == actionReload {
		if code := a.requireReloadSupported(ctx, opts, resolved, service); code != exitSuccess {
			return code
		}
	}

	actionStore := a.openManualActionStore(ctx, cfg, action)
	if actionStore != nil {
		defer func() { _ = actionStore.Close() }()
	}
	result, err := a.operateWithCascade(ctx, opts, cfg, resolved, service, action, actionStore)
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
		a.printOperation(result)
	}
	return operationExit(result.Status)
}

func (a App) requireReloadSupported(ctx context.Context, opts options, resolved config.Resolved, service string) int {
	detection, err := a.Detector.Detect(ctx, opts.backend)
	if err != nil {
		return a.fail(opts, fmt.Sprintf("backend detection failed: %v", err))
	}
	manager, err := a.NewManager(detection.Backend)
	if err != nil {
		return a.fail(opts, fmt.Sprintf("service manager unavailable: %v", err))
	}
	resolver := servicemgr.NewUnitResolver()
	resolver.Runner = a.Runner
	resolver.Manager = manager
	supportOpts := opts
	supportOpts.quiet = true
	target, err := a.resolveControlTarget(ctx, supportOpts, service, resolved.Tree, detection.Backend, manager, resolver)
	if err != nil {
		return a.fail(opts, fmt.Sprintf("control target failed: %v", err))
	}
	reloadCtx, cancel := context.WithTimeout(ctx, reloadCapabilityTimeout)
	defer cancel()
	canReload, err := operation.ReloadSupported(reloadCtx, resolved.Tree, target.Manager, target.Unit)
	if err != nil {
		return a.fail(opts, fmt.Sprintf("reload support unavailable: %v", err))
	}
	if !canReload {
		return a.fail(opts, operation.UnsupportedReloadError(target.Unit).Error())
	}
	return exitSuccess
}

// operateWithCascade runs the action on the primary service, and — unless
// --no-cascade — on the services it lists in also_apply, in dependency order
// (start/restart: primary first; stop: additionals first). Targets run through
// their own guarded operation; each target's result is printed. The primary's
// result is returned and drives the exit code.
func (a App) operateWithCascade(ctx context.Context, opts options, cfg *config.Config, resolved config.Resolved, service, action string, actionStore *state.Store) (operation.Result, error) {
	targets := config.CascadeTargets(resolved.Tree)
	// also_apply cascades only start/stop/restart, not reload/resume.
	if opts.noCascade || action == actionReload || action == actionResume || len(targets) == 0 {
		a.beginManualOperationSettling(cfg, actionStore, service, action)
		out, err := a.Operate(ctx, opts, cfg, resolved, service, action)
		activeAfterStart := a.manualActionActiveAfterStart(ctx, opts, cfg, resolved, service, action, out, err)
		a.syncManualActionMonitoring(cfg, actionStore, service, action, out, err, activeAfterStart)
		a.finishManualOperationSettling(cfg, actionStore, service, action, out, err, activeAfterStart)
		return out, err
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
	var cascadeFailed bool
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
		a.beginManualOperationSettling(cfg, actionStore, svc, action)
		out, err := a.Operate(ctx, opts, cfg, res, svc, action)
		activeAfterStart := a.manualActionActiveAfterStart(ctx, opts, cfg, res, svc, action, out, err)
		a.syncManualActionMonitoring(cfg, actionStore, svc, action, out, err, activeAfterStart)
		a.finishManualOperationSettling(cfg, actionStore, svc, action, out, err, activeAfterStart)
		if svc == service {
			primary, primaryErr = out, err
			continue
		}
		// A cascade target counts as failed both when its operation returns an
		// error and when it completes with a failed result; either way the
		// primary must be downgraded so the exit code reflects the failure.
		if err != nil || app.CascadeTargetFailed(out) {
			cascadeFailed = true
		}
		if err != nil {
			fmt.Fprintf(a.Stderr, "cascade %s: %v\n", svc, err)
		} else if !opts.quiet {
			fmt.Fprintf(a.Stdout, "cascade %s: %s %s\n", svc, action, out.Status)
		}
		if err == nil {
			a.notifyInteractiveBlockedAction(ctx, out)
		}
	}
	return app.DowngradePrimaryOnCascadeFailure(primary, cascadeFailed), primaryErr
}

func (a App) openManualActionStore(ctx context.Context, cfg *config.Config, action string) *state.Store {
	if !operationActionUsesState(action) {
		return nil
	}
	store, err := state.OpenContext(ctx, filepath.Join(cfg.Global.StateDir(), state.Filename))
	if err != nil {
		fmt.Fprintf(a.Stderr, "warning: operation state unavailable: %v\n", err)
		return nil
	}
	return store
}

func operationActionUsesState(action string) bool {
	return action == actionStart || action == actionStop || action == actionRestart || action == actionReload || action == actionResume
}

func (a App) beginManualOperationSettling(cfg *config.Config, store *state.Store, service, action string) {
	if store == nil {
		return
	}
	if err := app.BeginOperationSettlingForCLI(store, service, action); err != nil {
		msg := err.Error()
		fmt.Fprintf(a.Stderr, cliWarningFormat, msg)
		a.recordAccess(cfg, action+"-settling", service, accessStatusError, msg)
	}
}

func (a App) finishManualOperationSettling(cfg *config.Config, store *state.Store, service, action string, result operation.Result, opErr error, activeAfterStart bool) {
	if store == nil {
		return
	}
	if err := app.FinishOperationSettlingForCLIWithActive(store, service, action, result, opErr, activeAfterStart); err != nil {
		msg := err.Error()
		fmt.Fprintf(a.Stderr, cliWarningFormat, msg)
		a.recordAccess(cfg, action+"-settling", service, accessStatusError, msg)
	}
}

func (a App) syncManualActionMonitoring(cfg *config.Config, store *state.Store, service, action string, result operation.Result, opErr error, activeAfterStart bool) {
	if store == nil || opErr != nil {
		return
	}
	change, err := app.SyncManualActionMonitoringWithActive(store, service, action, result, state.SourceCLIManualStop, state.SourceCLI, activeAfterStart)
	if err != nil {
		msg := err.Error()
		fmt.Fprintf(a.Stderr, cliWarningFormat, msg)
		a.recordAccess(cfg, action+"-monitor", service, accessStatusError, msg)
		return
	}
	if change.Changed {
		a.recordAccess(cfg, change.Action, service, accessStatusOK, change.Message)
	}
}

func (a App) manualActionActiveAfterStart(ctx context.Context, opts options, _ *config.Config, resolved config.Resolved, service, action string, result operation.Result, opErr error) bool {
	if opErr != nil || result.Status != operation.ResultPostflightFailed || !cliManualStartLikeAction(action) {
		return false
	}
	if a.Detector == nil || a.NewManager == nil {
		return false
	}
	detection, err := a.Detector.Detect(ctx, opts.backend)
	if err != nil {
		return false
	}
	manager, err := a.NewManager(detection.Backend)
	if err != nil {
		return false
	}
	resolver := servicemgr.NewUnitResolver()
	resolver.Manager = manager
	target, err := a.resolveControlTarget(ctx, options{quiet: true, backend: opts.backend}, service, resolved.Tree, detection.Backend, manager, resolver)
	if err != nil {
		return false
	}
	st, err := target.Manager.Status(ctx, target.Unit)
	return err == nil && st.Status == servicemgr.StatusActive
}

func cliManualStartLikeAction(action string) bool {
	return action == actionStart || action == actionRestart || action == actionResume
}

// defaultOperate wires the real operation engine from a resolved service and
// runs the requested action.
func (a App) defaultOperate(ctx context.Context, opts options, cfg *config.Config, resolved config.Resolved, service, action string) (operation.Result, error) {
	detection, err := a.Detector.Detect(ctx, opts.backend)
	if err != nil {
		return operation.Result{}, fmt.Errorf("backend detection failed: %w", err)
	}
	manager, err := a.NewManager(detection.Backend)
	if err != nil {
		return operation.Result{}, fmt.Errorf("service manager unavailable: %w", err)
	}

	resolver := servicemgr.NewUnitResolver()
	resolver.Manager = manager
	target, err := a.resolveControlTarget(ctx, opts, service, resolved.Tree, detection.Backend, manager, resolver)
	if err != nil {
		return operation.Result{}, err
	}

	runtime := cfg.Global.RuntimeDir()
	locker := locks.NewOperationLocker(locks.RuntimeOpsDir(runtime))
	locker.OnReclaim = func(service, reason string) {
		fmt.Fprintf(a.Stderr, "reclaimed stale operation lock for %s (%s)\n", service, reason)
	}
	discoverer := process.NewDiscovererWithUserLookup(app.EngineUserLookup(cfg, a.Runner))
	if backendPIDs := backendPIDsForTarget(ctx, target, a.Runner); backendPIDs != nil {
		discoverer.BackendPIDs = backendPIDs
	}
	collector := metrics.New(metrics.OSReader{})
	selectors, _ := process.ParseSelectors(resolved.Tree)
	metricSample := app.MetricSampleForOperation(service, resolved.Tree, collector, discoverer, selectors)
	libBaseline := map[string]string{}
	engine := operation.New(operation.Config{
		Service:          service,
		Unit:             target.Unit,
		Backend:          string(target.Backend),
		Tree:             resolved.Tree,
		Manager:          target.Manager,
		Locker:           &locker,
		Scanner:          locks.NewScanner(locks.RuntimeLocksDir(runtime)),
		Discoverer:       discoverer,
		ResolveUser:      discoverer.ResolveUser,
		CheckDeps:        checks.Deps{DefaultTimeout: engineDefaultTimeout(cfg), Runner: a.Runner, Processes: discoverer.ObserveState, ProcessesAny: discoverer.ObserveAnyState, ProcessCount: discoverer.CountMatching},
		MetricSample:     metricSample,
		Changed:          app.ArtifactChangedFunc(libBaseline),
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
		fmt.Fprintf(a.Stdout, "  residual pid=%d %s=%s\n", p.PID, key, value)
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

// runConfig dispatches the `config` subcommands.
func (a App) runConfig(opts options) int {
	if len(opts.args) == 0 {
		return a.commandUsageError(commandConfig, "config requires a subcommand (validate)")
	}

	sub := opts.args[0]
	rest := opts.args[1:]
	globalPath := opts.globalPath()

	switch sub {
	case commandValidate:
		return a.runConfigValidate(globalPath, rest, opts)
	default:
		return a.commandUsageError(commandConfig, fmt.Sprintf("unknown config subcommand %q", sub))
	}
}

func (a App) runConfigValidate(globalPath string, rest []string, opts options) int {
	if len(rest) > 0 {
		return a.commandUsageError(commandConfig, "config validate takes no service name; it validates the whole Sermo configuration")
	}

	cfg, err := a.LoadConfig(globalPath)
	if err != nil {
		return a.fail(opts, fmt.Sprintf("load config failed: %v", err))
	}

	issues := config.Validate(cfg)

	if len(issues) == 0 {
		switch {
		case opts.json:
			writeJSON(a.Stdout, map[string]any{cliJSONKeyValid: true})
		case !opts.quiet:
			fmt.Fprintln(a.Stdout, cliTextOK)
		}
		return exitSuccess
	}

	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyValid: false, cliJSONKeyErrors: issuesJSON(issues)})
	} else {
		a.printIssues(opts, issues)
	}
	return exitConfigInvalid
}

// printIssues writes validation findings in the section-30 ERROR format.
func (a App) printIssues(opts options, issues []config.Issue) {
	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyValid: false, cliJSONKeyErrors: issuesJSON(issues)})
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
		out = append(out, map[string]string{cliJSONKeyScope: is.Scope, cliJSONKeyError: is.Msg})
	}
	return out
}

// runPreflight resolves a service, builds its preflight checks and runs them
// under engine.default_timeout. A required check failure exits 1.
func (a App) runPreflight(ctx context.Context, opts options) int {
	if opts.service() == "" {
		return a.commandUsageError(commandPreflight, "preflight requires a service name")
	}
	if len(opts.args) > 1 {
		return a.commandUsageError(commandPreflight, "preflight takes exactly one service name")
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

	resolved, code := a.resolveService(opts, cfg, service)
	if code != exitSuccess {
		return code
	}

	section, _ := resolved.Tree[config.SectionPreflight].(map[string]any)
	discoverer := process.NewDiscovererWithUserLookup(app.EngineUserLookup(cfg, a.Runner))
	deps := checks.Deps{
		Service:        service,
		DefaultTimeout: engineDefaultTimeout(cfg),
		Status:         a.statusFunc(opts, resolved.Tree, config.ServiceUnit(resolved.Tree, service)),
		Processes:      discoverer.ObserveState,
		ProcessCount:   discoverer.CountMatching,
	}
	built, buildWarnings := checks.BuildWithWarnings(section, deps)
	warnings := checks.BuildWarningStrings(buildWarnings)
	for _, w := range warnings {
		fmt.Fprintf(a.Stderr, cliWarningFormat, w)
	}

	ctx, cancel := context.WithTimeout(ctx, app.PreflightDeadline(deps.DefaultTimeout))
	defer cancel()
	results := checks.BuildWarningResults(buildWarnings)
	results = append(results, checks.Run(ctx, built, 0)...)
	outcome := checks.Evaluate(results)

	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyService: service, cliJSONKeyOK: outcome.OK, cliJSONKeyChecks: results})
	} else {
		a.printPreflight(service, outcome)
	}

	if outcome.OK {
		return exitSuccess
	}
	return exitNotActive
}

func (a App) printPreflight(service string, outcome checks.Outcome) {
	overall := cliTextOK
	if !outcome.OK {
		overall = cliTextFail
	}
	if len(outcome.Results) == 0 {
		fmt.Fprintf(a.Stdout, "preflight %s: %s (no checks)\n", service, overall)
		return
	}
	fmt.Fprintf(a.Stdout, "preflight %s: %s\n", service, overall)
	for _, r := range outcome.Results {
		tag := cliTextOK
		if !r.OK {
			tag = cliTextFail
			if r.Optional {
				tag = cliTextWarn
			}
		}
		fmt.Fprintf(a.Stdout, "  %-4s %s: %s\n", tag, r.Check, r.Message)
	}
}

// statusFunc builds a lazy backend status query for `service` checks; it only
// detects the backend and resolves the unit (service candidates) when a service
// check actually runs.
func (a App) statusFunc(opts options, tree map[string]any, base string) func(context.Context) (servicemgr.Status, error) {
	return func(ctx context.Context) (servicemgr.Status, error) {
		detection, err := a.Detector.Detect(ctx, opts.backend)
		if err != nil {
			return "", fmt.Errorf("detect service backend: %w", err)
		}
		manager, err := a.NewManager(detection.Backend)
		if err != nil {
			return "", err
		}
		resolver := servicemgr.NewUnitResolver()
		resolver.Manager = manager
		target, err := a.resolveControlTarget(ctx, opts, base, tree, detection.Backend, manager, resolver)
		if err != nil {
			return "", err
		}
		status, err := target.Manager.Status(ctx, target.Unit)
		if err != nil {
			return "", fmt.Errorf("status %s: %w", target.Unit, err)
		}
		return status.Status, nil
	}
}

func engineDefaultTimeout(cfg *config.Config) time.Duration {
	return app.EngineDuration(cfg, config.EngineKeyDefaultTimeout, app.DefaultEngineCheckTimeout)
}

// runLocks reports the named runtime locks for a service (active, expired and
// stale), reading the runtime root from the loaded config.
func (a App) runLocks(opts options) int {
	if opts.service() == "" {
		return a.commandUsageError(commandLocks, "locks requires a service name")
	}
	if len(opts.args) > 1 {
		return a.commandUsageError(commandLocks, "locks takes exactly one service name")
	}

	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	service := canonicalServiceIfKnown(cfg, opts.service())

	dir := locks.RuntimeLocksDir(cfg.Global.RuntimeDir())
	report, err := locks.NewScanner(dir).Scan(service)
	if err != nil {
		return a.fail(opts, fmt.Sprintf("scan locks failed: %v", err))
	}

	for _, w := range report.Warnings {
		fmt.Fprintf(a.Stderr, cliWarningFormat, w)
	}

	if opts.json {
		writeJSON(a.Stdout, map[string]any{
			cliJSONKeyService: report.Service,
			cliJSONKeyLocks:   report.Locks,
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
// , reading the service's `processes` selectors from resolved config.
func (a App) runProcesses(ctx context.Context, opts options) int {
	if opts.service() == "" {
		return a.commandUsageError(commandProcesses, "processes requires a service name")
	}
	if len(opts.args) > 1 {
		return a.commandUsageError(commandProcesses, "processes takes exactly one service name")
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

	resolved, code := a.resolveService(opts, cfg, service)
	if code != exitSuccess {
		return code
	}

	selectors, warnings := process.ParseSelectors(resolved.Tree)
	procs, discWarnings := a.discoverProcesses(ctx, opts, cfg, resolved, service, selectors)
	warnings = append(warnings, discWarnings...)

	for _, w := range warnings {
		fmt.Fprintf(a.Stderr, cliWarningFormat, w)
	}

	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyService: service, cliJSONKeyProcesses: procs})
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
	target, err := a.resolveControlTarget(ctx, opts, service, resolved.Tree, detection.Backend, manager, servicemgr.UnitResolver{Runner: a.Runner, Manager: manager})
	if err != nil {
		return discoverer.Discover(selectors)
	}
	if backendPIDs := backendPIDsForTarget(ctx, target, a.Runner); backendPIDs != nil {
		discoverer.BackendPIDs = backendPIDs
	}
	return discoverer.Discover(selectors)
}

func backendPIDsForTarget(ctx context.Context, target control.Target, runner execx.Runner) func() []int {
	if target.BackendPIDs != nil {
		return target.BackendPIDs
	}
	switch target.Backend {
	case servicemgr.BackendSystemd, servicemgr.BackendOpenRC:
		return servicemgr.BackendPIDsFuncWithRunner(ctx, target.Backend, target.Unit, runner, nil)
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
		return process.SelectorKeyExe, p.Exe
	}
	if cmd := strings.TrimSpace(strings.Join(p.Cmdline, " ")); cmd != "" {
		return process.SelectorKeyCmd, strconv.Quote(cmd)
	}
	if p.Exe != "" {
		return process.SelectorKeyExe, p.Exe
	}
	return process.SelectorKeyExe, cliDisplayUnknown
}

func orUnknown(s string) string {
	if s == "" {
		return cliDisplayUnknown
	}
	return s
}

// serviceStatus resolves the backend, builds a manager and queries the service.
// On any failure it reports the error and returns a non-success exit code.
func (a App) serviceStatus(ctx context.Context, opts options) (servicemgr.ServiceStatus, int) {
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

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

	service := opts.service()
	target := control.Target{Unit: service, Backend: detection.Backend, Manager: manager}
	if cfg, err := a.LoadConfig(opts.globalPath()); err == nil {
		if canonical, ok := cfg.CanonicalServiceName(service); ok {
			service = canonical
			resolved, errs := cfg.Resolve(service)
			if len(errs) > 0 {
				a.reportError(opts, fmt.Sprintf("config resolve failed: %v", errs[0]))
				return servicemgr.ServiceStatus{}, exitRuntimeError
			}
			resolver := servicemgr.NewUnitResolver()
			resolver.Runner = a.Runner
			resolver.Manager = manager
			target, err = a.resolveControlTarget(ctx, opts, service, resolved.Tree, detection.Backend, manager, resolver)
			if err != nil {
				a.reportError(opts, fmt.Sprintf("control target failed: %v", err))
				return servicemgr.ServiceStatus{}, exitRuntimeError
			}
		} else if len(cfg.Services) > 0 {
			a.reportError(opts, fmt.Sprintf(cliUnknownServiceFormat, service))
			return servicemgr.ServiceStatus{}, exitRuntimeError
		}
	}

	status, err := target.Manager.Status(ctx, target.Unit)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("status query failed: %v", err))
		return servicemgr.ServiceStatus{}, exitRuntimeError
	}
	return status, exitSuccess
}

func (a App) resolveControlTarget(ctx context.Context, opts options, service string, tree map[string]any, backend servicemgr.Backend, manager servicemgr.Manager, resolver servicemgr.UnitResolver) (control.Target, error) {
	target, warning := control.ResolveWithFallback(ctx, service, tree, backend, manager, resolver)
	if warning == "" {
		return target, nil
	}
	if target.Unit == "" {
		return control.Target{}, errors.New(warning)
	}
	if !opts.quiet {
		fmt.Fprintf(a.Stderr, "warning: service %s: %s\n", service, warning)
	}
	return target, nil
}

func (a App) reportError(opts options, msg string) {
	if opts.json {
		writeJSON(a.Stdout, map[string]string{cliJSONKeyError: msg})
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
	case commandStart, commandStop, commandRestart, commandReload, commandResume, commandMount, commandUmount, commandState:
		return app.DefaultEngineOperationTimeout
	case commandServices:
		return defaultListCommandTimeout
	default:
		return defaultProbeCommandTimeout
	}
}

func statusToJSON(status servicemgr.ServiceStatus, mon monitorView, displayState string) statusJSON {
	out := statusJSON{
		Service: status.Service,
		State:   displayState,
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
// - `sermoctl events clear [--before TIME]` clears all events or events before a given time.
func (a App) runEvents(ctx context.Context, opts options) int {
	args := opts.args
	if len(args) > 0 && args[0] == commandArgClear {
		if len(args) > 1 {
			return a.commandUsageError(commandEvents, "events clear accepts only optional --before TIME")
		}
		return a.runEventsClear(ctx, opts, commandEvents)
	}
	if len(args) > 1 {
		return a.commandUsageError(commandEvents, "events accepts at most one service name")
	}

	service, limit := a.eventListTarget(opts)
	evs, err := a.FetchEvents(ctx, opts, service, limit)
	if err != nil {
		return a.fail(opts, err.Error())
	}
	a.writeEvents(opts, service, evs)
	return exitSuccess
}

// eventListTarget returns the service filter and limit for `sermoctl events`.
// Config loading is best effort so the daemon can still serve events when the
// local configuration is unavailable.
func (a App) eventListTarget(opts options) (string, int) {
	limit := defaultEventsListLimit
	if opts.eventLimit > 0 {
		limit = opts.eventLimit
	}
	if len(opts.args) == 0 {
		return "", limit
	}

	service := opts.args[0]
	if a.LoadConfig == nil {
		return service, limit
	}
	if cfg, err := a.LoadConfig(opts.globalPath()); err == nil {
		service = canonicalServiceIfKnown(cfg, service)
	}
	return service, limit
}

func (a App) writeEvents(opts options, service string, evs []event) {
	if opts.json {
		writeJSON(a.Stdout, evs)
		return
	}

	if len(evs) == 0 {
		if service != "" {
			fmt.Fprintf(a.Stdout, "no recent events for %s\n", service)
		} else {
			fmt.Fprintln(a.Stdout, "no recent events")
		}
		return
	}
	a.writeEventsTable(evs)
}

func (a App) writeEventsTable(evs []event) {
	tw := tabwriter.NewWriter(a.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tTARGET\tKIND\tACTION\tMESSAGE")
	for _, e := range evs {
		timestamp, target, kind, action, message := eventTableFields(e)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", timestamp, target, kind, action, message)
	}
	_ = tw.Flush()
}

func eventTableFields(e event) (string, string, string, string, string) {
	timestamp := e.Time
	if len(timestamp) >= eventsTableTimestampWidth {
		timestamp = timestamp[:eventsTableTimestampWidth]
	}

	target := e.Service
	if target == "" {
		target = e.Watch
	}
	if target == "" {
		target = "-"
	}
	target = eventTableValue(target, eventsTableTargetWidth)

	kind := eventTableValue(e.Kind, eventsTableKindWidth)
	action := e.Action
	if action == "" {
		action = e.Status
	}
	action = eventTableValue(action, eventsTableActionWidth)
	return timestamp, target, kind, action, eventTableMessage(e.Message)
}

func eventTableValue(value string, width int) string {
	if len(value) > width {
		return value[:width]
	}
	return value
}

func eventTableMessage(message string) string {
	// The message column is capped for terminal readability; tabwriter sizes
	// the rest to content.
	if len(message) > eventsTableMessageWidth {
		return message[:eventsTableMessageWidth-eventsTableEllipsisWidth] + eventsTableEllipsis
	}
	return message
}

// runActivity dispatches activity subcommands. Activity is the dashboard's
// recent-events view, so clearing it uses the same daemon event-prune path.
func (a App) runActivity(ctx context.Context, opts options) int {
	if len(opts.args) > 0 && opts.args[0] == commandArgClear {
		if len(opts.args) > 1 {
			return a.commandUsageError(commandActivity, "activity clear accepts only optional --before TIME")
		}
		return a.runEventsClear(ctx, opts, "activity entries")
	}
	return a.commandUsageError(commandActivity, "activity supports only: clear [--before TIME]")
}

func (a App) runEventsClear(ctx context.Context, opts options, noun string) int {
	cfg, code := a.loadConfig(opts)
	if code != exitSuccess {
		return code
	}
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
		a.recordAccess(cfg, accessCommandEventsClear, "", accessStatusError, err.Error())
		return a.fail(opts, err.Error())
	}
	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyPruned: n})
	} else if before.IsZero() {
		fmt.Fprintf(a.Stdout, "cleared %d %s\n", n, noun)
	} else {
		fmt.Fprintf(a.Stdout, "cleared %d %s before %s\n", n, noun, before.Format(time.RFC3339))
	}
	a.recordAccess(cfg, accessCommandEventsClear, "", accessStatusOK, fmt.Sprintf("pruned %d %s", n, noun))
	return exitSuccess
}

func parseBefore(value string, now func() time.Time) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	at := now()
	if d, err := time.ParseDuration(value); err == nil {
		if d <= 0 {
			return time.Time{}, errors.New("invalid --before: duration must be positive")
		}
		return at.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		if t.After(at) {
			return time.Time{}, errors.New("invalid --before: timestamp must not be in the future")
		}
		return t, nil
	}
	return time.Time{}, errors.New("invalid --before: use a non-future RFC3339 timestamp (e.g. 2026-06-13T12:00:00Z) or positive duration (e.g. 1h, 30m)")
}

// pruneDaemonEvents performs the HTTP call to the running sermod's web API
// to prune its event log. It reads the web: address/port and any
// admin password from the shared config so local sermoctl can authenticate
// the same way the operator would via the UI.
func (a App) pruneDaemonEvents(ctx context.Context, opts options, before time.Time) (int, error) {
	cfg, code := a.loadConfig(opts)
	if code != exitSuccess || cfg == nil {
		return 0, errors.New("failed to load config")
	}
	base, err := webAPIBase(cfg)
	if err != nil {
		return 0, err
	}
	u := base + daemonAPIPathEventsClear
	if !before.IsZero() {
		u += "?" + daemonAPIQueryBefore + "=" + before.Format(time.RFC3339)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return 0, fmt.Errorf("build clear events request: %w", err)
	}
	req.Header.Set(daemonWebCSRFHeader, daemonWebCSRFValue)

	// If the config declares an admin password, send Basic auth (any user + pw).
	applyDaemonWebAuth(req, cfg)

	client := &http.Client{Timeout: daemonWebClientTimeout}
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
		return nil, errors.New("failed to load config")
	}
	base, err := webAPIBase(cfg)
	if err != nil {
		return nil, err
	}

	var u string
	if service != "" {
		u = fmt.Sprintf("%s%s/%s%s?%s=%d", base, daemonAPIPathServices, service, daemonAPIPathServiceEvents, daemonAPIQueryLimit, limit)
	} else {
		u = fmt.Sprintf("%s%s?%s=%d", base, daemonAPIPathEvents, daemonAPIQueryLimit, limit)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build events request: %w", err)
	}
	// no CSRF needed for GET; add auth if configured
	applyDaemonWebAuth(req, cfg)

	client := &http.Client{Timeout: daemonWebClientTimeout}
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

func applyDaemonWebAuth(req *http.Request, cfg *config.Config) {
	if pw := daemonWebPassword(cfg); pw != "" {
		req.Header.Set(daemonWebHeaderAuthorization, daemonWebBasicAuth(pw))
	}
}

func daemonWebPassword(cfg *config.Config) string {
	if wraw, ok := cfg.Global.Raw[config.SectionWeb].(map[string]any); ok {
		return cfgval.String(wraw[config.WebKeyPassword])
	}
	return ""
}

func daemonWebBasicAuth(password string) string {
	cred := base64.StdEncoding.EncodeToString([]byte(daemonWebAuthUserPrefix + password))
	return daemonWebBasicAuthPrefix + cred
}

// daemonAPIGet performs an authenticated GET against the running sermod web API.
func (a App) daemonAPIGet(ctx context.Context, opts options, path string) ([]byte, int, error) {
	cfg, err := a.LoadConfig(opts.globalPath())
	if err != nil || cfg == nil {
		return nil, 0, err
	}
	base, err := webAPIBase(cfg)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("build daemon API request for %s: %w", path, err)
	}
	applyDaemonWebAuth(req, cfg)
	client := &http.Client{Timeout: daemonWebClientTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("daemon API GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read daemon API response for %s: %w", path, err)
	}
	return body, resp.StatusCode, nil
}

// fetchDaemonServiceState reads GET /api/services/{name} from the running
// sermod web API and returns its computed state field.
func (a App) fetchDaemonServiceState(ctx context.Context, opts options, service string) (string, bool) {
	cfg, err := a.LoadConfig(opts.globalPath())
	if err != nil || cfg == nil {
		return "", false
	}
	name := service
	if canonical, ok := cfg.CanonicalServiceName(service); ok {
		name = canonical
	} else if len(cfg.Services) > 0 {
		return "", false
	}
	body, status, err := a.daemonAPIGet(ctx, opts, daemonAPIPathServices+"/"+url.PathEscape(name))
	if err != nil || status != http.StatusOK {
		return "", false
	}
	var detail struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(body, &detail); err != nil || detail.State == "" {
		return "", false
	}
	return detail.State, true
}

func (a App) fetchDaemonWatchState(ctx context.Context, opts options, watch string) (string, bool) {
	body, status, err := a.daemonAPIGet(ctx, opts, daemonAPIPathWatches)
	if err != nil || status != http.StatusOK {
		return "", false
	}
	var watches []struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(body, &watches); err != nil {
		return "", false
	}
	for _, w := range watches {
		if w.Name == watch && w.State != "" {
			return w.State, true
		}
	}
	return "", false
}

func (a App) fetchDaemonWatchDetail(ctx context.Context, opts options, watch string) (daemonWatchDetail, bool) {
	body, status, err := a.daemonAPIGet(ctx, opts, daemonAPIPathWatches)
	if err != nil || status != http.StatusOK {
		return daemonWatchDetail{}, false
	}
	var watches []daemonWatchDetail
	if err := json.Unmarshal(body, &watches); err != nil {
		return daemonWatchDetail{}, false
	}
	for _, detail := range watches {
		if detail.Name == watch {
			return detail, true
		}
	}
	return daemonWatchDetail{}, false
}

func (a App) fetchDaemonApplicationStates(ctx context.Context, opts options) map[string]string {
	body, status, err := a.daemonAPIGet(ctx, opts, daemonAPIPathApplications)
	if err != nil || status != http.StatusOK {
		return nil
	}
	var apps []struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(body, &apps); err != nil {
		return nil
	}
	out := make(map[string]string, len(apps))
	for _, application := range apps {
		if application.Name != "" && application.State != "" {
			out[application.Name] = application.State
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func webAPIBase(cfg *config.Config) (string, error) {
	wraw, _ := cfg.Global.Raw[config.SectionWeb].(map[string]any)
	if wraw == nil {
		return "", errors.New("web UI is not enabled in config (no web: block or no port); the event API is exposed by the running daemon")
	}
	addr := cfgval.String(wraw[config.WebKeyAddress])
	if addr == "" {
		addr = defaultWebAPIAddress
	}
	p, ok := cfgval.Int(wraw[config.WebKeyPort])
	if !ok || p <= 0 {
		return "", errors.New("web.port is not set in config")
	}
	port := p
	return fmt.Sprintf("%s://%s:%d", daemonWebSchemeHTTP, addr, port), nil
}

// defaultReloadPidfileFallbacks are the absolute pidfiles `daemon reload` checks
// after the configured runtime dir. Keep this list restricted to current
// supported paths; old package locations are intentionally not searched.
func defaultReloadPidfileFallbacks() []string {
	return []string{filepath.Join(config.DefaultRuntime, daemonPIDFilename)}
}

// runReload asks the running sermod to reload its configuration (SIGHUP
// equivalent). It prefers a pidfile written by the daemon under the configured
// runtime dir. If no pidfile is found it falls back to a native /proc scan for
// a running sermod process. This works whether or not the web UI is enabled.
func (a App) runReload(_ context.Context, opts options) int {
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}

	runtimeDir := cfg.Global.RuntimeDir()
	if runtimeDir == "" {
		runtimeDir = config.DefaultRuntime
	}

	fallbacks := a.pidfileFallbacks
	if fallbacks == nil {
		fallbacks = defaultReloadPidfileFallbacks()
	}
	candidates := append([]string{filepath.Join(runtimeDir, daemonPIDFilename)}, fallbacks...)

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
		if pids, err := find(daemonProcessName); err == nil {
			for _, p := range pids {
				if p > 0 {
					pid = p
					break
				}
			}
		}
	}

	if pid <= 0 {
		a.recordAccess(cfg, accessCommandDaemonReload, "", accessStatusError, "could not find running sermod pid")
		return a.fail(opts, "could not find running sermod pid (no pidfile and no running sermod process)")
	}

	// Send SIGHUP. On Linux this is reliable for the daemon's signal handler.
	if err := (process.OSSignaler{}).Signal(pid, syscall.SIGHUP); err != nil {
		a.recordAccess(cfg, accessCommandDaemonReload, "", accessStatusError, err.Error())
		return a.fail(opts, fmt.Sprintf("failed to signal pid %d: %v", pid, err))
	}

	a.recordAccess(cfg, accessCommandDaemonReload, "", accessStatusOK, fmt.Sprintf("pid %d", pid))
	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyOK: true, cliJSONKeyPID: pid})
	} else {
		fmt.Fprintf(a.Stdout, "reload signal (HUP) sent to sermod pid %d\n", pid)
	}
	return exitSuccess
}

func parseArgs(args []string) (options, error) {
	opts := options{backend: ""}
	flagArgs, commandArgs := splitCommandArgs(args)
	opts.commandArgs = append(opts.commandArgs, commandArgs...)

	var backend string
	var notifyValues []string
	fs := pflag.NewFlagSet(cliFlagSetName, pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.SetInterspersed(true)
	fs.BoolVarP(&opts.help, cliFlagHelp, "h", false, "")
	fs.BoolVarP(&opts.version, cliFlagVersion, "V", false, "")
	fs.BoolVar(&opts.json, cliFlagJSON, false, "")
	fs.BoolVarP(&opts.quiet, cliFlagQuiet, "q", false, "")
	fs.BoolVar(&opts.noCascade, cliFlagNoCascade, false, "")
	fs.BoolVar(&opts.force, cliFlagForce, false, "")
	fs.BoolVar(&opts.lazy, cliFlagLazy, false, "")
	fs.BoolVar(&opts.kill, cliFlagKill, false, "")
	fs.BoolVar(&opts.series, cliFlagSeries, false, "")
	fs.BoolVar(&opts.long, cliFlagLong, false, "")
	fs.StringArrayVar(&notifyValues, cliFlagNotify, nil, "")
	fs.DurationVar(&opts.since, cliFlagSince, 0, "")
	fs.StringVar(&opts.before, cliFlagBefore, "", "")
	fs.IntVar(&opts.eventLimit, cliFlagLimit, 0, "")
	fs.StringVar(&backend, cliFlagBackend, "", "")
	fs.DurationVar(&opts.timeout, cliFlagTimeout, 0, "")
	fs.StringVar(&opts.config, cliFlagConfig, "", "")
	fs.StringVar(&opts.name, cliFlagName, "", "")
	fs.StringVar(&opts.reason, cliFlagReason, "", "")
	fs.StringVar(&opts.confirm, cliFlagConfirm, "", "")
	fs.DurationVar(&opts.ttl, cliFlagTTL, 0, "")

	if err := fs.Parse(flagArgs); err != nil {
		return opts, normalizePflagError(err)
	}
	opts.timeoutSet = fs.Changed(cliFlagTimeout)
	// --limit defaults to 0 (unset → runEvents applies its default). An explicit
	// 0 or negative is rejected rather than silently falling back to the default,
	// which the bare `> 0` guard could not distinguish from "unset".
	if fs.Changed(cliFlagLimit) && opts.eventLimit < 1 {
		return opts, errors.New("--limit must be a positive integer")
	}
	if backend != "" {
		parsedBackend, err := servicemgr.ParseBackend(backend)
		if err != nil {
			return opts, fmt.Errorf("parse backend %q: %w", backend, err)
		}
		opts.backend = parsedBackend
	}
	for _, value := range notifyValues {
		opts.notifyNames = append(opts.notifyNames, splitFlagList(value)...)
	}
	rest := fs.Args()
	if len(rest) > 0 {
		opts.command = rest[0]
		opts.args = append(opts.args, rest[1:]...)
	}
	return opts, nil
}

// splitCommandArgs preserves the lock wrapper convention: everything after a
// literal `--` is a command payload, not another sermoctl flag or argument.
func splitCommandArgs(args []string) (flagArgs, commandArgs []string) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

func normalizePflagError(err error) error {
	if msg := err.Error(); strings.HasPrefix(msg, pflagUnknownFlagPrefix) {
		return fmt.Errorf("unknown flag %s", strings.TrimPrefix(msg, pflagUnknownFlagPrefix))
	}
	return err
}

func writeJSON(w io.Writer, value any) {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value) //nolint:errchkjson // best-effort CLI output of internal result structs; a write error to stdout has no recovery
}
