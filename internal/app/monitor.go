package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"sermo/internal/config"
	"sermo/internal/metrics"
	"sermo/internal/notify"
	"sermo/internal/process"
)

// Monitor runs service workers and host watches in reloadable generations.
// SIGHUP triggers Reload, which validates new config, preserves per-service
// runtime state, stops the current generation gracefully, and starts a new one.
type Monitor struct {
	ConfigPath string
	// CatalogDirs mirrors `sermod --catalog`: when set, a reload overrides the
	// config's paths.catalog with these directories so reload behaves like the
	// initial load.
	CatalogDirs []string
	Logger      interface {
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

	var loadOpts []config.Option
	if len(m.CatalogDirs) > 0 {
		loadOpts = append(loadOpts, config.WithCatalogDirs(m.CatalogDirs...))
	}
	newCfg, err := config.Load(m.ConfigPath, loadOpts...)
	if err != nil {
		m.emitReloadError(fmt.Sprintf("load config: %v", err))
		return
	}
	if issues := config.Validate(newCfg); len(issues) > 0 {
		m.emitReloadError(fmt.Sprintf("config invalid: %s", formatValidationIssues(issues)))
		return
	}

	oldWorkers := m.workers
	oldWatches := m.watches
	// Stop the current generation before capturing state. This ensures no
	// scheduler goroutines are calling RunCycle (which mutates cycle, State,
	// windows and libBaseline) on the old workers while we snapshot them.
	// See reload state preservation.
	m.stopGenerationLocked(false)

	savedWorkers := captureWorkerState(oldWorkers)
	savedWatches := captureWatchState(oldWatches)
	prevCfg := m.cfg
	prevDeps := m.deps

	m.applyConfig(newCfg)

	workers, warnings := BuildWorkers(newCfg, m.deps, m.collector)
	watches, watchWarnings := BuildWatches(newCfg, m.deps, m.deps.Interval)
	hostWatches := len(watches)
	watches = append(watches, BuildAppWatches(newCfg, m.deps)...)
	if len(workers) == 0 && hostWatches == 0 && !HasConfiguredTargets(newCfg) {
		// Rollback: restore previous generation and restart it (we stopped above).
		m.cfg = prevCfg
		m.deps = prevDeps
		m.workers = oldWorkers
		m.watches = oldWatches
		m.emitReloadError("no services or watches configured")
		m.startGenerationLocked(m.parent, false)
		return
	}

	applyWorkerState(workers, savedWorkers)
	applyWatchState(watches, savedWatches)
	resetRemovedServiceMetrics(m.collector, oldWorkers, workers)

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
	m.deps.Interval = config.EngineInterval(cfg, 30*time.Second)
	m.deps.DefaultTimeout = EngineDuration(cfg, "default_timeout", 10*time.Second)
	m.deps.OperationTimeout = EngineDuration(cfg, "operation_timeout", 90*time.Second)
	m.deps.MaxParallel = EngineInt(cfg, "max_parallel_checks", 8)
	m.deps.UserLookup = EngineUserLookup(cfg, m.deps.ExecxRunner)
	m.deps.SystemFreshness = m.deps.Interval / 2
	if m.collector != nil && m.deps.SystemFreshness > 0 {
		m.collector.SystemFreshness = m.deps.SystemFreshness
	}
	// Recreate the shared /proc reader so reloads can change user/group lookup
	// policy as well as the freshness window.
	m.deps.ProcReader = process.NewCachingReader(process.OSReader{LookupUserName: m.deps.UserLookup.Username}, m.deps.SystemFreshness)
	notifiers, warns := notify.Build(cfg.Notifiers(), notify.WithTemplateDir(cfg.Global.TemplateDir()))
	m.deps.Notifiers = notifiers
	m.deps.GlobalNotify = config.NotifyDefault(cfg.Global.Raw)
	for _, w := range warns {
		m.Logger.Warn("reload notifiers", "warning", w)
	}
}

func (m *Monitor) startGenerationLocked(ctx context.Context, firstBoot bool) {
	genCtx, cancel := context.WithCancel(ctx)
	m.genCancel = cancel

	sched := m.scheduler
	// firstGen is the very first boot: it keeps the StartupDelay and gates
	// readiness on first cycles. Reloads skip both (the daemon is already up).
	firstGen := firstBoot && !m.booted
	if firstGen {
		m.booted = true
	} else {
		sched.StartupDelay = 0
	}

	if m.deps.Settling != nil {
		names := monitorTargetNames(m.workers, m.watches)
		m.deps.Settling.Reset(names)
		if !firstGen {
			var preserved []string
			for _, w := range m.workers {
				if w != nil && w.cycle > 0 {
					preserved = append(preserved, w.Service)
				}
			}
			for _, wt := range m.watches {
				if wt != nil && wt.settled {
					preserved = append(preserved, wt.Name)
				}
			}
			m.deps.Settling.MarkObservedBulk(preserved)
		}
	}

	m.genWG.Add(1)
	go func() {
		defer m.genWG.Done()
		sched.Run(genCtx, m.workers, m.watches, m.deps.OpGate, m.readiness, false, firstGen)
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

// formatValidationIssues joins the first few validation findings for reload errors.
func monitorTargetNames(workers []*Worker, watches []*Watch) []string {
	names := make([]string, 0, len(workers)+len(watches))
	for _, w := range workers {
		if monitorTargetActive(w) {
			names = append(names, w.Service)
		}
	}
	for _, wt := range watches {
		if watchTargetActive(wt) {
			names = append(names, wt.Name)
		}
	}
	return names
}

func formatValidationIssues(issues []config.Issue) string {
	const limit = 5
	msgs := make([]string, 0, min(len(issues), limit))
	for i, issue := range issues {
		if i >= limit {
			msgs = append(msgs, fmt.Sprintf("... and %d more", len(issues)-limit))
			break
		}
		msgs = append(msgs, issue.Msg)
	}
	return strings.Join(msgs, "; ")
}
