package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/pflag"

	"sermo/internal/app"
	"sermo/internal/assist"
	"sermo/internal/buildinfo"
	"sermo/internal/checks"
	"sermo/internal/cliutil"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/httpx"
	"sermo/internal/mountctl"
	"sermo/internal/notify"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
	"sermo/internal/web"
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
	// actionRepair is manual-only and clears only proven-stale runtime pidfiles
	// before running the normal guarded start path.
	actionRepair = operation.ActionRepair
	// actionReap is not a rule action: a stray process is one Sermo cannot name,
	// so clearing one is always an operator's decision, never a remediation.
	actionReap = process.SectionReap
)

const (
	defaultProbeCommandTimeout = 2 * time.Second
	defaultListCommandTimeout  = 30 * time.Second
	daemonWebClientTimeout     = 10 * time.Second
	defaultEventsListLimit     = 50
	tabwriterPadding           = 2
)

const (
	daemonProcessName            = "sermod"
	daemonWebSchemeHTTP          = checks.URLSchemeHTTP
	daemonWebAuthUserPrefix      = "admin:"
	daemonWebHeaderAuthorization = httpx.HeaderAuthorization
	daemonWebBasicAuthPrefix     = "Basic "
	// daemonWebLocalhostName is the hostname that always resolves to loopback
	// (RFC 6761), so a web.address spelled that way names a local daemon.
	daemonWebLocalhostName = "localhost"
	// beforeFlagLabel names the --before flag in cutoff parse errors.
	beforeFlagLabel         = "--" + web.APIQueryBefore
	cliUnknownServiceFormat = "unknown service %q"
	cliWarningFormat        = "warning: %s\n"
)

const (
	cliFlagSetName   = "sermoctl"
	cliFlagApply     = "apply"
	cliFlagBackend   = commandBackend
	cliFlagBefore    = web.APIQueryBefore
	cliFlagConfig    = commandConfig
	cliFlagConfirm   = "confirm"
	cliFlagCost      = "cost"
	cliFlagForce     = "force"
	cliFlagGenerate  = "generate"
	cliFlagHash      = "hash"
	cliFlagHelp      = commandHelp
	cliFlagJSON      = "json"
	cliFlagKill      = "kill-blockers"
	cliFlagLazy      = "lazy"
	cliFlagLimit     = web.APIQueryLimit
	cliFlagLong      = "long"
	cliFlagName      = config.EntryKeyName
	cliFlagNoCascade = "no-cascade"
	cliFlagNotify    = rules.RuleFieldNotify
	cliFlagQuiet     = "quiet"
	cliFlagReason    = "reason"
	cliFlagSeries    = "series"
	cliFlagSince     = "since"
	cliFlagStdin     = "stdin"
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
	// Wide enough for the longest event kind ("notify-suppressed");
	// "recovered" used to truncate to "recovere" at 8.
	eventsTableKindWidth     = 17
	eventsTableRuleWidth     = 14
	eventsTableActionWidth   = 7
	eventsTableMessageWidth  = 60
	eventsTableEllipsisWidth = 3
	eventsTableEllipsis      = "..."
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
	// buildServiceRuntime is the hermetic test seam for the canonical runtime
	// builder. nil uses app.BuildServiceRuntime in production.
	buildServiceRuntime func(context.Context, app.ServiceRuntimeConfig) app.ServiceRuntime
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
	// FetchDaemonWatchDetail returns the current daemon-published snapshot for
	// one watch. ok is false when sermod or its web API is unavailable.
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
	apply      bool // --apply: signal the authorized strays during `sermoctl reap` (without it, preview only)
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
	// web hash-password flags
	generate bool   // --generate: hash a freshly generated secret and print it once
	stdin    bool   // --stdin: read the password from standard input
	hash     string // --hash: credential format (bcrypt or sha256)
	cost     int    // --cost: bcrypt work factor (0 means the default)
}

// event and eventPage preserve the local CLI names while sharing the daemon's
// JSON model and event-target semantics.
type event = web.Event

type eventPage = web.EventPage

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
	return cliApp.Run(ctx, args)
}

// env reads an environment variable through the injected seam, falling back to
// the real environment for an App built directly (helpers reachable without
// withDefaults, such as the daemon API calls).
func (a App) env(name string) string {
	if a.Env == nil {
		return os.Getenv(name)
	}
	return a.Env(name)
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
	if a.FetchEvents == nil {
		a.FetchEvents = a.fetchEvents
	}
	if a.FetchDaemonServiceState == nil {
		a.FetchDaemonServiceState = a.fetchDaemonServiceState
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
	a.Runner = execx.RunnerOrDefault(a.Runner)
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

type commandHandler func(App, context.Context, options) int

// commandHandlers centralizes command dispatch. Commands with a narrower
// signature adapt here while their implementation stays in its owning module.
var commandHandlers = map[string]commandHandler{
	commandHelp: func(a App, _ context.Context, opts options) int { return runHelp(a, opts) },
	commandVersion: func(a App, _ context.Context, _ options) int {
		fmt.Fprintln(a.Stdout, buildinfo.String())
		return exitSuccess
	},
	commandBackend:   App.runBackend,
	commandStatus:    App.runStatus,
	commandIsActive:  App.runIsActive,
	commandStart:     func(a App, ctx context.Context, opts options) int { return a.runAction(ctx, opts, opts.command) },
	commandStop:      func(a App, ctx context.Context, opts options) int { return a.runAction(ctx, opts, opts.command) },
	commandRestart:   func(a App, ctx context.Context, opts options) int { return a.runAction(ctx, opts, opts.command) },
	commandResume:    func(a App, ctx context.Context, opts options) int { return a.runAction(ctx, opts, opts.command) },
	commandRepair:    func(a App, ctx context.Context, opts options) int { return a.runAction(ctx, opts, opts.command) },
	commandMount:     App.runMount,
	commandUmount:    App.runUmount,
	commandConfig:    func(a App, _ context.Context, opts options) int { return a.runConfig(opts) },
	commandLocks:     func(a App, _ context.Context, opts options) int { return a.runLocks(opts) },
	commandProcesses: App.runProcesses,
	commandReap:      func(a App, ctx context.Context, opts options) int { return a.runAction(ctx, opts, opts.command) },
	commandPreflight: App.runPreflight,
	commandDaemon:    App.runDaemon,
	commandNotifier:  App.runNotifier,
	commandWatch:     App.runWatch,
	commandEvents:    App.runEvents,
	commandActivity:  App.runActivity,
	commandApps:      App.runApps,
	commandLibs:      App.runLibs,
	commandPatterns:  func(a App, _ context.Context, opts options) int { return a.runPatterns(opts) },
	commandServices:  App.runServices,
	commandState:     App.runState,
	commandLock:      App.runLock,
	commandUnmonitor: func(a App, ctx context.Context, opts options) int { return a.runMonitor(ctx, opts, true) },
	commandMonitor:   func(a App, ctx context.Context, opts options) int { return a.runMonitor(ctx, opts, false) },
	commandPanic:     App.runPanic,
	commandSLA:       App.runSLA,
	commandWizard:    App.runWizard,
	commandWeb:       func(a App, _ context.Context, opts options) int { return a.runWeb(opts) },
}

func (a App) run(ctx context.Context, args []string) int {
	opts, code, done := a.prepareOptions(args)
	if done {
		return code
	}
	return a.dispatchCommand(ctx, opts)
}

func (a App) prepareOptions(args []string) (options, int, bool) {
	opts, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(a.Stderr, "usage error: %v\n", err)
		writeUsage(a.Stderr)
		return options{}, exitUsage, true
	}
	// `--version`/`-V` is parsed as a global flag (so it is never mistaken for the
	// *value* of another flag, e.g. `lock svc --reason -V`); the `version`
	// subcommand is handled in the command switch below.
	if opts.version {
		fmt.Fprintln(a.Stdout, buildinfo.String())
		return options{}, exitSuccess, true
	}
	if opts.help {
		if opts.command != "" {
			if !writeCommandUsage(a.Stdout, opts.command) {
				fmt.Fprintf(a.Stderr, "usage error: unknown help topic %q\n", opts.command)
				writeUsage(a.Stderr)
				return options{}, exitUsage, true
			}
		} else {
			writeUsage(a.Stdout)
		}
		return options{}, exitSuccess, true
	}
	if opts.timeout <= 0 {
		opts.timeout = defaultTimeout(opts.command)
	}
	if opts.backend == "" {
		envBackend, err := servicemgr.ParseBackend(a.Env(config.EnvBackendOverride))
		if err != nil {
			fmt.Fprintf(a.Stderr, "usage error: %s: %v\n", config.EnvBackendOverride, err)
			return options{}, exitUsage, true
		}
		opts.backend = envBackend
	}
	if opts.command != commandUmount && (opts.force || opts.lazy || opts.kill) {
		return options{}, a.commandUsageError(opts.command, "--force, --lazy and --kill-blockers are only supported by umount"), true
	}
	// --apply is the only thing that lets a command signal a stray, so it must not
	// be accepted anywhere it would be silently ignored.
	if opts.command != commandReap && opts.apply {
		return options{}, a.commandUsageError(opts.command, "--apply is only supported by "+commandReap), true
	}
	return opts, exitSuccess, false
}

func (a App) dispatchCommand(ctx context.Context, opts options) int {
	if handler, ok := commandHandlers[opts.command]; ok {
		return handler(a, ctx, opts)
	}
	if opts.command == commandReload {
		return a.runServiceReload(ctx, opts)
	}
	if opts.command == "" {
		fmt.Fprintln(a.Stderr, "usage error: missing command")
		writeUsage(a.Stderr)
		return exitUsage
	}
	fmt.Fprintf(a.Stderr, "usage error: unknown command %q\n", opts.command)
	writeUsage(a.Stderr)
	return exitUsage
}

func engineDefaultTimeout(cfg *config.Config) time.Duration {
	return config.EngineDuration(cfg, config.EngineKeyDefaultTimeout, app.DefaultEngineCheckTimeout)
}

// runProcesses discovers and reports the processes belonging to a service
// , reading the service's `processes` selectors from resolved config.
// requireSingleServiceName runs the shared usage guard for commands that take
// exactly one service name: a missing name and extra arguments are usage
// errors. noun names the command in the usage messages.
func (a App) requireSingleServiceName(hasService bool, argCount int, cmd, noun string) int {
	if !hasService {
		return a.commandUsageError(cmd, noun+" requires a service name")
	}
	if argCount > 1 {
		return a.commandUsageError(cmd, noun+" takes exactly one service name")
	}
	return exitSuccess
}

// resolveServiceCommand runs the shared single-service command header: usage
// guards, config load, service canonicalization and resolve. These commands
// name themselves in their usage messages, so cmd is both the command and the
// noun; the multi-word subcommands that need a separate noun (e.g. "lock
// acquire") call requireSingleServiceName directly.
func (a App) resolveServiceCommand(opts options, cmd string) (cfg *config.Config, service string, resolved config.Resolved, code int) {
	if code := a.requireSingleServiceName(opts.service() != "", len(opts.args), cmd, cmd); code != exitSuccess {
		return nil, "", config.Resolved{}, code
	}
	service = opts.service()

	cfg, code = a.loadConfig(opts)
	if cfg == nil {
		return nil, "", config.Resolved{}, code
	}
	service, code = a.canonicalService(opts, cfg, service)
	if code != exitSuccess {
		return nil, "", config.Resolved{}, code
	}
	resolved, code = a.resolveService(opts, cfg, service)
	if code != exitSuccess {
		return nil, "", config.Resolved{}, code
	}
	return cfg, service, resolved, exitSuccess
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

// defaultTimeout returns the per-command outer deadline used when --timeout is
// not given. Backend actions can legitimately take much longer than a probe.
func defaultTimeout(command string) time.Duration {
	switch command {
	case commandStart, commandStop, commandRestart, commandReload, commandResume, commandRepair, commandMount, commandUmount, commandState:
		return app.DefaultEngineOperationTimeout
	case commandStatus, commandIsActive:
		// Config loading and control-target resolution are part of a live service
		// query. On large hosts they can legitimately exceed the short probe CLI
		// budget before the init backend is reached.
		return app.DefaultEngineCheckTimeout
	case commandServices:
		return defaultListCommandTimeout
	default:
		return defaultProbeCommandTimeout
	}
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
	fs.BoolVar(&opts.apply, cliFlagApply, false, "")
	fs.BoolVar(&opts.series, cliFlagSeries, false, "")
	fs.BoolVar(&opts.long, cliFlagLong, false, "")
	fs.StringArrayVar(&notifyValues, cliFlagNotify, nil, "")
	fs.DurationVar(&opts.since, cliFlagSince, 0, "")
	fs.StringVar(&opts.before, cliFlagBefore, "", "")
	fs.IntVar(&opts.eventLimit, cliFlagLimit, 0, "")
	fs.BoolVar(&opts.generate, cliFlagGenerate, false, "")
	fs.BoolVar(&opts.stdin, cliFlagStdin, false, "")
	fs.StringVar(&opts.hash, cliFlagHash, "", "")
	fs.IntVar(&opts.cost, cliFlagCost, 0, "")
	fs.StringVar(&backend, cliFlagBackend, "", "")
	fs.DurationVar(&opts.timeout, cliFlagTimeout, 0, "")
	fs.StringVar(&opts.config, cliFlagConfig, "", "")
	fs.StringVar(&opts.name, cliFlagName, "", "")
	fs.StringVar(&opts.reason, cliFlagReason, "", "")
	fs.StringVar(&opts.confirm, cliFlagConfirm, "", "")
	fs.DurationVar(&opts.ttl, cliFlagTTL, 0, "")

	if err := fs.Parse(flagArgs); err != nil {
		return opts, cliutil.NormalizePflagError(err) //nolint:wrapcheck // the normalized message is printed verbatim as the usage error; wrapping would re-add the pflag prefix noise the helper strips
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

func writeJSON(w io.Writer, value any) {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value) //nolint:errchkjson // best-effort CLI output of internal result structs; a write error to stdout has no recovery
}

// newTabWriter builds the standard column writer used for CLI tables.
func newTabWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, tabwriterPadding, ' ', 0)
}
