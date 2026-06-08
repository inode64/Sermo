// Command sermod is the Sermo monitoring daemon.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"sermo/internal/app"
	"sermo/internal/buildinfo"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/notify"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
	"sermo/internal/web"
)

const exitConfigInvalid = 78

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	for _, a := range args {
		if a == "version" || a == "--version" || a == "-V" {
			fmt.Println(buildinfo.String())
			return 0
		}
	}
	parsed, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "usage error: %v\n", err)
		fmt.Fprintln(os.Stderr, "usage: sermod run [--config /etc/sermo/sermo.yml] [--profiles DIR ...] [--verbose|-v]")
		fmt.Fprintln(os.Stderr, "       sermod version")
		return 64
	}
	if parsed.command != "run" {
		fmt.Fprintf(os.Stderr, "usage error: unknown command %q\n", parsed.command)
		return 64
	}
	globalPath := parsed.globalPath

	level := slog.LevelInfo
	if parsed.verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if parsed.verbose {
		logger.Debug("verbose logging enabled", "config", globalPath)
	}

	// Sermo is designed to run as root: it inspects and signals processes owned by
	// other users, controls the service manager, opens raw ICMP sockets and reads
	// privileged /proc entries. It still starts unprivileged, but those features
	// degrade — so warn loudly rather than fail silently.
	if os.Geteuid() != 0 {
		logger.Warn("sermod is not running as root; features that need privileges will be unavailable",
			"euid", os.Geteuid(),
			"affected", "service control, signalling other users' processes, icmp checks, per-process IO, cross-user /proc inspection")
	}

	var loadOpts []config.Option
	if len(parsed.profiles) > 0 {
		loadOpts = append(loadOpts, config.WithProfilesDirs(parsed.profiles...))
		logger.Debug("overriding profile directories", "profiles", parsed.profiles)
	}
	cfg, err := config.Load(globalPath, loadOpts...)
	if err != nil {
		logger.Error("load config", "error", err)
		return 2
	}
	if issues := config.Validate(cfg); len(issues) > 0 {
		for _, is := range issues {
			logger.Error("config invalid", "scope", is.Scope, "message", is.Msg)
		}
		return exitConfigInvalid
	}
	logger.Debug("config loaded", "path", globalPath, "services", len(cfg.Services))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	detector := servicemgr.NewDetector()
	backend, err := servicemgr.ParseBackend(engineString(cfg, "backend"))
	if err != nil {
		logger.Error("backend", "error", err)
		return 2
	}
	detection, err := detector.Detect(ctx, backend)
	if err != nil {
		logger.Error("detect backend", "error", err)
		return 2
	}
	manager, err := servicemgr.NewManager(detection.Backend)
	if err != nil {
		logger.Error("service manager", "error", err)
		return 2
	}
	logger.Debug("service backend detected", "backend", detection.Backend)

	store, err := state.Open(filepath.Join(cfg.Global.StateDir(), state.Filename))
	if err != nil {
		logger.Error("open state store", "error", err)
		return 2
	}
	defer store.Close()

	notifiers, notifyWarnings := notify.Build(notifiersRaw(cfg))
	for _, w := range notifyWarnings {
		logger.Warn("build notifiers", "warning", w)
	}

	eventLog := app.NewEventLog(1000)

	interval := app.EngineInterval(cfg, 30*time.Second)
	opGate := app.NewOpGate(app.EngineInt(cfg, "max_parallel_operations", 2), cfg.Global.RuntimeDir())
	deps := app.Deps{
		Backend:          detection.Backend,
		Manager:          manager,
		Runtime:          cfg.Global.RuntimeDir(),
		Interval:         interval,
		DefaultTimeout:   app.EngineDuration(cfg, "default_timeout", 10*time.Second),
		OperationTimeout: app.EngineDuration(cfg, "operation_timeout", 90*time.Second),
		MaxParallel:      app.EngineInt(cfg, "max_parallel_checks", 8),
		Sleep:            time.Sleep,
		Now:              time.Now,
		// Events go to slog and to the in-memory log the web UI reads.
		Emit:            app.MultiEmit(app.SlogEmitter(logger), eventLog.Add),
		Monitor:         store,
		SLA:             store,
		Notifiers:       notifiers,
		Snapshots:       app.NewSnapshots(),
		Remediation:     app.NewRemediationRegistry(),
		RuleWindows:     app.NewRuleWindowRegistry(),
		Events:          eventLog,
		SystemFreshness: interval / 2,
		OpGate:          opGate,
		ExecxRunner:     execx.CommandRunner{},
	}

	// Bound the SLA and measurement tables to roughly a year of per-minute data.
	cutoff := time.Now().Add(-366 * 24 * time.Hour)
	if n, err := store.PruneSLA(cutoff); err != nil {
		logger.Warn("prune sla samples", "error", err)
	} else if n > 0 {
		logger.Info("pruned old sla samples", "rows", n)
	}
	if n, err := store.PruneMeasurements(cutoff); err != nil {
		logger.Warn("prune measurements", "error", err)
	} else if n > 0 {
		logger.Info("pruned old measurements", "rows", n)
	}

	collector := metrics.New(metrics.OSReader{})
	if deps.SystemFreshness > 0 {
		collector.SystemFreshness = deps.SystemFreshness
	}
	deps.Collector = collector

	workers, warnings := app.BuildWorkers(cfg, deps, collector)
	for _, w := range warnings {
		logger.Warn("build workers", "warning", w)
	}

	watches, watchWarnings := app.BuildWatches(cfg, deps, interval)
	for _, w := range watchWarnings {
		logger.Warn("build watches", "warning", w)
	}
	logger.Debug("built monitor targets", "enabled_services", len(workers), "enabled_watches", len(watches), "configured", app.HasConfiguredTargets(cfg))

	if len(workers) == 0 && len(watches) == 0 {
		if !app.HasConfiguredTargets(cfg) {
			logger.Error("no services or watches configured to monitor")
			return 2
		}
		logger.Warn("all services and watches are disabled; starting with nothing to monitor")
	}

	startupDelay := app.EngineDuration(cfg, "startup_delay", 0)
	if startupDelay > 0 {
		logger.Info("sermod waiting before first checks", "startup_delay", startupDelay)
	}
	readiness := app.NewReadiness(string(detection.Backend), len(workers), len(watches))

	// Write a pidfile under the runtime directory so sermoctl reload (and
	// operators) can reliably signal the running daemon for config reload.
	// This augments the pidfile managed by OpenRC (/run/sermod.pid) and
	// systemd's $MAINPID. Best-effort; failure is only logged.
	{
		pidDir := cfg.Global.RuntimeDir()
		if pidDir == "" {
			pidDir = "/run/sermo"
		}
		pidPath := filepath.Join(pidDir, "sermod.pid")
		if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil { //nolint:gosec // G306: pidfile is intentionally world-readable (0644)
			logger.Warn("write pidfile failed (reload via sermoctl may need to fall back)", "path", pidPath, "error", err)
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
			logger.Warn("build web backend", "warning", w)
		}
		auth := webAuth(cfg)
		server := &web.Server{
			Addr:             addr,
			Backend:          webHolder,
			Auth:             auth,
			Logger:           logger,
			OperationTimeout: app.MaxOperationTimeout(cfg, deps.OperationTimeout),
			Readiness:        readiness,
			// Trigger reload by signalling ourself with SIGHUP. This re-uses the
			// exact same Monitor.Reload path as external SIGHUP, systemd
			// ExecReload, or sermoctl (when it finds the pid).
			Reload: func() error {
				return syscall.Kill(os.Getpid(), syscall.SIGHUP)
			},
		}
		logger.Debug("starting web ui server", "address", addr, "auth", auth.Enabled())
		go func() {
			if err := server.Run(ctx); err != nil {
				logger.Error("web server", "error", err)
			}
		}()
		if auth.Enabled() {
			logger.Info("sermod web ui listening", "address", addr, "auth", "enabled")
		} else {
			logger.Warn("sermod web ui listening with NO authentication", "address", addr)
		}
	} else {
		logger.Warn("web ui disabled; no port will be opened", "reason", webDisabledReason)
	}

	logger.Info("sermod starting", "backend", detection.Backend, "services", len(workers), "watches", len(watches))

	monitor := app.NewMonitor(cfg, deps, app.Scheduler{
		Interval:     interval,
		OpSlots:      app.EngineInt(cfg, "max_parallel_operations", 2),
		StartupDelay: startupDelay,
	}, readiness, collector, webHolder)
	monitor.ConfigPath = globalPath
	monitor.ProfileDirs = parsed.profiles
	monitor.Logger = logger
	monitor.Init(workers, watches)

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			monitor.Reload()
		}
	}()

	monitor.Run(ctx)
	logger.Info("sermod stopped")
	return 0
}

// cliArgs holds the parsed `sermod` command line.
type cliArgs struct {
	command    string
	globalPath string
	verbose    bool
	// profiles overrides paths.profiles from the global config. Repeatable;
	// each --profiles adds a directory. Empty means use the config as-is.
	profiles []string
}

func parseArgs(args []string) (cliArgs, error) {
	parsed := cliArgs{globalPath: config.DefaultGlobalPath}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case strings.HasPrefix(arg, "--config="):
			parsed.globalPath = strings.TrimPrefix(arg, "--config=")
		case arg == "--config":
			i++
			if i >= len(args) {
				return cliArgs{}, fmt.Errorf("--config requires a value")
			}
			parsed.globalPath = args[i]
		case strings.HasPrefix(arg, "--profiles="):
			parsed.profiles = append(parsed.profiles, strings.TrimPrefix(arg, "--profiles="))
		case arg == "--profiles":
			i++
			if i >= len(args) {
				return cliArgs{}, fmt.Errorf("--profiles requires a value")
			}
			parsed.profiles = append(parsed.profiles, args[i])
		case arg == "--verbose" || arg == "-v":
			parsed.verbose = true
		case strings.HasPrefix(arg, "-"):
			return cliArgs{}, fmt.Errorf("unknown flag %s", arg)
		case parsed.command == "":
			parsed.command = arg
		default:
			return cliArgs{}, fmt.Errorf("unexpected argument %q", arg)
		}
	}
	if parsed.command == "" {
		return cliArgs{}, fmt.Errorf("missing command")
	}
	return parsed, nil
}

func notifiersRaw(cfg *config.Config) map[string]any {
	m, _ := cfg.Global.Raw["notifiers"].(map[string]any)
	return m
}

// webListenAddr returns the host:port the web UI should bind to, or "" when the
// web UI is disabled. The second return value explains the decision (a non-empty
// reason when disabled) so `--verbose` can surface why no port was opened.
// Address defaults to loopback.
func webListenAddr(cfg *config.Config) (addr, reason string) {
	m, _ := cfg.Global.Raw["web"].(map[string]any)
	if m == nil {
		return "", "no [web] section in config"
	}
	if _, present := m["port"]; !present {
		return "", "web.port is not set"
	}
	port := 0
	switch v := m["port"].(type) {
	case int:
		port = v
	case int64:
		port = int(v)
	case uint64:
		port = int(v)
	case float64:
		port = int(v)
	default:
		return "", fmt.Sprintf("web.port is not a number (%T)", m["port"])
	}
	if port <= 0 {
		return "", fmt.Sprintf("web.port must be positive (got %d)", port)
	}
	address, _ := m["address"].(string)
	if address == "" {
		address = "127.0.0.1"
	}
	return net.JoinHostPort(address, strconv.Itoa(port)), ""
}

// webAuth builds the web access control from the `web` block (admin password,
// optional guest password, optional anonymous guest read access).
func webAuth(cfg *config.Config) web.Auth {
	m, _ := cfg.Global.Raw["web"].(map[string]any)
	if m == nil {
		return web.Auth{}
	}
	auth := web.Auth{}
	auth.AdminPassword, _ = m["password"].(string)
	auth.GuestPassword, _ = m["guest_password"].(string)
	auth.AnonymousGuest, _ = m["guest"].(bool)
	return auth
}

func engineMap(cfg *config.Config) map[string]any {
	m, _ := cfg.Global.Raw["engine"].(map[string]any)
	return m
}

func engineString(cfg *config.Config, key string) string {
	s, _ := engineMap(cfg)[key].(string)
	return s
}
