package app

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"sermo/internal/config"
	"sermo/internal/emission"
	"sermo/internal/metrics"
	"sermo/internal/notify"
	"sermo/internal/process"
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

const (
	monitorLogFieldError        = "error"
	monitorLogFieldServices     = "services"
	monitorLogFieldWarning      = "warning"
	monitorLogFieldWatches      = "watches"
	validationIssuePreviewLimit = 5
)

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
func (m *Monitor) Reload(ctx context.Context) {
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

	newCfg, err := config.Load(m.ConfigPath, config.WithLoadContext(ctx)) //nolint:contextcheck // WithLoadContext binds ctx for service-unit discovery
	if err != nil {
		m.emitReloadError(fmt.Sprintf("load config: %v", err))
		return
	}
	if issues := config.Validate(newCfg); len(issues) > 0 {
		m.emitReloadError("config invalid: " + formatValidationIssues(issues))
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
	if m.deps.ArtifactSamples == nil {
		m.deps.ArtifactSamples = NewArtifactSamples()
	}

	reloadDeps := m.deps
	workers, svcWatches, warnings := BuildWorkers(ctx, newCfg, reloadDeps, m.collector)
	watches, watchWarnings := BuildWatches(newCfg, m.deps, m.deps.Interval)
	hostWatches := len(watches)
	watches = append(watches, svcWatches...)
	watches = append(watches, BuildArtifactWatches(ctx, newCfg, reloadDeps)...)
	if len(workers) == 0 && hostWatches == 0 && !HasConfiguredTargets(newCfg) {
		// Rollback: restore previous generation and restart it (we stopped above).
		m.cfg = prevCfg
		m.deps = prevDeps
		m.workers = oldWorkers
		m.watches = oldWatches
		m.emitReloadError("no services or watches configured")
		m.startGenerationLocked(ctx, false)
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
		if warns := m.web.Reload(ctx, newCfg, reloadDeps); len(warns) > 0 {
			for _, w := range warns {
				m.Logger.Warn("reload web backend", monitorLogFieldWarning, w)
			}
		}
	}
	for _, w := range append(warnings, watchWarnings...) {
		m.Logger.Warn("reload build", monitorLogFieldWarning, w)
	}

	m.startGenerationLocked(ctx, false)

	if m.deps.Emit != nil {
		m.deps.Emit(Event{
			Kind:    eventKindReload,
			Message: fmt.Sprintf("config reloaded (%d services, %d watches)", len(workers), len(watches)),
		})
	}
	m.Logger.Info("config reloaded", monitorLogFieldServices, len(workers), monitorLogFieldWatches, len(watches))
}

func (m *Monitor) applyConfig(cfg *config.Config) {
	m.cfg = cfg
	m.deps.Runtime = cfg.Global.RuntimeDir()
	m.deps.Interval = config.EngineInterval(cfg, config.DefaultEngineInterval)
	m.deps.DefaultTimeout = EngineDuration(cfg, config.EngineKeyDefaultTimeout, DefaultEngineCheckTimeout)
	m.deps.OperationTimeout = EngineDuration(cfg, config.EngineKeyOperationTimeout, DefaultEngineOperationTimeout)
	m.deps.MaxParallel = EngineInt(cfg, config.EngineKeyMaxParallelChecks, DefaultEngineMaxParallelChecks)
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
	m.deps.GlobalEmission = emission.Merge(cfg.Global.Raw[emission.Section], emission.Default())
	for _, w := range warns {
		m.Logger.Warn("reload notifiers", monitorLogFieldWarning, w)
	}
	if m.deps.DiagnosticLog != nil {
		m.deps.DiagnosticLog.UpdateConfig(cfg)
		go m.deps.DiagnosticLog.Export()
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
					preserved = append(preserved, SettlingServiceKey(w.Service))
				}
			}
			for _, wt := range m.watches {
				if wt != nil && wt.settled {
					preserved = append(preserved, settlingKeyForWatch(wt))
				}
			}
			m.deps.Settling.MarkObservedBulk(preserved)
		}
	}

	m.genWG.Go(func() {
		sched.Run(genCtx, m.workers, m.watches, m.deps.OpGate, m.readiness, false, firstGen)
	})
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
	m.Logger.Warn("config reload rejected", monitorLogFieldError, msg)
	if m.deps.Emit != nil {
		m.deps.Emit(Event{Kind: eventKindError, Action: eventActionReload, Message: msg})
	}
}

// formatValidationIssues joins the first few validation findings for reload errors.
func monitorTargetNames(workers []*Worker, watches []*Watch) []string {
	names := make([]string, 0, len(workers)+len(watches))
	for _, w := range workers {
		if monitorTargetActive(w) {
			names = append(names, SettlingServiceKey(w.Service))
		}
	}
	for _, wt := range watches {
		if watchTargetActive(wt) {
			names = append(names, settlingKeyForWatch(wt))
		}
	}
	return names
}

func formatValidationIssues(issues []config.Issue) string {
	msgs := make([]string, 0, min(len(issues), validationIssuePreviewLimit))
	for i, issue := range issues {
		if i >= validationIssuePreviewLimit {
			msgs = append(msgs, fmt.Sprintf("... and %d more", len(issues)-validationIssuePreviewLimit))
			break
		}
		msgs = append(msgs, issue.Msg)
	}
	return strings.Join(msgs, "; ")
}
