// Command sermod is the Sermo monitoring daemon.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	"sermo/internal/app"
	"sermo/internal/buildinfo"
	"sermo/internal/cfgval"
	"sermo/internal/cliutil"
	"sermo/internal/config"
	"sermo/internal/control"
	"sermo/internal/emission"
	"sermo/internal/execx"
	"sermo/internal/logfile"
	"sermo/internal/metrics"
	"sermo/internal/notify"
	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
	"sermo/internal/telegrambot"
	"sermo/internal/web"
	"sermo/internal/webcred"
)

const (
	exitConfigInvalid  = 78
	exitAlreadyRunning = 1
	exitFailure        = 2
	exitUsage          = 64
)

const (
	commandRun     = "run"
	commandVersion = "version"
	flagConfig     = "config"
	flagVerbose    = "verbose"
	flagVersion    = "--version"
	flagVersionAlt = "-V"
	shortVerbose   = "v"
)

const (
	defaultRuntimeDir    = config.DefaultRuntime
	daemonPIDFilename    = config.DaemonPIDFilename
	instanceLockFilename = "sermod.lock"
	daemonEventLogLimit  = 1000
	daemonPIDFileMode    = 0o644
	daemonRuntimeDirMode = 0o700
	// daemonWebTokenFileMode keeps the runtime token owner-only: reading it is
	// equivalent to holding the admin password.
	daemonWebTokenFileMode = 0o600
	// secretGroupOtherMask matches any group or other permission bit, which on a
	// file holding a password is worth a warning.
	secretGroupOtherMask = 0o077
	// tmpFileExt names the staging file of an atomic write, the same spelling
	// mountctl uses for its state files.
	tmpFileExt = ".tmp"
	// shutdownPruneDrainTimeout bounds how long shutdown waits for an in-flight
	// state-maintenance statement; it must stay well under init-system stop
	// timeouts (systemd defaults to 90s) so a long DELETE or consolidation cannot
	// force SIGKILL.
	shutdownPruneDrainTimeout = 5 * time.Second
)

// drainOrTimeout waits for done or the timeout, reporting whether done arrived.
func drainOrTimeout(done <-chan struct{}, timeout time.Duration) bool {
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// goTracked runs fn in a goroutine and reports its completion on the returned
// channel, so shutdown can drain it before the store closes.
func goTracked(fn func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	return done
}

const (
	logFieldAddress               = "address"
	logFieldAffected              = "affected"
	logFieldAuth                  = "auth"
	logFieldBackend               = "backend"
	logFieldConfig                = "config"
	logFieldConfigured            = "configured"
	logFieldEnabledApps           = "enabled_apps"
	logFieldEnabledLibraries      = "enabled_libraries"
	logFieldEnabledServices       = "enabled_services"
	logFieldEnabledServiceWatches = "enabled_service_watches"
	logFieldEnabledWatches        = "enabled_watches"
	logFieldError                 = "error"
	logFieldEUID                  = "euid"
	logFieldKey                   = "key"
	logFieldMessage               = "message"
	logFieldMode                  = "mode"
	logFieldPath                  = "path"
	logFieldPID                   = "pid"
	logFieldProcesses             = "processes"
	logFieldReason                = "reason"
	logFieldRows                  = "rows"
	logFieldScope                 = "scope"
	logFieldServices              = "services"
	logFieldWatches               = "watches"

	logValueAuthEnabled = "enabled"
)

func main() {
	//nolint:forbidigo // main cannot return an exit code; os.Exit here is the only way to propagate it.
	os.Exit(run(os.Args[1:]))
}

func versionRequested(args []string) bool {
	if len(args) > 0 && args[0] == commandVersion {
		return true
	}
	return slices.Contains(args, flagVersion) || slices.Contains(args, flagVersionAlt)
}

func parseRunArgs(args []string) (cliArgs, error) {
	parsed, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "usage error: %v\n", err)
		fmt.Fprintln(os.Stderr, "usage: sermod run [--config /etc/sermo/sermo.yml] [--verbose|-v]")
		fmt.Fprintln(os.Stderr, "       sermod version")
		return cliArgs{}, err
	}
	if parsed.command != commandRun {
		fmt.Fprintf(os.Stderr, "usage error: unknown command %q\n", parsed.command)
		return cliArgs{}, errors.New("unknown command")
	}
	return parsed, nil
}

// loadDaemonConfig returns a non-nil config with exit code 0, or nil with a
// non-zero code; callers gate on the nil config so the pairing stays checkable.
func loadDaemonConfig(logger *slog.Logger, globalPath string) (*config.Config, int) {
	cfg, err := config.Load(globalPath)
	if err != nil {
		logger.Error("load config", logFieldError, err)
		return nil, exitFailure
	}
	if issues := config.Validate(cfg); len(issues) > 0 {
		for _, issue := range issues {
			logger.Error("config invalid", logFieldScope, issue.Scope, logFieldMessage, issue.Msg)
		}
		return nil, exitConfigInvalid
	}
	return cfg, 0
}

//nolint:gocognit,gocyclo,maintidx // Daemon startup is intentionally ordered: locks, persistence, workers and shutdown must remain visible in one flow.
func run(args []string) int {
	if versionRequested(args) {
		fmt.Fprintln(os.Stdout, buildinfo.String())
		return 0
	}
	parsed, err := parseRunArgs(args)
	if err != nil {
		return exitUsage
	}
	globalPath := parsed.globalPath

	level := slog.LevelInfo
	if parsed.verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if parsed.verbose {
		logger.Debug("verbose logging enabled", logFieldConfig, globalPath)
	}

	// Sermo is designed to run as root: it inspects and signals processes owned by
	// other users, controls the service manager, opens raw ICMP sockets and reads
	// privileged /proc entries. It still starts unprivileged, but those features
	// degrade — so warn loudly rather than fail silently.
	if os.Geteuid() != 0 {
		logger.Warn("sermod is not running as root; features that need privileges will be unavailable",
			logFieldEUID, os.Geteuid(),
			logFieldAffected, "service control, signalling other users' processes, icmp checks, per-process IO, cross-user /proc inspection")
	}

	cfg, exitCode := loadDaemonConfig(logger, globalPath)
	if cfg == nil {
		return exitCode
	}
	logger.Debug("config loaded", logFieldPath, globalPath, logFieldServices, len(cfg.Services))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	detection, exitCode := detectServiceManager(ctx, cfg, logger)
	if exitCode != 0 {
		return exitCode
	}
	logger.Debug("service backend detected", logFieldBackend, detection.Backend)
	manager, err := servicemgr.NewManager(detection.Backend)
	if err != nil {
		logger.Error("service manager", logFieldError, err)
		return exitFailure
	}

	rt, instanceLock, exitCode := acquireDaemonRuntimeLock(cfg, logger)
	if exitCode != 0 {
		return exitCode
	}
	defer func() { _ = instanceLock.Close() }()

	store, exitCode := openDaemonStore(cfg, logger)
	if exitCode != 0 {
		return exitCode
	}
	defer func() { _ = store.Close() }()

	notifiers, notifyWarnings := notify.Build(cfg.Notifiers(), notify.WithTemplateDir(cfg.Global.TemplateDir()))
	app.LogBuildNotices(logger, "build notifiers", notifyWarnings)

	eventLog, err := app.NewPersistentEventLog(daemonEventLogLimit, store, func(err error) {
		logger.Warn("persist event failed", logFieldError, err)
	})
	if err != nil {
		logger.Warn("load persisted events failed", logFieldError, err)
	}
	snapshots, err := app.NewPersistentSnapshots(store, func(err error) {
		logger.Warn("persist service snapshots failed", logFieldError, err)
	})
	if err != nil {
		logger.Warn("load persisted service snapshots failed", logFieldError, err)
	}
	watchSnapshots, err := app.NewPersistentWatchSnapshots(store, func(err error) {
		logger.Warn("persist watch snapshots failed", logFieldError, err)
	})
	if err != nil {
		logger.Warn("load persisted watch snapshots failed", logFieldError, err)
	}

	accessLog := openEngineLog(logger, cfg, config.EngineKeyAccess)
	eventFile := openEngineLog(logger, cfg, config.EngineKeyEvents)
	if eventFile != nil {
		eventLog.SetEventFile(eventFile)
	}
	diagFile := openEngineLog(logger, cfg, config.EngineKeyDiagnostics)

	interval := config.EngineInterval(cfg, config.DefaultEngineInterval)
	runner := execx.CommandRunner{}
	var diagnosticLog *app.DiagnosticLog
	if diagFile != nil {
		diagnosticLog = app.NewDiagnosticLog(cfg, nil, diagFile, time.Now)
		go diagnosticLog.Run(ctx, config.EngineDiagnosticsInterval(cfg, config.DefaultEngineDiagnosticsInterval))
	}
	panicGate := app.NewPanicGate(store)
	// webChanges pushes a change signal to connected dashboards (SSE) on every
	// emitted event; with the web UI disabled nobody subscribes and Notify is
	// a cheap no-op.
	webChanges := web.NewBroadcaster()
	userLookup := app.EngineUserLookup(cfg, runner)
	readiness := app.NewReadiness(string(detection.Backend), 0, 0)
	readiness.WatchPanic(panicGate.Active)
	settling := app.NewSettling(readiness)
	deps := app.Deps{
		Backend:          detection.Backend,
		Manager:          manager,
		Runtime:          cfg.Global.RuntimeDir(),
		Interval:         interval,
		DefaultTimeout:   config.EngineDuration(cfg, config.EngineKeyDefaultTimeout, app.DefaultEngineCheckTimeout),
		OperationTimeout: config.EngineDuration(cfg, config.EngineKeyOperationTimeout, app.DefaultEngineOperationTimeout),
		MaxParallel:      config.EngineInt(cfg, config.EngineKeyMaxParallelChecks, app.DefaultEngineMaxParallelChecks),
		//nolint:forbidigo // the engine's injectable Sleep seam; production wires the real clock, tests stub it.
		Sleep: time.Sleep,
		Now:   time.Now,
		// Events go to slog, to the persisted ring the web UI reads, and to
		// the dashboard change stream.
		Emit:                 app.MultiEmit(app.SlogEmitter(logger), eventLog.Add, func(app.Event) { webChanges.Notify() }),
		Monitor:              store,
		OperationSettling:    store,
		ServiceRestartNotice: store,
		Panic:                panicGate,
		RuleState:            store,
		WatchState:           store,
		SLA:                  store,
		DaemonMetrics:        store,
		Notifiers:            notifiers,
		GlobalNotify:         config.NotifyDefault(cfg.Global.Raw),
		GlobalEmission:       emission.Merge(cfg.Global.Raw[emission.Section], emission.Default()),
		GlobalClear:          rules.ClearWindowOrDefault(cfg.Global.Defaults[rules.SectionClearWindow]),
		Snapshots:            snapshots,
		WatchSnapshots:       watchSnapshots,
		Live:                 app.NewLiveMetrics(),
		ServiceMetrics:       app.NewServiceMetricSampler(store),
		Observability:        app.NewObservabilityRegistry(),
		Remediation:          app.NewRemediationRegistry(),
		RuleWindows:          app.NewRuleWindowRegistry(),
		Events:               eventLog,
		DiagnosticLog:        diagnosticLog,
		SystemFreshness:      interval / app.SystemFreshnessIntervalDivisor,
		ExecxRunner:          runner,
		UserLookup:           userLookup,
		Settling:             settling,
	}

	// Startup hygiene, before anything spawns a child of our own: whatever is left
	// in sermod's control group at this point belongs to a previous incarnation the
	// init system did not clean up (KillMode=process/none), and each survivor keeps
	// holding its file descriptors, inotify instances and memory forever.
	if app.ReapOwnStraysEnabled(cfg) {
		if n := (app.SelfStrayHygiene{Emit: deps.Emit}).Run(); n > 0 {
			logger.Warn("terminated leftovers from a previous sermod incarnation", logFieldProcesses, n)
		}
	}

	collector := metrics.New(metrics.OSReader{})
	if deps.SystemFreshness > 0 {
		collector.SystemFreshness = deps.SystemFreshness
	}
	deps.Collector = collector
	deps.DaemonMetricSampler = app.NewDaemonMetricSampler(collector, time.Now, store)

	// One shared /proc snapshot for service discovery: concurrent workers and web
	// runtime queries within a cycle reuse a single walk instead of each scanning
	// every PID. Freshness mirrors the metrics collector's SystemFreshness.
	deps.ProcReader = process.NewCachingReader(process.OSReader{LookupUserName: userLookup.Username}, deps.SystemFreshness)

	// A second collector dedicated to the web's per-cycle live CPU sampling, kept
	// separate from the engine's so their rate deltas never corrupt each other.
	deps.LiveCollector = metrics.New(metrics.OSReader{})
	deps.ArtifactSamples = app.NewArtifactSamples()
	// One resolution cache per startup generation: the workers build and the web
	// backend build probe each service unit once and log its warning once.
	deps.Targets = control.NewTargetCache()

	workers, svcWatches, warnings := app.BuildWorkers(ctx, cfg, deps, collector)
	app.LogBuildNotices(logger, "build workers", warnings)

	watches, watchWarnings := app.BuildWatches(cfg, deps, interval)
	app.LogBuildNotices(logger, "build watches", watchWarnings)
	hostWatches := len(watches)
	// Service-embedded watches (a service's `watches:` section) run the host-watch
	// runtime with per-service scoped check deps; they share the scheduler and
	// readiness settling like host watches.
	watches = append(watches, svcWatches...)
	// Artifact watches share cadence-limited samples for catalog apps, libraries
	// and changed service files.
	artifactWatches := app.BuildArtifactWatches(ctx, cfg, deps)
	watches = append(watches, artifactWatches...)
	logger.Debug("built monitor targets",
		logFieldEnabledServices, len(workers),
		logFieldEnabledWatches, hostWatches,
		logFieldEnabledServiceWatches, len(svcWatches),
		logFieldEnabledLibraries, countArtifactWatches(artifactWatches, config.CategoryLibrary),
		logFieldEnabledApps, countArtifactWatches(artifactWatches, config.CategoryApp),
		logFieldConfigured, app.HasConfiguredTargets(cfg))

	if len(workers) == 0 && len(watches) == 0 {
		if !app.HasConfiguredTargets(cfg) {
			logger.Error("no services or watches configured to monitor")
			return exitFailure
		}
		logger.Warn("all services and watches are disabled; starting with nothing to monitor")
	}

	startupDelay := config.EngineDuration(cfg, config.EngineKeyStartupDelay, 0)
	if startupDelay > 0 {
		logger.Info("sermod waiting before first checks", config.EngineKeyStartupDelay, startupDelay)
	}
	readiness.UpdateCounts(len(workers), len(watches))

	// Write a pidfile under the runtime directory so sermoctl daemon reload (and
	// operators) can reliably signal the running daemon for config reload.
	// This augments the pidfile managed by OpenRC (/run/sermod.pid) and
	// systemd's $MAINPID. Best-effort; failure is only logged.
	{
		pidPath := filepath.Join(rt, daemonPIDFilename)
		if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), daemonPIDFileMode); err != nil {
			logger.Warn("write pidfile failed (daemon reload via sermoctl may need to fall back)", logFieldPath, pidPath, logFieldError, err)
		} else {
			// Best effort cleanup on normal exit (init systems may manage their own).
			defer func(p string) { _ = os.Remove(p) }(pidPath)
		}
	}

	botCfg := telegrambot.ParseConfig(config.SectionMap(cfg.Global.Raw, config.SectionTelegramBot))

	var webHolder *app.WebBackendHolder
	addr, webDisabledReason := webListenAddr(cfg)
	// The web backend feeds both the dashboard and the report bot; build it when
	// either is enabled, even if the HTTP server itself stays off.
	if addr != "" || botCfg.Enabled {
		var webWarnings []string
		webHolder, webWarnings = app.NewWebBackendHolder(ctx, cfg, deps)
		app.LogBuildNotices(logger, "build web backend", webWarnings)
	}

	var webDone <-chan struct{}
	if addr != "" {
		auth := webAuth(cfg)
		warnWorldReadableSecret(logger, cfg)
		token, removeToken := writeWebToken(logger, rt, auth)
		defer removeToken()
		auth.RuntimeToken = token
		server := &web.Server{
			Addr:                   addr,
			Backend:                webHolder,
			Auth:                   auth,
			Hostname:               config.ShortHostname(),
			AllowedHosts:           webAllowedHosts(cfg),
			Logger:                 logger,
			AccessLog:              accessLog,
			MaxSeriesWindow:        app.EngineRetention(cfg).MaxWindow(),
			OperationTimeout:       app.MaxOperationTimeout(cfg, deps.OperationTimeout),
			OperationTimeoutSource: webHolder.MaxOperationTimeout,
			Readiness:              readiness,
			Changes:                webChanges,
			// Trigger reload by signalling ourself with SIGHUP. This re-uses the
			// exact same Monitor.Reload path as sermoctl daemon reload.
			Reload: func() error {
				return (process.OSSignaler{}).Signal(os.Getpid(), syscall.SIGHUP)
			},
		}
		logger.Debug("starting web ui server", logFieldAddress, addr, logFieldAuth, auth.Enabled())
		webDone = goTracked(func() {
			if err := server.Run(ctx); err != nil {
				logger.Error("web server", logFieldError, err)
			}
		})
		if auth.Enabled() {
			logger.Info("sermod web ui listening", logFieldAddress, addr, logFieldAuth, logValueAuthEnabled)
		} else {
			logger.Warn("sermod web ui listening with NO authentication", logFieldAddress, addr)
		}
	} else {
		logger.Warn("web ui disabled; no port will be opened", logFieldReason, webDisabledReason)
	}

	// Interactive read-only report bot (long polling; no inbound socket). It
	// reads the same web backend the dashboard serves and replies to commands
	// from allow-listed chats only.
	var botDone <-chan struct{}
	if botCfg.Enabled {
		bot := telegrambot.New(app.NewTelegramReporter(webHolder, store, time.Now), botCfg, logger)
		deps.TelegramBot = bot
		botDone = goTracked(func() { bot.Run(ctx) })
		logger.Info("telegram report bot enabled", "allowed_chats", len(botCfg.AllowedChats))
	}

	maintenanceDone := startStateMaintenance(ctx, logger, store, app.EngineRollupInterval(cfg))

	logger.Info("sermod starting", logFieldBackend, detection.Backend, logFieldServices, len(workers), logFieldWatches, len(watches))

	monitor := app.NewMonitor(cfg, deps, app.Scheduler{
		Interval:     interval,
		StartupDelay: startupDelay,
	}, readiness, collector, webHolder)
	monitor.ConfigPath = globalPath
	monitor.Logger = logger
	monitor.Init(workers, watches)

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				// Ignore a SIGHUP racing shutdown: reloading against a cancelled
				// context would spawn a fresh generation and emit a spurious
				// "config reloaded" after the daemon reported stopped.
				if ctx.Err() != nil {
					return
				}
				monitor.Reload(ctx)
			}
		}
	}()

	monitor.Run(ctx)
	signal.Stop(hup) // stop SIGHUP delivery; the goroutine exits via ctx.Done()
	// Drain background store users before the deferred store.Close() runs: the
	// web server finishes its bounded graceful shutdown (in-flight requests may
	// query the store) and the startup prune stops at its next step boundary.
	// The prune wait is bounded too: a single DELETE over years of history is
	// not cancellable mid-statement, and shutdown must not outwait an init
	// system's stop timeout — after the deadline the deferred close proceeds
	// and the straggling statement fails with a closed-database error at worst.
	if webDone != nil {
		<-webDone
	}
	if botDone != nil {
		<-botDone
	}
	if !drainOrTimeout(maintenanceDone, shutdownPruneDrainTimeout) {
		logger.Warn("state maintenance still running at shutdown; closing the store without it")
	}
	// Since Go 1.26 NotifyContext records the received signal as the
	// cancellation cause; name it so operators can tell SIGTERM from SIGINT.
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		logger.Info("sermod stopped", logFieldReason, cause)
	} else {
		logger.Info("sermod stopped")
	}
	return 0
}

func openDaemonStore(cfg *config.Config, logger *slog.Logger) (*state.Store, int) {
	store, err := state.OpenContextWith(context.Background(), filepath.Join(cfg.Global.StateDir(), state.Filename), app.EngineStateOptions(cfg))
	if err != nil {
		logger.Error("open state store", logFieldError, err)
		return nil, exitFailure
	}
	return store, 0
}

func acquireDaemonRuntimeLock(cfg *config.Config, logger *slog.Logger) (string, io.Closer, int) {
	runtimeDir := cfg.Global.RuntimeDir()
	if runtimeDir == "" {
		runtimeDir = defaultRuntimeDir
	}
	if err := os.MkdirAll(runtimeDir, daemonRuntimeDirMode); err != nil {
		logger.Warn("create runtime dir failed", logFieldPath, runtimeDir, logFieldError, err)
	}
	lock, err := acquireInstanceLock(runtimeDir)
	if err == nil {
		return runtimeDir, lock, 0
	}
	if busy, ok := errors.AsType[*alreadyRunningError](err); ok && busy.PID > 0 {
		logger.Warn("refusing to start a second sermod instance", logFieldPID, busy.PID)
	} else if ok {
		logger.Warn("refusing to start a second sermod instance")
	} else {
		logger.Warn("acquire sermod instance lock failed", logFieldError, err)
	}
	return runtimeDir, nil, exitAlreadyRunning
}

func detectServiceManager(ctx context.Context, cfg *config.Config, logger *slog.Logger) (servicemgr.Detection, int) {
	backend, err := servicemgr.ParseBackend(config.EngineString(cfg, config.EngineKeyBackend))
	if err != nil {
		logger.Error("backend", logFieldError, err)
		return servicemgr.Detection{}, exitFailure
	}
	detection, err := servicemgr.NewDetector().Detect(ctx, backend)
	if err != nil {
		logger.Error("detect backend", logFieldError, err)
		return servicemgr.Detection{}, exitFailure
	}
	return detection, 0
}

// cliArgs holds the parsed `sermod` command line.
type cliArgs struct {
	command    string
	globalPath string
	verbose    bool
}

func parseArgs(args []string) (cliArgs, error) {
	parsed := cliArgs{globalPath: config.DefaultGlobalPath}
	fs := pflag.NewFlagSet("sermod", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.SetInterspersed(true)
	fs.StringVar(&parsed.globalPath, flagConfig, config.DefaultGlobalPath, "")
	fs.BoolVarP(&parsed.verbose, flagVerbose, shortVerbose, false, "")
	if err := fs.Parse(args); err != nil {
		return cliArgs{}, fmt.Errorf("parse flags: %w", cliutil.NormalizePflagError(err))
	}

	rest := fs.Args()
	if len(rest) > 0 {
		parsed.command = rest[0]
	}
	if len(rest) > 1 {
		return cliArgs{}, fmt.Errorf("unexpected argument %q", rest[1])
	}
	if parsed.command == "" {
		return cliArgs{}, errors.New("missing command")
	}
	return parsed, nil
}

// webListenAddr returns the host:port the web UI should bind to, or "" when the
// web UI is disabled. The second return value explains the decision (a non-empty
// reason when disabled) so `--verbose` can surface why no port was opened.
// Address defaults to loopback.
func webListenAddr(cfg *config.Config) (addr, reason string) {
	bind, err := cfg.Global.WebBind()
	if err != nil {
		return "", err.Error()
	}
	return bind.HostPort(), ""
}

func countArtifactWatches(watches []*app.Watch, category string) int {
	count := 0
	for _, watch := range watches {
		if watch != nil && watch.CheckType == category {
			count++
		}
	}
	return count
}

// webAuth builds the web access control from the `web` block (admin
// credentials, optional guest credentials, optional anonymous guest read
// access).
func webAuth(cfg *config.Config) web.Auth {
	m := cfg.Global.WebSection()
	if m == nil {
		return web.Auth{}
	}
	auth := web.Auth{
		AdminCredentials: cfg.Global.WebCredentials(),
		GuestCredentials: cfg.Global.WebGuestCredentials()}
	auth.AnonymousGuest, _ = m[config.WebKeyGuest].(bool)
	return auth
}

// writeWebToken generates the runtime token that grants sermoctl admin access to
// the web API and writes it to <runtime>/web.token, readable only by the daemon
// user. Hashed credentials leave no password for the CLI to send, so without the
// token `sermoctl status` and friends could not authenticate at all.
//
// It returns the token and a cleanup function; both are empty when auth is
// disabled (an open dashboard needs no credential) or the file cannot be
// written, which is logged and left non-fatal: the dashboard itself still works.
func writeWebToken(logger *slog.Logger, runtimeDir string, auth web.Auth) (string, func()) {
	if !auth.Enabled() {
		return "", func() {}
	}
	path := filepath.Join(runtimeDir, config.DaemonWebTokenFilename)
	token, err := webcred.GenerateSecret()
	if err == nil {
		err = writeFileAtomic(path, []byte(token+"\n"), daemonWebTokenFileMode)
	}
	if err != nil {
		logger.Warn("write web token failed (sermoctl will need SERMO_WEB_PASSWORD)", logFieldPath, path, logFieldError, err)
		return "", func() {}
	}
	return token, func() { _ = os.Remove(path) }
}

// writeFileAtomic writes data through a temporary file in the same directory, so
// a reader never sees a half-written secret and a crash cannot leave one behind
// under the real name.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + tmpFileExt
	// os.WriteFile only applies mode when it creates the file, so a staging file
	// left behind by an earlier crash would donate its own permissions to the
	// secret. Start from nothing.
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove staging file %s: %w", tmp, err)
	}
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return fmt.Errorf("write staging file %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename staging file %s to %s: %w", tmp, path, err)
	}
	return nil
}

// warnWorldReadableSecret logs a password file whose mode lets other users read
// it. It is a warning, not a validation error: the daemon can still run, and
// refusing to start over a permission bit would be worse than saying so.
func warnWorldReadableSecret(logger *slog.Logger, cfg *config.Config) {
	for _, path := range cfg.Global.WebCredentialFiles() {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.Mode().Perm()&secretGroupOtherMask != 0 {
			logger.Warn("web password file is readable beyond its owner (chmod 0600 recommended)",
				logFieldPath, path, logFieldMode, info.Mode().Perm().String())
		}
	}
}

// webAllowedHosts reads web.allowed_hosts: extra Host header names the open
// (auth-less) UI accepts besides localhost, IP literals and the bind host.
func webAllowedHosts(cfg *config.Config) []string {
	m := cfg.Global.WebSection()
	if m == nil {
		return nil
	}
	return cfgval.StringList(m[config.WebKeyAllowedHosts])
}

func openEngineLog(logger *slog.Logger, cfg *config.Config, key string) *logfile.Writer {
	path := config.EngineString(cfg, key)
	if path == "" {
		return nil
	}
	w, err := logfile.Open(path)
	if err != nil {
		logger.Warn("engine log disabled", logFieldKey, key, logFieldPath, path, logFieldError, err)
		return nil
	}
	logger.Info("engine log enabled", logFieldKey, key, logFieldPath, path)
	return w
}

// stateMaintainer consolidates stored history into the coarser archives and
// prunes each resolution to its retention.
type stateMaintainer interface {
	Maintain(ctx context.Context, now time.Time) (state.MaintainResult, error)
}

// startStateMaintenance keeps the resolution ladder current for the daemon's
// lifetime: one pass immediately, then one per interval.
//
// It is started here rather than per configuration generation so a reload does
// not restart the cadence, and it runs off the startup critical path so health
// endpoints and the Web UI bind before the first pass. The pass is interruptible
// between statements: main waits for this goroutine before the deferred
// store.Close(), so continuing would only delay exit.
func startStateMaintenance(ctx context.Context, logger *slog.Logger, store stateMaintainer, interval time.Duration) <-chan struct{} {
	return goTracked(func() { runStateMaintenance(ctx, logger, store, interval) })
}

func runStateMaintenance(ctx context.Context, logger *slog.Logger, store stateMaintainer, interval time.Duration) {
	maintainStateOnce(ctx, logger, store)
	if interval <= 0 {
		interval = state.DefaultRollupInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			maintainStateOnce(ctx, logger, store)
		}
	}
}

func maintainStateOnce(ctx context.Context, logger *slog.Logger, store stateMaintainer) {
	if ctx.Err() != nil {
		return
	}
	result, err := store.Maintain(ctx, time.Now())
	if err != nil {
		logger.Warn("consolidate stored history", logFieldError, err)
	}
	if result.Rolled > 0 || result.Pruned() > 0 {
		logger.Info("consolidated stored history",
			"rolled", result.Rolled, logFieldRows, result.Pruned())
	}
}
