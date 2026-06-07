package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"sermo/internal/config"
	"sermo/internal/metrics"
	"sermo/internal/notify"
)

// Monitor runs service workers and host watches in reloadable generations.
// SIGHUP triggers Reload, which validates new config, preserves per-service
// runtime state, stops the current generation gracefully, and starts a new one.
type Monitor struct {
	ConfigPath string
	Logger     interface {
		Info(msg string, args ...any)
		Warn(msg string, args ...any)
		Error(msg string, args ...any)
	}

	cfg       *config.Config
	deps      Deps
	scheduler Scheduler
	readiness *Readiness
	collector *metrics.Collector
	web       *WebBackendHolder

	parent context.Context

	mu        sync.Mutex
	workers   []*Worker
	watches   []*Watch
	genCancel context.CancelFunc
	genWG     sync.WaitGroup
	booted    bool
}

// NewMonitor wires a monitor from the initial validated config and shared deps.
func NewMonitor(cfg *config.Config, deps Deps, scheduler Scheduler, readiness *Readiness, collector *metrics.Collector, web *WebBackendHolder) *Monitor {
	return &Monitor{
		cfg: cfg, deps: deps, scheduler: scheduler, readiness: readiness,
		collector: collector, web: web,
	}
}

// Init records the first worker/watch set built at daemon start.
func (m *Monitor) Init(workers []*Worker, watches []*Watch) {
	m.workers = workers
	m.watches = watches
}

// Run starts the first generation and blocks until ctx is cancelled, then stops
// workers and marks readiness shutting down.
func (m *Monitor) Run(ctx context.Context) {
	m.parent = ctx
	m.mu.Lock()
	m.startGenerationLocked(ctx, true)
	m.mu.Unlock()
	<-ctx.Done()
	m.mu.Lock()
	m.stopGenerationLocked(true)
	m.mu.Unlock()
}

// Reload loads config from disk, validates it, and swaps in a new worker
// generation. Invalid config or an empty fleet is rejected and the current
// generation keeps running.
func (m *Monitor) Reload() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.ConfigPath == "" {
		m.emitReloadError("config path is not set")
		return
	}
	if m.parent == nil {
		m.emitReloadError("monitor is not running")
		return
	}

	newCfg, err := config.Load(m.ConfigPath)
	if err != nil {
		m.emitReloadError(fmt.Sprintf("load config: %v", err))
		return
	}
	if issues := config.Validate(newCfg); len(issues) > 0 {
		m.emitReloadError(fmt.Sprintf("config invalid: %s", issues[0].Msg))
		return
	}

	oldWorkers := m.workers
	saved := captureWorkerState(oldWorkers)
	prevCfg := m.cfg
	prevDeps := m.deps

	m.applyConfig(newCfg)

	workers, warnings := BuildWorkers(newCfg, m.deps, m.collector)
	watches, watchWarnings := BuildWatches(newCfg, m.deps, m.deps.Interval)
	if len(workers) == 0 && len(watches) == 0 && !HasConfiguredTargets(newCfg) {
		m.cfg = prevCfg
		m.deps = prevDeps
		m.emitReloadError("no services or watches configured")
		return
	}

	applyWorkerState(workers, saved)
	resetRemovedServiceMetrics(m.collector, oldWorkers, workers)

	m.stopGenerationLocked(false)

	m.cfg = newCfg
	m.workers = workers
	m.watches = watches
	if m.readiness != nil {
		m.readiness.UpdateCounts(len(workers), len(watches))
	}
	if m.web != nil {
		if warns := m.web.Reload(newCfg, m.deps); len(warns) > 0 {
			for _, w := range warns {
				m.Logger.Warn("reload web backend", "warning", w)
			}
		}
	}
	for _, w := range append(warnings, watchWarnings...) {
		m.Logger.Warn("reload build", "warning", w)
	}

	m.startGenerationLocked(m.parent, false)

	if m.deps.Emit != nil {
		m.deps.Emit(Event{
			Kind:    "reload",
			Message: fmt.Sprintf("config reloaded (%d services, %d watches)", len(workers), len(watches)),
		})
	}
	m.Logger.Info("config reloaded", "services", len(workers), "watches", len(watches))
}

func (m *Monitor) applyConfig(cfg *config.Config) {
	m.cfg = cfg
	m.deps.Runtime = cfg.Global.RuntimeDir()
	m.deps.Interval = EngineInterval(cfg, 30*time.Second)
	m.deps.DefaultTimeout = EngineDuration(cfg, "default_timeout", 10*time.Second)
	m.deps.OperationTimeout = EngineDuration(cfg, "operation_timeout", 90*time.Second)
	m.deps.MaxParallel = EngineInt(cfg, "max_parallel_checks", 8)
	m.deps.SystemFreshness = m.deps.Interval / 2
	if m.collector != nil && m.deps.SystemFreshness > 0 {
		m.collector.SystemFreshness = m.deps.SystemFreshness
	}
	notifiers, warns := notify.Build(notifiersRaw(cfg))
	m.deps.Notifiers = notifiers
	for _, w := range warns {
		m.Logger.Warn("reload notifiers", "warning", w)
	}
}

func (m *Monitor) startGenerationLocked(ctx context.Context, firstBoot bool) {
	genCtx, cancel := context.WithCancel(ctx)
	m.genCancel = cancel

	sched := m.scheduler
	if m.booted || !firstBoot {
		sched.StartupDelay = 0
	} else {
		m.booted = true
	}

	m.genWG.Add(1)
	go func() {
		defer m.genWG.Done()
		sched.Run(genCtx, m.workers, m.watches, m.deps.OpGate, m.readiness, false)
	}()
}

func (m *Monitor) stopGenerationLocked(final bool) {
	if m.genCancel == nil {
		return
	}
	m.genCancel()
	m.genWG.Wait()
	m.genCancel = nil
	if final && m.readiness != nil {
		m.readiness.MarkShuttingDown()
	}
}

func (m *Monitor) emitReloadError(msg string) {
	m.Logger.Warn("config reload rejected", "error", msg)
	if m.deps.Emit != nil {
		m.deps.Emit(Event{Kind: "error", Action: "reload", Message: msg})
	}
}