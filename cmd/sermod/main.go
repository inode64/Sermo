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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	"sermo/internal/app"
	"sermo/internal/buildinfo"
	"sermo/internal/cfgval"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/logfile"
	"sermo/internal/metrics"
	"sermo/internal/notify"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
	"sermo/internal/web"
)

const (
	exitConfigInvalid  = 78
	exitAlreadyRunning = 1
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
	daemonPIDFilename    = "sermod.pid"
	instanceLockFilename = "sermod.lock"
)

const (
	logFieldAddress               = "address"
	logFieldAffected              = "affected"
	logFieldAuth                  = "auth"
	logFieldBackend               = "backend"
	logFieldConfig                = "config"
	logFieldConfigured            = "configured"
	logFieldEnabledApps           = "enabled_apps"
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

func run(args []string) int {
	// `version` is a subcommand: honor it only as the first argument, never when
	// it appears as a flag value (e.g. `--config version`). The flag forms may
	// appear anywhere.
	if len(args) > 0 && args[0] == commandVersion {
		fmt.Println(buildinfo.String())
		return 0
	}
	for _, a := range args {
		if a == flagVersion || a == flagVersionAlt {
			fmt.Println(buildinfo.String())
			return 0
		}
	}
	parsed, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "usage error: %v\n", err)
		fmt.Fprintln(os.Stderr, "usage: sermod run [--config /etc/sermo/sermo.yml] [--verbose|-v]")
		fmt.Fprintln(os.Stderr, "       sermod version")
		return exitUsage
	}
	if parsed.command != commandRun {
		fmt.Fprintf(os.Stderr, "usage error: unknown command %q\n", parsed.command)
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

	cfg, err := config.Load(globalPath)
	if err != nil {
		logger.Error("load config", logFieldError, err)
		return 2
	}
	if issues := config.Validate(cfg); len(issues) > 0 {
		for _, is := range issues {
			logger.Error("config invalid", logFieldScope, is.Scope, logFieldMessage, is.Msg)
		}
		return exitConfigInvalid
	}
	logger.Debug("config loaded", logFieldPath, globalPath, logFieldServices, len(cfg.Services))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	detector := servicemgr.NewDetector()
	backend, err := servicemgr.ParseBackend(app.EngineString(cfg, config.EngineKeyBackend))
	if err != nil {
		logger.Error("backend", logFieldError, err)
		return 2
	}
	detection, err := detector.Detect(ctx, backend)
	if err != nil {
		logger.Error("detect backend", logFieldError, err)
		return 2
	}
	manager, err := servicemgr.NewManager(detection.Backend)
	if err != nil {
		logger.Error("service manager", logFieldError, err)
		return 2
	}
	logger.Debug("service backend detected", logFieldBackend, detection.Backend)

	// Ensure the runtime root exists owner-only (root) before any lock dir or the
	// pidfile is created under it, so it stays 0700 even when the packaging
	// (tmpfiles.d / OpenRC) has not pre-created it. Best-effort.
	rt := cfg.Global.RuntimeDir()
	if rt == "" {
		rt = defaultRuntimeDir
	}
	if err := os.MkdirAll(rt, 0o700); err != nil {
		logger.Warn("create runtime dir failed", logFieldPath, rt, logFieldError, err)
	}

	instanceLock, err := acquireInstanceLock(rt)
	if err != nil {
		var busy *alreadyRunningError
		if errors.As(err, &busy) {
			if busy.PID > 0 {
				logger.Warn("refusing to start a second sermod instance", logFieldPID, busy.PID)
			} else {
				logger.Warn("refusing to start a second sermod instance")
			}
		} else {
			logger.Warn("acquire sermod instance lock failed", logFieldError, err)
		}
		return exitAlreadyRunning
	}
	defer instanceLock.Close()

	store, err := state.OpenWith(
		filepath.Join(cfg.Global.StateDir(), state.Filename),
		state.Options{CacheBytes: app.EngineByteSize(cfg, config.EngineKeyStateCacheSize, state.DefaultCacheBytes)},
	)
	if err != nil {
		logger.Error("open state store", logFieldError, err)
		return 2
	}
	defer store.Close()

	notifiers, notifyWarnings := notify.Build(cfg.Notifiers(), notify.WithTemplateDir(cfg.Global.TemplateDir()))
	for _, w := range notifyWarnings {
		logger.Warn("build notifiers", logFieldWarning, w)
	}

	// Bound persisted history to roughly a year of data before hydrating the
	// recent-event ring.
	cutoff := time.Now().Add(-state.DefaultHistoryRetention)
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

	eventLog, err := app.NewPersistentEventLog(1000, store, func(err error) {
		logger.Warn("persist event failed", logFieldError, err)
	})
	if err != nil {
		logger.Warn("load persisted events failed", logFieldError, err)
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
		SLA:               store,
		DaemonMetrics:     store,
		Notifiers:         notifiers,
		GlobalNotify:      config.NotifyDefault(cfg.Global.Raw),
		Snapshots:         app.NewSnapshots(),
		Live:              app.NewLiveMetrics(),
		ServiceMetrics:    app.NewServiceMetricSampler(store),
		Observability:     app.NewObservabilityRegistry(),
		Remediation:       app.NewRemediationRegistry(),
		RuleWindows:       app.NewRuleWindowRegistry(),
		Events:            eventLog,
		DiagnosticLog:     diagnosticLog,
		SystemFreshness:   interval / 2,
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

	// One shared /proc snapshot for service discovery: concurrent workers and web
	// runtime queries within a cycle reuse a single walk instead of each scanning
	// every PID. Freshness mirrors the metrics collector's SystemFreshness.
	deps.ProcReader = process.NewCachingReader(process.OSReader{LookupUserName: userLookup.Username}, deps.SystemFreshness)

	// A second collector dedicated to the web's per-cycle live CPU sampling, kept
	// separate from the engine's so their rate deltas never corrupt each other.
	deps.LiveCollector = metrics.New(metrics.OSReader{})

	workers, svcWatches, warnings := app.BuildWorkers(cfg, deps, collector)
	for _, w := range warnings {
		logger.Warn("build workers", logFieldWarning, w)
	}

	watches, watchWarnings := app.BuildWatches(cfg, deps, interval)
	for _, w := range watchWarnings {
		logger.Warn("build watches", logFieldWarning, w)
	}
	hostWatches := len(watches)
	// Service-embedded watches (a service's `watches:` section) run the host-watch
	// runtime with per-service scoped check deps; they share the scheduler and
	// readiness settling like host watches.
	watches = append(watches, svcWatches...)
	// App-watches monitor installed applications for errors on a slower cadence.
	// They share the scheduler/generation machinery and count toward readiness
	// first-cycle settling alongside host watches.
	appWatches := app.BuildAppWatches(cfg, deps)
	watches = append(watches, appWatches...)
	logger.Debug("built monitor targets",
		logFieldEnabledServices, len(workers),
		logFieldEnabledWatches, hostWatches,
		logFieldEnabledServiceWatches, len(svcWatches),
		logFieldEnabledApps, len(appWatches),
		logFieldConfigured, app.HasConfiguredTargets(cfg))

	if len(workers) == 0 && len(watches) == 0 {
		if !app.HasConfiguredTargets(cfg) {
			logger.Error("no services or watches configured to monitor")
			return 2
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
		if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil { //nolint:gosec // G306: pidfile is intentionally world-readable (0644)
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
		webHolder, webWarnings = app.NewWebBackendHolder(cfg, deps)
		for _, w := range webWarnings {
			logger.Warn("build web backend", logFieldWarning, w)
		}
		auth := webAuth(cfg)
		server := &web.Server{
			Addr:             addr,
			Backend:          webHolder,
			Auth:             auth,
			Logger:           logger,
			AccessLog:        accessLog,
			OperationTimeout: app.MaxOperationTimeout(cfg, deps.OperationTimeout),
			Readiness:        readiness,
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
				monitor.Reload()
			}
		}
	}()

	monitor.Run(ctx)
	signal.Stop(hup) // stop SIGHUP delivery; the goroutine exits via ctx.Done()
	logger.Info("sermod stopped")
	return 0
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
		return cliArgs{}, fmt.Errorf("missing command")
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
