package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"sermo/internal/app"
	"sermo/internal/config"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
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

	interval := engineDuration(cfg, "interval", 30*time.Second)
	deps := app.Deps{
		Backend:         detection.Backend,
		Manager:         manager,
		Runtime:         cfg.Global.RuntimeDir(),
		DefaultTimeout:  engineDuration(cfg, "default_timeout", 10*time.Second),
		MaxParallel:     engineInt(cfg, "max_parallel_checks", 8),
		Sleep:           time.Sleep,
		Now:             time.Now,
		Emit:            app.SlogEmitter(logger),
		Monitor:         store,
		SystemFreshness: interval / 2,
	}

	workers, warnings := app.BuildWorkers(cfg, deps)
	for _, w := range warnings {
		logger.Warn("build workers", "warning", w)
	}
	if len(workers) == 0 {
		logger.Error("no enabled services to monitor")
		return 2
	}

	startupDelay := engineDuration(cfg, "startup_delay", 0)
	if startupDelay > 0 {
		logger.Info("sermod waiting before first checks", "startup_delay", startupDelay)
	}
	logger.Info("sermod starting", "backend", detection.Backend, "services", len(workers))
	scheduler := app.Scheduler{
		Interval:     interval,
		OpSlots:      engineInt(cfg, "max_parallel_operations", 2),
		StartupDelay: startupDelay,
	}
	scheduler.Run(ctx, workers)
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
