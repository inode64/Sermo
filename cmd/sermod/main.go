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
	"sermo/internal/config"
	"sermo/internal/notify"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
	"sermo/internal/web"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	command, globalPath, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "usage error: %v\n", err)
		fmt.Fprintln(os.Stderr, "usage: sermod run [--config /etc/sermo/sermo.yml]")
		return 64
	}
	if command != "run" {
		fmt.Fprintf(os.Stderr, "usage error: unknown command %q\n", command)
		return 64
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Sermo is designed to run as root: it inspects and signals processes owned by
	// other users, controls the service manager, opens raw ICMP sockets and reads
	// privileged /proc entries. It still starts unprivileged, but those features
	// degrade — so warn loudly rather than fail silently.
	if os.Geteuid() != 0 {
		logger.Warn("sermod is not running as root; features that need privileges will be unavailable",
			"euid", os.Geteuid(),
			"affected", "service control, signalling other users' processes, icmp checks, per-process IO, cross-user /proc inspection")
	}

	cfg, err := config.Load(globalPath)
	if err != nil {
		logger.Error("load config", "error", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// SIGHUP is reserved for config reload (post-MVP); log it instead of letting
	// its default disposition terminate the daemon (section 24).
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			logger.Warn("SIGHUP received; config reload is not supported in the MVP")
		}
	}()

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

	interval := engineDuration(cfg, "interval", 30*time.Second)
	deps := app.Deps{
		Backend:        detection.Backend,
		Manager:        manager,
		Runtime:        cfg.Global.RuntimeDir(),
		Interval:       interval,
		DefaultTimeout: engineDuration(cfg, "default_timeout", 10*time.Second),
		MaxParallel:    engineInt(cfg, "max_parallel_checks", 8),
		Sleep:          time.Sleep,
		Now:            time.Now,
		// Events go to slog and to the in-memory log the web UI reads.
		Emit:            app.MultiEmit(app.SlogEmitter(logger), eventLog.Add),
		Monitor:         store,
		SLA:             store,
		Notifiers:       notifiers,
		Snapshots:       app.NewSnapshots(),
		Events:          eventLog,
		SystemFreshness: interval / 2,
	}

	// Bound the SLA table to roughly a year of per-minute samples per service.
	if n, err := store.PruneSLA(time.Now().Add(-366 * 24 * time.Hour)); err != nil {
		logger.Warn("prune sla samples", "error", err)
	} else if n > 0 {
		logger.Info("pruned old sla samples", "rows", n)
	}

	workers, warnings := app.BuildWorkers(cfg, deps)
	for _, w := range warnings {
		logger.Warn("build workers", "warning", w)
	}

	watches, watchWarnings := app.BuildWatches(cfg, deps, interval)
	for _, w := range watchWarnings {
		logger.Warn("build watches", "warning", w)
	}

	if len(workers) == 0 && len(watches) == 0 {
		logger.Error("no enabled services or watches to monitor")
		return 2
	}

	startupDelay := engineDuration(cfg, "startup_delay", 0)
	if startupDelay > 0 {
		logger.Info("sermod waiting before first checks", "startup_delay", startupDelay)
	}
	if addr := webListenAddr(cfg); addr != "" {
		backend, webWarnings := app.NewWebBackend(cfg, deps)
		for _, w := range webWarnings {
			logger.Warn("build web backend", "warning", w)
		}
		auth := webAuth(cfg)
		server := &web.Server{Addr: addr, Backend: backend, Auth: auth, Logger: logger}
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
	}

	logger.Info("sermod starting", "backend", detection.Backend, "services", len(workers), "watches", len(watches))
	scheduler := app.Scheduler{
		Interval:     interval,
		OpSlots:      engineInt(cfg, "max_parallel_operations", 2),
		StartupDelay: startupDelay,
	}
	scheduler.Run(ctx, workers, watches)
	logger.Info("sermod stopped")
	return 0
}

func parseArgs(args []string) (command, globalPath string, err error) {
	globalPath = config.DefaultGlobalPath
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case strings.HasPrefix(arg, "--config="):
			globalPath = strings.TrimPrefix(arg, "--config=")
		case arg == "--config":
			i++
			if i >= len(args) {
				return "", "", fmt.Errorf("--config requires a value")
			}
			globalPath = args[i]
		case strings.HasPrefix(arg, "-"):
			return "", "", fmt.Errorf("unknown flag %s", arg)
		case command == "":
			command = arg
		default:
			return "", "", fmt.Errorf("unexpected argument %q", arg)
		}
	}
	if command == "" {
		return "", "", fmt.Errorf("missing command")
	}
	return command, globalPath, nil
}

func notifiersRaw(cfg *config.Config) map[string]any {
	m, _ := cfg.Global.Raw["notifiers"].(map[string]any)
	return m
}

// webListenAddr returns the host:port the web UI should bind to, or "" when no
// web.port is configured (web disabled). Address defaults to loopback.
func webListenAddr(cfg *config.Config) string {
	m, _ := cfg.Global.Raw["web"].(map[string]any)
	if m == nil {
		return ""
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
	}
	if port <= 0 {
		return ""
	}
	address, _ := m["address"].(string)
	if address == "" {
		address = "127.0.0.1"
	}
	return net.JoinHostPort(address, strconv.Itoa(port))
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

func engineDuration(cfg *config.Config, key string, fallback time.Duration) time.Duration {
	s, _ := engineMap(cfg)[key].(string)
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

func engineInt(cfg *config.Config, key string, fallback int) int {
	switch v := engineMap(cfg)[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case uint64:
		return int(v)
	case float64:
		return int(v)
	default:
		return fallback
	}
}
