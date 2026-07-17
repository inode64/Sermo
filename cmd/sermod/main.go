// Command sermod is the Sermo monitoring daemon.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	"sermo/internal/app"
	"sermo/internal/buildinfo"
	"sermo/internal/cfgval"
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
	"sermo/internal/web"
)

const (
	exitConfigInvalid  = 78
	exitAlreadyRunning = 1
	exitFailure        = 2
	exitUsage          = 64
)

const (
	commandRun             = "run"
	commandVersion         = "version"
	flagConfig             = "config"
	flagVerbose            = "verbose"
	flagVersion            = "--version"
	flagVersionAlt         = "-V"
	pflagUnknownFlagPrefix = "unknown flag: "
	shortVerbose           = "v"
)

const (
	defaultRuntimeDir    = config.DefaultRuntime
	defaultWebAddress    = "127.0.0.1"
	daemonPIDFilename    = config.DaemonPIDFilename
	instanceLockFilename = "sermod.lock"
	daemonEventLogLimit  = 1000
	daemonPIDFileMode    = 0o644
	daemonRuntimeDirMode = 0o700
)

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
	logFieldPath                  = "path"
	logFieldPID                   = "pid"
	logFieldReason                = "reason"
	logFieldRows                  = "rows"
	logFieldScope                 = "scope"
	logFieldServices              = "services"
	logFieldWarning               = "warning"
	logFieldWatches               = "watches"

	logValueAuthEnabled = "enabled"
)

func main() {
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
		fmt.Println(buildinfo.String())
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
	if exitCode != 0 {
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
	defer instanceLock.Close()

	store, exitCode := openDaemonStore(cfg, logger)
	if exitCode != 0 {
		return exitCode
	}
	defer store.Close()

	notifiers, notifyWarnings := notify.Build(cfg.Notifiers(), notify.WithTemplateDir(cfg.Global.TemplateDir()))
	for _, w := range notifyWarnings {
		logger.Warn("build notifiers", logFieldWarning, w)
	}

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
	opGate := app.NewOpGate(app.EngineInt(cfg, config.EngineKeyMaxParallelOperations, app.DefaultEngineMaxParallelOperations), cfg.Global.RuntimeDir())
	var diagnosticLog *app.DiagnosticLog
	if diagFile != nil {
		diagnosticLog = app.NewDiagnosticLog(cfg, nil, opGate, diagFile, time.Now)
		go diagnosticLog.Run(ctx, config.EngineDiagnosticsInterval(cfg, config.DefaultEngineDiagnosticsInterval))
	}
	panicGate := app.NewPanicGate(store)
	userLookup := app.EngineUserLookup(cfg, runner)
	readiness := app.NewReadiness(string(detection.Backend), 0, 0)
	readiness.WatchPanic(panicGate.Active)
	settling := app.NewSettling(readiness)
	deps := app.Deps{
		Backend:          detection.Backend,
		Manager:          manager,
		Runtime:          cfg.Global.RuntimeDir(),
		Interval:         interval,
		DefaultTimeout:   app.EngineDuration(cfg, config.EngineKeyDefaultTimeout, app.DefaultEngineCheckTimeout),
		OperationTimeout: app.EngineDuration(cfg, config.EngineKeyOperationTimeout, app.DefaultEngineOperationTimeout),
		MaxParallel:      app.EngineInt(cfg, config.EngineKeyMaxParallelChecks, app.DefaultEngineMaxParallelChecks),
		Sleep:            time.Sleep,
		Now:              time.Now,
		// Events go to slog and to the persisted ring the web UI reads.
		Emit:              app.MultiEmit(app.SlogEmitter(logger), eventLog.Add),
		Monitor:           store,
		OperationSettling: store,
		Panic:             panicGate,
		RuleState:         store,
		WatchState:        store,
		SLA:               store,
		ProcessUptime:     store,
		DaemonMetrics:     store,
		Notifiers:         notifiers,
		GlobalNotify:      config.NotifyDefault(cfg.Global.Raw),
		GlobalEmission:    emission.Merge(cfg.Global.Raw[emission.Section], emission.Default()),
		GlobalClear:       rules.ClearWindowOrDefault(cfg.Global.Defaults[rules.SectionClearWindow]),
		Snapshots:         snapshots,
		WatchSnapshots:    watchSnapshots,
		Live:              app.NewLiveMetrics(),
		ServiceMetrics:    app.NewServiceMetricSampler(store),
		Observability:     app.NewObservabilityRegistry(),
		Remediation:       app.NewRemediationRegistry(),
		RuleWindows:       app.NewRuleWindowRegistry(),
		Events:            eventLog,
		DiagnosticLog:     diagnosticLog,
		SystemFreshness:   interval / app.SystemFreshnessIntervalDivisor,
		OpGate:            opGate,
		ExecxRunner:       runner,
		UserLookup:        userLookup,
		Settling:          settling,
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
	for _, w := range watchWarnings {
		logger.Warn("build watches", logFieldWarning, w)
	}
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

	startupDelay := app.EngineDuration(cfg, config.EngineKeyStartupDelay, 0)
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

	var webHolder *app.WebBackendHolder
	addr, webDisabledReason := webListenAddr(cfg)
	if addr != "" {
		var webWarnings []string
		webHolder, webWarnings = app.NewWebBackendHolder(ctx, cfg, deps)
		app.LogBuildNotices(logger, "build web backend", webWarnings)
		auth := webAuth(cfg)
		server := &web.Server{
			Addr:                   addr,
			Backend:                webHolder,
			Auth:                   auth,
			Logger:                 logger,
			AccessLog:              accessLog,
			OperationTimeout:       app.MaxOperationTimeout(cfg, deps.OperationTimeout),
			OperationTimeoutSource: webHolder.MaxOperationTimeout,
			Readiness:              readiness,
			// Trigger reload by signalling ourself with SIGHUP. This re-uses the
			// exact same Monitor.Reload path as sermoctl daemon reload.
			Reload: func() error {
				return (process.OSSignaler{}).Signal(os.Getpid(), syscall.SIGHUP)
			},
		}
		logger.Debug("starting web ui server", logFieldAddress, addr, logFieldAuth, auth.Enabled())
		go func() {
			if err := server.Run(ctx); err != nil {
				logger.Error("web server", logFieldError, err)
			}
		}()
		if auth.Enabled() {
			logger.Info("sermod web ui listening", logFieldAddress, addr, logFieldAuth, logValueAuthEnabled)
		} else {
			logger.Warn("sermod web ui listening with NO authentication", logFieldAddress, addr)
		}
	} else {
		logger.Warn("web ui disabled; no port will be opened", logFieldReason, webDisabledReason)
	}

	startOldHistoryPrune(ctx, logger, store, time.Now().Add(-state.DefaultHistoryRetention))

	logger.Info("sermod starting", logFieldBackend, detection.Backend, logFieldServices, len(workers), logFieldWatches, len(watches))

	monitor := app.NewMonitor(cfg, deps, app.Scheduler{
		Interval:     interval,
		OpSlots:      app.EngineInt(cfg, config.EngineKeyMaxParallelOperations, app.DefaultEngineMaxParallelOperations),
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
	store, err := state.OpenContextWith(context.Background(), filepath.Join(cfg.Global.StateDir(), state.Filename), state.Options{CacheBytes: app.EngineByteSize(cfg, config.EngineKeyStateCacheSize, state.DefaultCacheBytes)})
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
	backend, err := servicemgr.ParseBackend(app.EngineString(cfg, config.EngineKeyBackend))
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
		return cliArgs{}, normalizePflagError(err)
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

func normalizePflagError(err error) error {
	if msg := err.Error(); strings.HasPrefix(msg, pflagUnknownFlagPrefix) {
		return fmt.Errorf("unknown flag %s", strings.TrimPrefix(msg, pflagUnknownFlagPrefix))
	}
	return err
}

// webListenAddr returns the host:port the web UI should bind to, or "" when the
// web UI is disabled. The second return value explains the decision (a non-empty
// reason when disabled) so `--verbose` can surface why no port was opened.
// Address defaults to loopback.
func webListenAddr(cfg *config.Config) (addr, reason string) {
	m, _ := cfg.Global.Raw[config.SectionWeb].(map[string]any)
	if m == nil {
		return "", "no [web] section in config"
	}
	if _, present := m[config.WebKeyPort]; !present {
		return "", "web.port is not set"
	}
	// cfgval.Int accepts the same shapes config validation does (including a
	// quoted "9797"), so a config that validates never silently disables the UI.
	port, ok := cfgval.Int(m[config.WebKeyPort])
	if !ok {
		return "", fmt.Sprintf("web.port is not a number (%T)", m[config.WebKeyPort])
	}
	if !cfgval.ValidTCPPort(port) {
		return "", fmt.Sprintf("web.port must be in %s (got %d)", cfgval.TCPPortRange(), port)
	}
	address, _ := m[config.WebKeyAddress].(string)
	if address == "" {
		address = defaultWebAddress
	}
	return net.JoinHostPort(address, strconv.Itoa(port)), ""
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

// webAuth builds the web access control from the `web` block (admin password,
// optional guest password, optional anonymous guest read access).
func webAuth(cfg *config.Config) web.Auth {
	m, _ := cfg.Global.Raw[config.SectionWeb].(map[string]any)
	if m == nil {
		return web.Auth{}
	}
	auth := web.Auth{}
	auth.AdminPassword, _ = m[config.WebKeyPassword].(string)
	auth.GuestPassword, _ = m[config.WebKeyGuestPassword].(string)
	auth.AnonymousGuest, _ = m[config.WebKeyGuest].(bool)
	return auth
}

func openEngineLog(logger *slog.Logger, cfg *config.Config, key string) *logfile.Writer {
	path := config.EngineLogPath(cfg, key)
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

type oldHistoryPruner interface {
	PruneSLA(before time.Time) (int64, error)
	PruneMeasurements(before time.Time) (int64, error)
	PruneMetrics(before time.Time) (int64, error)
	PruneDaemonMetrics(before time.Time) (int64, error)
	PruneServiceMetrics(before time.Time) (int64, error)
	PruneEvents(before time.Time) (int64, error)
}

func startOldHistoryPrune(ctx context.Context, logger *slog.Logger, store oldHistoryPruner, cutoff time.Time) {
	go func() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		pruneOldHistory(logger, store, cutoff)
	}()
}

func pruneOldHistory(logger *slog.Logger, store oldHistoryPruner, cutoff time.Time) {
	// Retention can scan large history tables on long-lived installations. Keep it
	// out of the startup critical path so health endpoints and the Web UI bind
	// before old samples are removed.
	for _, p := range []struct {
		what  string
		prune func(time.Time) (int64, error)
	}{
		{"sla samples", store.PruneSLA},
		{"measurements", store.PruneMeasurements},
		{"metrics", store.PruneMetrics},
		{"daemon metrics", store.PruneDaemonMetrics},
		{"service metrics", store.PruneServiceMetrics},
		{"events", store.PruneEvents},
	} {
		if n, err := p.prune(cutoff); err != nil {
			logger.Warn("prune "+p.what, logFieldError, err)
		} else if n > 0 {
			logger.Info("pruned old "+p.what, logFieldRows, n)
		}
	}
}
