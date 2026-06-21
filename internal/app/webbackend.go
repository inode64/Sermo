package app

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"sermo/internal/appinspect"
	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/control"
	"sermo/internal/diag"
	"sermo/internal/execx"
	"sermo/internal/locks"
	"sermo/internal/metrics"
	"sermo/internal/notify"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
	"sermo/internal/web"
)

const (
	applicationsCacheTTL = 30 * time.Second
	// serviceStatusCacheTTL bounds how often the web list re-queries systemd/OpenRC.
	serviceStatusCacheTTL = 10 * time.Second
	// slaTimelineCacheTTL caches SLA timeline strips for detail/expansion views.
	slaTimelineCacheTTL = 45 * time.Second
)

// webEntry is one service's web-backend record.
type webEntry struct {
	displayName       string
	category          string
	unit              string
	backend           string
	interval          time.Duration // resolved per-service cycle cadence (own interval or engine default)
	policyCooldown    time.Duration
	engine            operation.Engine
	status            func(context.Context) (servicemgr.Status, error)
	checkNames        []string          // sorted
	checkTypes        map[string]string // check name -> type
	discoverer        process.Discoverer
	selectors         []process.Selector
	processWarnings   []string
	noResidentProcess bool
	alsoApply         []string
	disabled          bool // true when the service had `enabled: false` (still listed for visibility)

	statusMu     sync.Mutex
	cachedStatus string
	statusAt     time.Time
}

// webWatch is a configured host watch for UI visibility (services may be 0).
type webWatch struct {
	name          string
	displayName   string
	checkType     string
	interval      time.Duration
	disabled      bool
	monitorMode   string
	fireOnFail    bool
	hasHook       bool
	hookCommand   []string
	notifiers     []string
	dryRun        bool
	notifierCount int
	check         map[string]any
	metrics       map[string]any
	expand        *ExpandSpec
}

// webNotifier is a configured notification target (used by watches).
type webNotifier struct {
	name    string
	typ     string
	enabled bool
	summary string
}

type diagnosticCleaner interface {
	PruneUnconfiguredControlStates(configured []string) (state.PruneUnconfiguredControlStatesResult, error)
}

type stateMaintainer interface {
	PruneHistory(before time.Time) (state.PruneHistoryResult, error)
	Compact(ctx context.Context) error
}

// WebBackend implements web.Backend over the daemon's services: status from the
// backend, monitoring state and SLA from the store, the latest check results from
// the shared snapshots, and start/stop/restart/reload/resume through the same safe operation
// engine the workers use.
type WebBackend struct {
	order            []string
	entries          map[string]*webEntry
	watchOrder       []string
	watches          map[string]*webWatch
	notifierOrder    []string
	notifiers        map[string]*webNotifier
	store            MonitorStore
	snapshots        *Snapshots
	sla              SLAReader
	events           *EventLog
	remediation      *RemediationRegistry
	ruleWindows      *RuleWindowRegistry
	cfg              *config.Config
	diagStore        diag.Store
	diagCleaner      diagnosticCleaner
	host             diag.Host
	measure          MeasurementReader
	collector        *metrics.Collector
	daemonMetrics    *daemonMetricSampler
	serviceMetrics   *ServiceMetricSampler
	live             *LiveMetrics
	storageUsage     checks.StorageUsageFunc
	mountSampler     checks.MountSamplerFunc
	netSampler       checks.NetSamplerFunc
	pingSampler      checks.PingSamplerFunc
	oomSampler       checks.OomSamplerFunc
	fdsSampler       checks.FdsSamplerFunc
	pidsSampler      checks.PidsSamplerFunc
	pressureSampler  checks.PressureSamplerFunc
	conntrackSampler checks.ConntrackSamplerFunc
	entropySampler   checks.EntropySamplerFunc
	zombieSampler    checks.ZombieSamplerFunc
	procSampler      ProcSampler
	diskIOSampler    checks.DiskIOSamplerFunc
	sensorSampler    checks.SensorSamplerFunc
	raidSampler      checks.RaidSamplerFunc
	edacSampler      checks.EdacSamplerFunc
	routeSampler     checks.RouteSamplerFunc
	firewallSampler  checks.FirewallRulesSamplerFunc
	execRunner       execx.Runner
	expander         VolumeExpander
	userLookup       *process.UserLookup
	emit             func(Event)
	opGate           *OpGate
	defaultTimeout   time.Duration
	operationTimeout time.Duration
	now              func() time.Time

	diskIOMu    sync.Mutex
	diskIOState map[string]webDiskIOState

	applicationsMu    sync.Mutex
	applicationsAt    time.Time
	applicationsCache []web.Application
	applicationsList  func(context.Context) []web.Application

	slaCacheMu sync.Mutex
	slaCache   map[slaCacheKey]cachedSLATimelines
}

type slaCacheKey struct {
	service string
	check   string // empty for service-level SLA
}

type cachedSLATimelines struct {
	windows []web.SLAWindow
	at      time.Time
}

type webDiskIOState struct {
	primed bool
	at     time.Time
	sample checks.DiskIOSample
}

// NewWebBackend resolves services for the web UI. All services present in the
// loaded configuration are included in the listing (even those with `enabled: false`)
// so that the dashboard can show the full fleet and let operators see what can be
// activated (by editing the service file and reloading). Only non-disabled services
// get a full runtime engine, checks, and operation support.
func NewWebBackend(cfg *config.Config, deps Deps) (*WebBackend, []string) {
	if deps.UserLookup == nil {
		deps.UserLookup = EngineUserLookup(cfg, deps.ExecxRunner)
	}
	wb := &WebBackend{
		entries:          map[string]*webEntry{},
		watches:          map[string]*webWatch{},
		notifiers:        map[string]*webNotifier{},
		store:            deps.Monitor,
		snapshots:        deps.Snapshots,
		events:           deps.Events,
		remediation:      deps.Remediation,
		ruleWindows:      deps.RuleWindows,
		cfg:              cfg,
		host:             diag.OSHost{},
		collector:        deps.Collector,
		daemonMetrics:    newDaemonMetricSampler(deps.Collector, deps.Now, deps.DaemonMetrics),
		serviceMetrics:   deps.ServiceMetrics,
		live:             deps.Live,
		storageUsage:     deps.StorageUsage,
		mountSampler:     deps.MountSampler,
		netSampler:       deps.NetSampler,
		pingSampler:      deps.PingSampler,
		oomSampler:       deps.OomSampler,
		fdsSampler:       deps.FdsSampler,
		pidsSampler:      deps.PidsSampler,
		pressureSampler:  deps.PressureSampler,
		conntrackSampler: deps.ConntrackSampler,
		entropySampler:   deps.EntropySampler,
		zombieSampler:    deps.ZombieSampler,
		procSampler:      deps.ProcSampler,
		diskIOSampler:    deps.DiskIOSampler,
		sensorSampler:    deps.SensorSampler,
		raidSampler:      deps.RaidSampler,
		edacSampler:      deps.EdacSampler,
		routeSampler:     deps.RouteSampler,
		firewallSampler:  deps.FirewallRulesSampler,
		execRunner:       deps.ExecxRunner,
		expander:         configuredVolumeExpander(deps),
		userLookup:       deps.UserLookup,
		emit:             deps.Emit,
		opGate:           deps.OpGate,
		defaultTimeout:   deps.DefaultTimeout,
		operationTimeout: deps.OperationTimeout,
		now:              deps.Now,
		slaCache:         map[slaCacheKey]cachedSLATimelines{},
	}
	if wb.serviceMetrics == nil {
		wb.serviceMetrics = NewServiceMetricSampler()
	}
	wb.sla, _ = deps.SLA.(SLAReader)
	wb.measure, _ = deps.SLA.(MeasurementReader)
	wb.diagStore, _ = deps.Monitor.(diag.Store)
	wb.diagCleaner, _ = deps.Monitor.(diagnosticCleaner)
	var warnings []string
	resolver := servicemgr.NewUnitResolver()
	resolver.Manager = deps.Manager

	for _, name := range cfg.SortedServiceNames() {
		doc := cfg.Services[name]
		if doc == nil {
			continue
		}
		resolved, errs := cfg.Resolve(name)
		if len(errs) > 0 {
			warnings = append(warnings, "skip service "+name+": "+errs[0])
			continue
		}
		disabled := cfgval.Disabled(doc.Body)
		target, warn := control.ResolveWithFallback(context.Background(), name, resolved.Tree, deps.Backend, deps.Manager, resolver)
		if warn != "" {
			warnings = append(warnings, "service "+name+": "+warn)
		}
		if target.Unit == "" {
			continue
		}
		iv := cfgval.Duration(resolved.Tree["interval"])
		if iv <= 0 {
			iv = config.EngineInterval(cfg, 30*time.Second)
		}
		entry := &webEntry{
			displayName:       config.DisplayName(resolved.Tree, name),
			category:          config.CategoryLabel(resolved.Tree, config.CategoryService),
			unit:              target.Unit,
			backend:           string(target.Backend),
			interval:          iv,
			policyCooldown:    rules.ParsePolicy(resolved.Tree).Cooldown,
			noResidentProcess: noResidentProcess(resolved.Tree),
			alsoApply:         config.CascadeTargets(resolved.Tree),
		}
		if disabled {
			entry.disabled = true
		} else {
			serviceDeps := deps
			serviceDeps.Backend = target.Backend
			serviceDeps.Manager = target.Manager
			serviceDeps.BackendPIDs = target.BackendPIDs
			engine, checkDeps, discoverer := serviceRuntime(name, target.Unit, resolved.Tree, serviceDeps, map[string]string{}, operationEventEmitter(deps.Emit))
			selectors, processWarnings := serviceProcessSelectors(context.Background(), resolved.Tree, serviceDeps, target.Unit)
			names, types := checkCatalog(resolved.Tree)
			entry.engine = engine
			entry.status = checkDeps.Status
			entry.checkNames = names
			entry.checkTypes = types
			entry.discoverer = discoverer
			entry.selectors = selectors
			entry.processWarnings = processWarnings
		}
		wb.entries[name] = entry
		wb.order = append(wb.order, name)
	}

	// Also surface host watches in the web UI (including disabled ones). This is
	// important when services=0 but watches=N (the main dashboard would otherwise
	// be empty). We read the raw global watches section (same source BuildWatches
	// uses) so listing is independent of whether the watch runner is active.
	if raw, _ := cfg.ResolveWatches(); len(raw) > 0 {
		for _, name := range slices.Sorted(maps.Keys(raw)) {
			entry, _ := raw[name].(map[string]any)
			disabled := cfgval.Disabled(entry)
			ctype := ""
			if ce, ok := entry["check"].(map[string]any); ok {
				ctype = cfgval.AsString(ce["type"])
			}
			fireOnFail := checks.IsHealthType(ctype)
			iv := cfgval.Duration(entry["interval"])
			if iv <= 0 {
				iv = 30 * time.Second
			}
			hasHook := false
			var hookCommand []string
			var notifierNames []string
			var expand *ExpandSpec
			dryRun := false
			if then, ok := entry["then"].(map[string]any); ok {
				if h, ok := then["hook"].(map[string]any); ok && len(h) > 0 {
					if cmd := h["command"]; cmd != nil {
						hookCommand = cfgval.StringArray(cmd)
						hasHook = len(hookCommand) > 0
					}
				}
				notifierNames = effectiveNotify(cfgval.StringList(then["notify"]), deps.GlobalNotify)
				dryRun = cfgval.Bool(then["dry_run"])
				if parsed, err := parseExpand(then, ctype); err != nil {
					warnings = append(warnings, "watch "+name+": "+err.Error())
				} else {
					expand = parsed
				}
			}
			ww := &webWatch{
				name:          name,
				displayName:   config.DisplayName(entry, name),
				checkType:     ctype,
				interval:      iv,
				disabled:      disabled,
				monitorMode:   config.MonitorMode(entry),
				fireOnFail:    fireOnFail,
				hasHook:       hasHook,
				hookCommand:   hookCommand,
				notifiers:     notifierNames,
				dryRun:        dryRun,
				notifierCount: len(notifierNames),
				check:         checkMap(entry),
				metrics:       metricsMap(entry),
				expand:        expand,
			}
			wb.watches[name] = ww
			wb.watchOrder = append(wb.watchOrder, name)
		}
	}

	// Surface configured notifiers (useful to know what watches can notify to).
	if raw := cfg.Notifiers(); len(raw) > 0 {
		for _, name := range slices.Sorted(maps.Keys(raw)) {
			entry, _ := raw[name].(map[string]any)
			typ := cfgval.AsString(entry["type"])
			wn := &webNotifier{
				name:    name,
				typ:     typ,
				enabled: notify.Enabled(entry),
				summary: notify.ConfigSummary(typ, entry),
			}
			wb.notifiers[name] = wn
			wb.notifierOrder = append(wb.notifierOrder, name)
		}
	}

	return wb, warnings
}

// checkCatalog returns a service's check names (sorted) and their types, from the
// resolved `checks` section.
func checkCatalog(tree map[string]any) ([]string, map[string]string) {
	section, ok := tree["checks"].(map[string]any)
	if !ok {
		return nil, nil
	}
	types := make(map[string]string, len(section))
	names := make([]string, 0, len(section))
	for name, raw := range section {
		typ := ""
		if m, ok := raw.(map[string]any); ok {
			typ, _ = m["type"].(string)
		}
		types[name] = typ
		names = append(names, name)
	}
	sort.Strings(names)
	return names, types
}

func (b *WebBackend) view(ctx context.Context, name string, e *webEntry) web.Service {
	return b.viewWithEvent(ctx, name, e, b.lastServiceEvent(name))
}

func (b *WebBackend) viewWithEvent(ctx context.Context, name string, e *webEntry, lastEvent *web.Event) web.Service {
	return b.viewWithRuntime(ctx, name, e, lastEvent, nil, false)
}

func (b *WebBackend) viewWithRuntime(ctx context.Context, name string, e *webEntry, lastEvent *web.Event, activeLocks []string, activeLocksReady bool) web.Service {
	svc := web.Service{
		Name:        name,
		DisplayName: e.displayName,
		Category:    e.category,
		Backend:     e.backend,
		Unit:        e.unit,
		Enabled:     !e.disabled,
		Monitored:   true, // no recorded state defaults to monitored
	}
	if e.interval > 0 {
		svc.Interval = formatInterval(e.interval)
	}
	if e.policyCooldown > 0 {
		svc.PolicyCooldown = formatInterval(e.policyCooldown)
	}
	svc.LastEvent = lastEvent
	if e.disabled {
		svc.Status = "disabled"
		svc.State = ServiceState(false, false, svc.Status, "")
		svc.Monitored = false
		svc.CheckHealth = ""
		svc.RemediationState = "disabled"
		return svc
	}
	svc.Status = e.backendStatus(ctx, b.webNow())
	if active, source, changed, ok := b.monitorView(name); ok {
		svc.Monitored, svc.MonitorSource, svc.MonitorChangedAt = active, source, changed
	}
	failing, health := checkHealthSummary(b.snapshots.Get(name), e.checkNames, svc.Monitored)
	svc.CheckHealth = health
	if failing > 0 {
		svc.ChecksFailing = failing
	}
	if !activeLocksReady {
		activeLocks = activeLockNames(b.cfg, name)
	}
	if len(activeLocks) > 0 {
		svc.ActiveLocks = activeLocks
	}
	b.decorateRemediation(name, &svc)
	svc.State = ServiceState(svc.Enabled, svc.Monitored, svc.Status, svc.CheckHealth)
	if len(e.alsoApply) > 0 {
		svc.AlsoApply = slices.Clone(e.alsoApply)
	}
	b.decorateServiceRuntime(name, e, &svc)
	return svc
}

func (b *WebBackend) decorateRemediation(name string, svc *web.Service) {
	if svc == nil {
		return
	}
	if !svc.Monitored {
		svc.RemediationState = "paused"
		return
	}
	if b.remediation == nil {
		svc.RemediationState = "pending"
		return
	}
	rep, ok := b.remediation.Get(name)
	if !ok {
		svc.RemediationState = "pending"
		return
	}
	if rep.Allowed {
		svc.RemediationState = "eligible"
	} else if rep.Reason != "" {
		svc.RemediationState = rep.Reason
	} else {
		svc.RemediationState = "blocked"
	}
	if !rep.NextEligibleAt.IsZero() {
		svc.NextEligibleAt = rep.NextEligibleAt.UTC().Format(time.RFC3339)
	}
}

func locksScanner(cfg *config.Config) locks.Scanner {
	return locks.NewScanner(filepath.Join(cfg.Global.RuntimeDir(), "locks"))
}

func serviceLocksReport(cfg *config.Config, service string) (locks.Report, error) {
	if cfg == nil {
		return locks.Report{Service: service}, nil
	}
	return locksScanner(cfg).Scan(service)
}

// activeLockNames returns the names of named runtime locks currently blocking
// actions for service (parity with `sermoctl locks SERVICE`, active only).
func activeLockNames(cfg *config.Config, service string) []string {
	report, err := serviceLocksReport(cfg, service)
	if err != nil {
		return nil
	}
	return activeLockNamesFromReport(report)
}

func activeLockNamesFromReport(report locks.Report) []string {
	var names []string
	for _, lk := range report.Locks {
		if lk.State != locks.StateActive {
			continue
		}
		n := lk.Name
		if n == "" {
			n = "(default)"
		}
		names = append(names, n)
	}
	return names
}

func (b *WebBackend) activeLockNamesByService() map[string][]string {
	reports := b.lockReportsByService()
	if len(reports) == 0 {
		return nil
	}
	out := make(map[string][]string, len(reports))
	for name, report := range reports {
		out[name] = activeLockNamesFromReport(report)
	}
	return out
}

func (b *WebBackend) lockReportsByService() map[string]locks.Report {
	if b.cfg == nil || len(b.order) == 0 {
		return nil
	}
	names := make([]string, 0, len(b.order))
	for _, name := range b.order {
		e := b.entries[name]
		if e == nil || e.disabled {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil
	}
	reports, err := locksScanner(b.cfg).ScanServices(names)
	if err != nil {
		return nil
	}
	return reports
}

// checkHealthSummary reports required-check health for the service list. It uses
// the same rule as SLA availability: a required, non-skipped check with OK=false
// counts as failing; optional failures are ignored. Paused services are "paused";
// services with no observed checks yet are "unknown".
func checkHealthSummary(snap map[string]CheckSnapshot, checkNames []string, monitored bool) (failing int, health string) {
	if !monitored {
		return 0, "paused"
	}
	if len(checkNames) == 0 {
		return 0, ""
	}
	if snap == nil {
		return 0, "unknown"
	}
	observed := false
	for _, name := range checkNames {
		cs, seen := snap[name]
		if !seen {
			continue
		}
		observed = true
		if cs.Skipped || cs.Optional || cs.healthy() {
			continue
		}
		failing++
	}
	if !observed {
		return 0, "unknown"
	}
	if failing > 0 {
		return failing, "failing"
	}
	return 0, "ok"
}

// Services returns the web view of every configured service.
func (b *WebBackend) Services(ctx context.Context) []web.Service {
	out := make([]web.Service, 0, len(b.order))
	lastEvents := b.lastServiceEvents()
	activeLocks := b.activeLockNamesByService()
	for _, name := range b.order {
		out = append(out, b.viewWithRuntime(ctx, name, b.entries[name], lastEvents[name], activeLocks[name], true))
	}
	return out
}

// Watches returns the configured host watches, including disabled ones.
func (b *WebBackend) Watches(ctx context.Context) []web.Watch {
	if len(b.watchOrder) == 0 {
		return nil
	}
	out := make([]web.Watch, 0, len(b.watchOrder))
	lastActivities := b.lastWatchActivities()
	system := b.watchSystemSnapshot()
	for _, name := range b.watchOrder {
		w := b.watches[name]
		if w == nil {
			continue
		}
		iv := formatInterval(w.interval)
		var storage *web.StorageWatchInfo
		// A disabled watch is config, not a live concern: skip the statfs/mount
		// sampling so the UI never surfaces sample errors (or pays the probe)
		// for something the operator has switched off.
		if isStorageCheckType(w.checkType) && !w.disabled {
			storage = storageWatchInfo(w, b)
		}
		var swap *web.SwapWatchInfo
		if w.checkType == "swap" && !w.disabled {
			swap = swapWatchInfo(system)
		}
		// memory/load/fds/pids carry a natural capacity, so render them with the
		// same progress bar as swap. Skip disabled watches (config, not a live
		// concern) so the UI never probes /proc for something switched off.
		var meter *web.WatchMeter
		var readings []web.WatchReading
		liveSummary := ""
		if !w.disabled {
			meter, readings, liveSummary = b.watchLiveView(w, system)
		}
		monitorMode := w.monitorMode
		if monitorMode == "" {
			monitorMode = config.MonitorEnabled
		}
		ww := web.Watch{
			Name:          w.name,
			DisplayName:   w.displayName,
			CheckType:     w.checkType,
			Summary:       watchSummary(w, storage, liveSummary),
			Interval:      iv,
			Enabled:       !w.disabled,
			Monitor:       monitorMode,
			Monitored:     !w.disabled && monitorMode != config.MonitorDisabled,
			FireOnFail:    w.fireOnFail,
			HasHook:       w.hasHook,
			HookCommand:   slices.Clone(w.hookCommand),
			Notifiers:     slices.Clone(w.notifiers),
			NotifierCount: w.notifierCount,
			DryRun:        w.dryRun,
			Conditions:    watchConditions(w.check, w.metrics),
			Storage:       storage,
			Swap:          swap,
			Meter:         meter,
			Readings:      readings,
		}
		if w.expand != nil {
			ww.Expand = &web.WatchExpand{ByBytes: w.expand.By}
		}
		if !w.disabled {
			if active, source, changed, ok := b.monitorView(watchMonitorKey(name)); ok {
				ww.Monitored, ww.MonitorSource, ww.MonitorChangedAt = active, source, changed
			}
		}
		// Compute last activity for this watch from the request-local event index.
		if activity, ok := lastActivities[name]; ok {
			ww.LastActivity = activity.At
			ww.LastActivityKind = activity.Kind
		}
		ww.State = WatchState(ww.Enabled, ww.Monitored, watchViewFailed(ww))
		out = append(out, ww)
	}
	return out
}

func (b *WebBackend) watchSystemSnapshot() metrics.Snapshot {
	if b.collector == nil {
		return nil
	}
	return b.collector.SampleSystem()
}

type watchActivity struct {
	At   string
	Kind string
}

func (b *WebBackend) lastServiceEvents() map[string]*web.Event {
	if b.events == nil {
		return nil
	}
	out := map[string]*web.Event{}
	for _, name := range b.order {
		ev, ok := b.events.LastService(name)
		if !ok {
			continue
		}
		webEv := loggedEventToWeb(ev)
		out[name] = &webEv
	}
	return out
}

func (b *WebBackend) lastWatchActivities() map[string]watchActivity {
	if b.events == nil {
		return nil
	}
	out := map[string]watchActivity{}
	for _, name := range b.watchOrder {
		ev, ok := b.events.LastWatchActivity(name)
		if !ok {
			continue
		}
		out[name] = watchActivity{
			At:   ev.Time.Format(time.RFC3339),
			Kind: ev.Kind,
		}
	}
	return out
}

// backendStatus returns the init-system status for a service, reusing a short TTL
// cache so the service list does not invoke systemctl/rc-status on every poll.
func (e *webEntry) backendStatus(ctx context.Context, now time.Time) string {
	if e == nil || e.status == nil {
		return "unknown"
	}
	e.statusMu.Lock()
	defer e.statusMu.Unlock()
	if !e.statusAt.IsZero() && now.Sub(e.statusAt) < serviceStatusCacheTTL {
		return e.cachedStatus
	}
	st, err := e.status(ctx)
	if err != nil {
		e.cachedStatus = "error"
	} else {
		e.cachedStatus = string(st)
	}
	e.statusAt = now
	return e.cachedStatus
}

func (e *webEntry) invalidateStatusCache() {
	if e == nil {
		return
	}
	e.statusMu.Lock()
	e.statusAt = time.Time{}
	e.statusMu.Unlock()
}

func watchViewFailed(w web.Watch) bool {
	if WatchActivityFailed(w.LastActivityKind) && watchActivityCurrent(w.LastActivity, w.MonitorChangedAt) {
		return true
	}
	return (w.Storage != nil && (w.Storage.SampleError != "" || w.Storage.MountSampleError != "")) || watchReadingsFailed(w.Readings)
}

func watchActivityCurrent(activity, changed string) bool {
	if activity == "" || changed == "" {
		return true
	}
	activityAt, err := time.Parse(time.RFC3339, activity)
	if err != nil {
		return true
	}
	changedAt, err := time.Parse(time.RFC3339, changed)
	if err != nil {
		return true
	}
	return !activityAt.Before(changedAt)
}

func watchReadingsFailed(readings []web.WatchReading) bool {
	for _, r := range readings {
		if r.Error != "" {
			return true
		}
	}
	return false
}

func isWatchActivityKind(kind string) bool {
	switch kind {
	case "firing", "recovered", "dry-run", "hook", "notify", "hook-failed", "notify-failed", "expand", "expand-skipped", "expand-failed":
		return true
	default:
		return false
	}
}

func checkMap(entry map[string]any) map[string]any {
	check, _ := entry["check"].(map[string]any)
	return check
}

func metricsMap(entry map[string]any) map[string]any {
	metrics, _ := entry["metrics"].(map[string]any)
	return metrics
}

func watchSummary(w *webWatch, storage *web.StorageWatchInfo, liveSummary string) string {
	if w == nil {
		return ""
	}
	if isStorageCheckType(w.checkType) && storage != nil {
		if storage.SampleError != "" {
			return storage.Path + ": " + storage.SampleError
		}
		fs := storage.FileSystem
		if fs == "" {
			fs = "filesystem"
		}
		return fmt.Sprintf("%s: %.1f%% free (%d bytes) on %s", storage.Path, storage.FreePct, storage.FreeBytes, fs)
	}
	if liveSummary != "" {
		return liveSummary
	}
	conds := watchConditions(w.check, w.metrics)
	if len(conds) == 0 {
		return ""
	}
	parts := make([]string, 0, len(conds))
	for _, c := range conds {
		parts = append(parts, watchConditionText(c))
	}
	return strings.Join(parts, ", ")
}

func watchConditionText(c web.WatchCondition) string {
	return strings.Join(slices.DeleteFunc([]string{c.Field, c.Op, c.Value}, func(s string) bool {
		return strings.TrimSpace(s) == ""
	}), " ")
}

func watchConditions(check, metrics map[string]any) []web.WatchCondition {
	if check == nil {
		return nil
	}
	var out []web.WatchCondition
	for _, field := range watchConditionFields(check) {
		m, ok := check[field].(map[string]any)
		if !ok {
			continue
		}
		out = append(out, web.WatchCondition{
			Field: field,
			Op:    cfgval.AsString(m["op"]),
			Value: cfgval.String(m["value"]),
		})
	}
	switch cfgval.AsString(check["type"]) {
	case "autofs":
		if path := cfgval.AsString(check["path"]); path != "" {
			out = append(out, web.WatchCondition{Field: "path", Op: "==", Value: path})
		} else if _, ok := check["count"].(map[string]any); !ok {
			out = append(out, web.WatchCondition{Field: "count", Op: ">=", Value: "1"})
		}
	case "count":
		if path := cfgval.AsString(check["path"]); path != "" {
			out = append(out, web.WatchCondition{Field: "path", Value: path})
		}
		if kind := cfgval.AsString(check["of"]); kind != "" {
			out = append(out, web.WatchCondition{Field: "of", Value: kind})
		}
		if recursive, ok := check["recursive"].(bool); ok {
			out = append(out, web.WatchCondition{Field: "recursive", Op: "==", Value: fmt.Sprintf("%t", recursive)})
		}
		if m, ok := check["count"].(map[string]any); ok {
			out = append(out, web.WatchCondition{Field: "count", Op: cfgval.AsString(m["op"]), Value: cfgval.String(m["value"])})
		} else if op := cfgval.AsString(check["op"]); op != "" {
			out = append(out, web.WatchCondition{Field: "count", Op: op, Value: cfgval.String(check["value"])})
		}
	case "file":
		out = append(out, fileWatchConditions(check)...)
	case "process":
		if value := cfgval.String(check["for"]); value != "" {
			out = append(out, web.WatchCondition{Field: "for", Op: ">=", Value: value})
		}
		if gone, ok := check["gone"].(bool); ok && gone {
			out = append(out, web.WatchCondition{Field: "gone", Op: "==", Value: "true"})
		}
	case "route":
		family := cfgval.AsString(check["family"])
		if family == "" {
			family = "ipv4"
		}
		out = append(out, web.WatchCondition{Field: "family", Op: "==", Value: family})
		if iface := cfgval.AsString(check["interface"]); iface != "" {
			out = append(out, web.WatchCondition{Field: "interface", Op: "==", Value: iface})
		}
	case "firewall_rules":
		backend := cfgval.AsString(check["backend"])
		if backend == "" {
			backend = "auto"
		}
		minRules := cfgval.String(check["min_rules"])
		if minRules == "" {
			minRules = "1"
		}
		out = append(out,
			web.WatchCondition{Field: "backend", Op: "==", Value: backend},
			web.WatchCondition{Field: "rules", Op: ">=", Value: minRules},
		)
	case "size":
		if path := cfgval.AsString(check["path"]); path != "" {
			out = append(out, web.WatchCondition{Field: "path", Value: path})
		}
		if growBy := cfgval.String(check["grow_by"]); growBy != "" {
			out = append(out, web.WatchCondition{Field: "growth", Op: ">=", Value: growBy})
		}
		if within := cfgval.String(check["within"]); within != "" {
			out = append(out, web.WatchCondition{Field: "within", Value: within})
		}
	}
	if v, ok := check["mounted"].(bool); ok {
		out = append(out, web.WatchCondition{Field: "mounted", Op: "==", Value: fmt.Sprintf("%t", v)})
	}
	if cfgval.AsString(check["type"]) == "oom" {
		if _, ok := check["delta"].(map[string]any); !ok {
			out = append(out, web.WatchCondition{Field: "delta", Op: ">", Value: "0"})
		}
	}
	out = append(out, watchMetricConditions(metrics)...)
	return out
}

func watchConditionFields(check map[string]any) []string {
	checkType := cfgval.AsString(check["type"])
	switch checkType {
	case "storage":
		return checks.StoragePredFields
	case "memory":
		return checks.MemoryPredFields
	case "pressure":
		return checks.PressurePredFields
	case "load":
		return checks.LoadPredFields
	case "fds":
		return checks.FdsPredFields
	case "pids":
		return checks.PidsPredFields
	case "conntrack":
		return checks.ConntrackPredFields
	case "entropy":
		return checks.EntropyPredFields
	case "zombies":
		return checks.ZombiePredFields
	case "oom":
		return []string{"delta"}
	case "process":
		return []string{"cpu", "memory", "io"}
	case "diskio":
		return checks.DiskIOPredFields
	case "sensors":
		return checks.SensorPredFields
	case "hdparm":
		return checks.HdparmPredFields
	case "smart":
		return checks.SmartPredFields
	case "raid":
		return checks.RaidPredFields
	case "edac":
		return checks.EdacPredFields
	case "autofs":
		return []string{"count"}
	default:
		return nil
	}
}

func fileWatchConditions(check map[string]any) []web.WatchCondition {
	var out []web.WatchCondition
	if path := cfgval.AsString(check["path"]); path != "" {
		out = append(out, web.WatchCondition{Field: "path", Value: path})
	}
	if recursive, ok := check["recursive"].(bool); ok {
		out = append(out, web.WatchCondition{Field: "recursive", Op: "==", Value: fmt.Sprintf("%t", recursive)})
	}
	if size, ok := check["size"].(map[string]any); ok {
		if on := cfgval.AsString(size["on"]); on != "" {
			out = append(out, web.WatchCondition{Field: "size", Value: on})
		} else {
			out = append(out, web.WatchCondition{Field: "size", Op: cfgval.AsString(size["op"]), Value: cfgval.String(size["value"])})
		}
	}
	for _, field := range []string{"permissions", "owner"} {
		if m, ok := check[field].(map[string]any); ok {
			out = append(out, web.WatchCondition{Field: field, Value: cfgval.AsString(m["on"])})
		}
	}
	if m, ok := check["existence"].(map[string]any); ok {
		out = append(out, web.WatchCondition{Field: "existence", Value: cfgval.AsString(m["on"])})
	}
	return out
}

func watchMetricConditions(metrics map[string]any) []web.WatchCondition {
	if len(metrics) == 0 {
		return nil
	}
	var out []web.WatchCondition
	for _, metric := range slices.Sorted(maps.Keys(metrics)) {
		entry, _ := metrics[metric].(map[string]any)
		if len(entry) == 0 {
			continue
		}
		if on := cfgval.AsString(entry["on"]); on != "" {
			out = append(out, web.WatchCondition{Field: metric + ".on", Value: on})
		}
		if expect := cfgval.AsString(entry["expect"]); expect != "" {
			out = append(out, web.WatchCondition{Field: metric + ".expect", Op: "==", Value: expect})
		}
		if delta, ok := entry["delta"].(map[string]any); ok {
			out = append(out, web.WatchCondition{
				Field: metric + ".delta",
				Op:    cfgval.AsString(delta["op"]),
				Value: cfgval.String(delta["value"]),
			})
		}
		if threshold, ok := entry["threshold"].(map[string]any); ok {
			out = append(out, web.WatchCondition{
				Field: metric + ".threshold",
				Op:    cfgval.AsString(threshold["op"]),
				Value: cfgval.String(threshold["value"]),
			})
		}
		if change, ok := entry["change"].(map[string]any); ok {
			out = append(out, web.WatchCondition{
				Field: metric + ".change",
				Op:    ">",
				Value: cfgval.String(change["delta"]),
			})
		}
		for _, field := range []string{"used_pct", "free_pct", "free_bytes"} {
			m, ok := entry[field].(map[string]any)
			if !ok {
				continue
			}
			out = append(out, web.WatchCondition{
				Field: metric + "." + field,
				Op:    cfgval.AsString(m["op"]),
				Value: cfgval.String(m["value"]),
			})
		}
	}
	return out
}

func (b *WebBackend) watchLiveView(w *webWatch, system metrics.Snapshot) (*web.WatchMeter, []web.WatchReading, string) {
	if w == nil {
		return nil, nil, ""
	}
	switch w.checkType {
	case "net":
		return b.netWatchView(w)
	case "icmp":
		return b.icmpWatchView(w)
	case "oom":
		return b.oomWatchView()
	case "fds":
		return b.fdsWatchView()
	case "pids":
		return b.pidsWatchView()
	case "pressure":
		return b.pressureWatchView(w)
	case "conntrack":
		return b.conntrackWatchView()
	case "entropy":
		return b.entropyWatchView()
	case "zombies":
		return b.zombieWatchView()
	case "process":
		return b.processWatchView(w)
	case "autofs":
		return b.autofsWatchView(w)
	case "diskio":
		return b.diskIOWatchView(w)
	case "sensors":
		return b.sensorsWatchView(w)
	case "raid":
		return b.raidWatchView()
	case "edac":
		return b.edacWatchView()
	case "route":
		return b.routeWatchView(w)
	case "file":
		return b.fileWatchView(w)
	case "count":
		return b.countWatchView(w)
	case "firewall_rules":
		return b.firewallRulesWatchView(w)
	case "size":
		return b.sizeWatchView(w)
	case "hdparm":
		return b.hdparmWatchView(w)
	case "smart":
		return b.smartWatchView(w)
	default:
		if m := watchMeter(w.checkType, system); m != nil {
			return m, nil, ""
		}
		return b.probeWatchView(w)
	}
}

func (b *WebBackend) processWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	name := cfgval.AsString(w.check["name"])
	if name == "" {
		msg := "missing name"
		return nil, watchErrorReadings(msg), "process: " + msg
	}
	user := cfgval.AsString(w.check["user"])
	sampler := b.procSampler
	if sampler == nil {
		sampler = osProcSampler{userLookup: b.userLookup}
	}
	samples, _ := sampler.Sample(ProcMatch{Name: name, User: user})
	sort.Slice(samples, func(i, j int) bool { return samples[i].PID < samples[j].PID })

	var rssTotal, cpuTicksTotal, ioTotal uint64
	ioKnown := false
	for _, sample := range samples {
		rssTotal += sample.RSS
		cpuTicksTotal += sample.CPUTicks
		if sample.HasIO {
			ioKnown = true
			ioTotal += sample.IOBytes
		}
	}

	readings := []web.WatchReading{
		{Field: "process", Label: "Process", Value: name},
		{Field: "matches", Label: "Matches", Value: fmt.Sprintf("%d", len(samples))},
	}
	if user != "" {
		readings = append(readings, web.WatchReading{Field: "user", Label: "User", Value: user})
	}
	if len(samples) > 0 {
		readings = append(readings,
			web.WatchReading{Field: "pids", Label: "PIDs", Value: processPIDList(samples)},
			web.WatchReading{Field: "rss", Label: "RSS total", Value: fmt.Sprintf("%d bytes", rssTotal)},
			web.WatchReading{Field: "cpu_ticks", Label: "CPU ticks", Value: fmt.Sprintf("%d", cpuTicksTotal)},
		)
		if ioKnown {
			readings = append(readings, web.WatchReading{Field: "io", Label: "IO total", Value: fmt.Sprintf("%d bytes", ioTotal)})
		}
	}

	target := "process " + name
	if user != "" {
		target += " user " + user
	}
	summary := fmt.Sprintf("%s: %d matching process%s", target, len(samples), pluralSuffix(len(samples), "process"))
	if len(samples) > 0 {
		summary += fmt.Sprintf(", rss %d bytes", rssTotal)
	}
	return nil, readings, summary
}

func (b *WebBackend) autofsWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	sampler := b.mountSampler
	if sampler == nil {
		sampler = checks.DefaultMounts
	}
	mounts, err := sampler()
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "autofs: " + msg
	}
	points := autofsMountpoints(mounts)
	readings := []web.WatchReading{{Field: "count", Label: "Mountpoints", Value: fmt.Sprintf("%d", len(points))}}
	if len(points) > 0 {
		readings = append(readings, web.WatchReading{Field: "mountpoints", Label: "Paths", Value: strings.Join(points, ", ")})
	}
	if path := cfgval.AsString(w.check["path"]); path != "" {
		state := "missing"
		if slices.Contains(points, path) {
			state = "active"
		}
		readings = append(readings, web.WatchReading{Field: "path", Label: "Configured path", Value: path})
		readings = append(readings, web.WatchReading{Field: "state", Label: "State", Value: state})
		return nil, readings, fmt.Sprintf("autofs %s %s (%d mountpoint%s)", path, state, len(points), pluralSuffix(len(points), "mountpoint"))
	}
	return nil, readings, fmt.Sprintf("%d autofs mountpoint%s active", len(points), pluralSuffix(len(points), "mountpoint"))
}

func (b *WebBackend) diskIOWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	device := cfgval.AsString(w.check["device"])
	if device == "" {
		msg := "missing device"
		return nil, watchErrorReadings(msg), "diskio: " + msg
	}
	sampler := b.diskIOSampler
	if sampler == nil {
		sampler = checks.SampleDiskIO
	}
	sample, err := sampler(device)
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "diskio " + device + ": " + msg
	}
	now := time.Now
	if b.now != nil {
		now = b.now
	}
	at := now()
	key := w.name + "\x00" + device

	b.diskIOMu.Lock()
	if b.diskIOState == nil {
		b.diskIOState = map[string]webDiskIOState{}
	}
	st := b.diskIOState[key]
	b.diskIOState[key] = webDiskIOState{primed: true, at: at, sample: sample}
	b.diskIOMu.Unlock()

	readings := []web.WatchReading{{Field: "device", Label: "Device", Value: device}}
	if !st.primed {
		readings = append(readings, web.WatchReading{Field: "state", Label: "State", Value: "baseline"})
		return nil, readings, "diskio " + device + " baseline"
	}
	rates, ok := checks.CalculateDiskIORates(st.sample, sample, at.Sub(st.at))
	if !ok {
		readings = append(readings, web.WatchReading{Field: "state", Label: "State", Value: "baseline"})
		return nil, readings, "diskio " + device + " baseline"
	}
	readings = append(readings,
		web.WatchReading{Field: "util_pct", Label: "Utilization", Value: watchPercent(rates.UtilPct)},
		web.WatchReading{Field: "read_bytes", Label: "Read", Value: fmt.Sprintf("%.0f B/s", rates.ReadBytes)},
		web.WatchReading{Field: "write_bytes", Label: "Write", Value: fmt.Sprintf("%.0f B/s", rates.WriteBytes)},
		web.WatchReading{Field: "await_ms", Label: "Await", Value: fmt.Sprintf("%.1f ms", rates.AwaitMs)},
	)
	return nil, readings, fmt.Sprintf("diskio %s util %.1f%% read %.0fB/s write %.0fB/s await %.1fms",
		device, rates.UtilPct, rates.ReadBytes, rates.WriteBytes, rates.AwaitMs)
}

func (b *WebBackend) sensorsWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	sampler := b.sensorSampler
	if sampler == nil {
		sampler = checks.SampleSensors
	}
	readings, err := sampler()
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "sensors: " + msg
	}
	chip := cfgval.AsString(w.check["chip"])
	label := cfgval.AsString(w.check["label"])
	values := checks.SummarizeSensors(readings, chip, label)
	out := []web.WatchReading{{Field: "inputs", Label: "Inputs", Value: fmt.Sprintf("%d", values.Count)}}
	if chip != "" {
		out = append(out, web.WatchReading{Field: "chip", Label: "Chip filter", Value: chip})
	}
	if label != "" {
		out = append(out, web.WatchReading{Field: "label", Label: "Label filter", Value: label})
	}
	parts := make([]string, 0, 3)
	if values.HasTemp {
		out = append(out, web.WatchReading{Field: "temp", Label: "Hottest temp", Value: fmt.Sprintf("%.1f C", values.Temp)})
		parts = append(parts, fmt.Sprintf("temp=%.1fC", values.Temp))
	}
	if values.HasFan {
		out = append(out, web.WatchReading{Field: "fan", Label: "Slowest fan", Value: fmt.Sprintf("%.0f RPM", values.Fan)})
		parts = append(parts, fmt.Sprintf("fan=%.0fRPM", values.Fan))
	}
	if values.HasVoltage {
		out = append(out, web.WatchReading{Field: "voltage", Label: "Lowest voltage", Value: fmt.Sprintf("%.2f V", values.Voltage)})
		parts = append(parts, fmt.Sprintf("voltage=%.2fV", values.Voltage))
	}
	if len(parts) == 0 {
		return nil, out, "sensors: no matching inputs"
	}
	return nil, out, "sensors " + strings.Join(parts, " ")
}

func (b *WebBackend) raidWatchView() (*web.WatchMeter, []web.WatchReading, string) {
	sampler := b.raidSampler
	if sampler == nil {
		sampler = checks.SampleRaid
	}
	st, err := sampler()
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "raid: " + msg
	}
	readings := []web.WatchReading{
		{Field: "arrays", Label: "Arrays", Value: fmt.Sprintf("%d", st.Arrays)},
		{Field: "degraded", Label: "Degraded", Value: fmt.Sprintf("%d", st.Degraded)},
		{Field: "recovering", Label: "Recovering", Value: fmt.Sprintf("%d", st.Recovering)},
	}
	summary := fmt.Sprintf("raid: %d arrays, %d degraded, %d recovering", st.Arrays, st.Degraded, st.Recovering)
	if len(st.DegradedNames) > 0 {
		names := strings.Join(st.DegradedNames, ", ")
		readings = append(readings, web.WatchReading{Field: "degraded_arrays", Label: "Degraded arrays", Value: names})
		summary += " (" + names + ")"
	}
	return nil, readings, summary
}

func (b *WebBackend) edacWatchView() (*web.WatchMeter, []web.WatchReading, string) {
	sampler := b.edacSampler
	if sampler == nil {
		sampler = checks.SampleEdac
	}
	st, err := sampler()
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "edac: " + msg
	}
	if !st.Present {
		msg := "no EDAC controllers"
		return nil, []web.WatchReading{{Field: "present", Label: "EDAC", Error: msg}}, "edac: " + msg
	}
	return nil,
		[]web.WatchReading{
			{Field: "ce", Label: "Correctable", Value: fmt.Sprintf("%d", st.CE)},
			{Field: "ue", Label: "Uncorrectable", Value: fmt.Sprintf("%d", st.UE)},
		},
		fmt.Sprintf("edac: %d correctable, %d uncorrectable", st.CE, st.UE)
}

func (b *WebBackend) routeWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	family := cfgval.AsString(w.check["family"])
	if family == "" {
		family = "ipv4"
	}
	iface := cfgval.AsString(w.check["interface"])
	sampler := b.routeSampler
	if sampler == nil {
		sampler = checks.SampleRoutes
	}
	routes, err := sampler(family)
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "route: " + msg
	}
	matched := matchingDefaultRoutes(routes, iface)
	readings := []web.WatchReading{
		{Field: "family", Label: "Family", Value: family},
		{Field: "routes", Label: "Default routes", Value: fmt.Sprintf("%d", len(routes))},
	}
	if iface != "" {
		readings = append(readings, web.WatchReading{Field: "interface", Label: "Required interface", Value: iface})
	}
	if len(matched) > 0 {
		readings = append(readings, web.WatchReading{Field: "egress", Label: "Egress", Value: matched[0].Iface})
		if matched[0].Gateway != "" {
			readings = append(readings, web.WatchReading{Field: "gateway", Label: "Gateway", Value: matched[0].Gateway})
		}
	}
	switch {
	case len(matched) > 0 && matched[0].Gateway != "":
		return nil, readings, fmt.Sprintf("%s default route via %s (gw %s)", family, matched[0].Iface, matched[0].Gateway)
	case len(matched) > 0:
		return nil, readings, fmt.Sprintf("%s default route via %s", family, matched[0].Iface)
	case iface != "" && len(routes) > 0:
		return nil, readings, fmt.Sprintf("no %s default route via %s (%d elsewhere)", family, iface, len(routes))
	default:
		return nil, readings, "no " + family + " default route"
	}
}

func (b *WebBackend) netWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	iface := cfgval.AsString(w.check["interface"])
	if iface == "" {
		msg := "missing interface"
		return nil, watchErrorReadings(msg), "net: " + msg
	}
	sampler := b.netSampler
	if sampler == nil {
		sampler = checks.SampleNet
	}
	s, err := sampler(iface)
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "net " + iface + ": " + msg
	}

	readings := []web.WatchReading{
		{Field: "interface", Label: "Interface", Value: iface},
		{Field: "state", Label: "State", Value: s.State},
	}
	parts := []string{iface + " state " + s.State}
	if watchMetricEnabled(w.metrics, "speed") {
		if s.SpeedKnown {
			readings = append(readings, web.WatchReading{Field: "speed", Label: "Speed", Value: fmt.Sprintf("%d Mbps", s.SpeedMbps)})
			parts = append(parts, fmt.Sprintf("speed %d Mbps", s.SpeedMbps))
		} else {
			readings = append(readings, web.WatchReading{Field: "speed", Label: "Speed", Value: "unknown"})
			parts = append(parts, "speed unknown")
		}
	}
	if watchMetricEnabled(w.metrics, "errors") {
		total := netErrorTotal(w.metrics, s.Counters)
		readings = append(readings, web.WatchReading{Field: "errors", Label: "Errors total", Value: fmt.Sprintf("%d", total)})
		parts = append(parts, fmt.Sprintf("errors %d", total))
	}
	if watchMetricEnabled(w.metrics, "address") {
		value := strings.Join(s.Addrs, ", ")
		if value == "" {
			value = "none"
		}
		readings = append(readings, web.WatchReading{Field: "address", Label: "Addresses", Value: value})
		parts = append(parts, fmt.Sprintf("%d address%s", len(s.Addrs), pluralSuffix(len(s.Addrs), "address")))
	}
	return nil, readings, strings.Join(parts, " · ")
}

func (b *WebBackend) icmpWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	host := cfgval.AsString(w.check["host"])
	if host == "" {
		msg := "missing host"
		return nil, watchErrorReadings(msg), "icmp: " + msg
	}
	count := 3
	if v, ok := cfgval.Int(w.check["count"]); ok && v > 0 {
		count = v
	}
	timeout := cfgval.Duration(w.check["timeout"])
	if timeout <= 0 {
		timeout = b.defaultTimeout
	}
	s, err := checks.SampleICMP(host, cfgval.StringList(w.check["interface"]),
		cfgval.AsString(w.check["interface_match"]) == "all", count, timeout, b.pingSampler)
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "icmp " + host + ": " + msg
	}
	state := "down"
	if s.Reachable {
		state = "up"
	}
	readings := []web.WatchReading{
		{Field: "host", Label: "Host", Value: host},
		{Field: "state", Label: "State", Value: state},
	}
	parts := []string{host + " " + state}
	if s.RTTKnown {
		readings = append(readings, web.WatchReading{Field: "latency", Label: "RTT", Value: fmt.Sprintf("%.1f ms", s.RTTms)})
		parts = append(parts, fmt.Sprintf("rtt %.1f ms", s.RTTms))
	} else if watchMetricEnabled(w.metrics, "latency") {
		readings = append(readings, web.WatchReading{Field: "latency", Label: "RTT", Value: "unknown"})
		parts = append(parts, "rtt unknown")
	}
	return nil, readings, strings.Join(parts, " · ")
}

func (b *WebBackend) oomWatchView() (*web.WatchMeter, []web.WatchReading, string) {
	sampler := b.oomSampler
	if sampler == nil {
		sampler = checks.SampleOom
	}
	count, ok := sampler()
	if !ok {
		msg := "oom_kill counter unavailable"
		return nil, watchErrorReadings(msg), "oom: " + msg
	}
	return nil,
		[]web.WatchReading{{Field: "total", Label: "OOM kills", Value: fmt.Sprintf("%d", count)}},
		fmt.Sprintf("%d oom_kill total", count)
}

func (b *WebBackend) fdsWatchView() (*web.WatchMeter, []web.WatchReading, string) {
	sampler := b.fdsSampler
	if sampler == nil {
		sampler = checks.SampleFds
	}
	s, err := sampler()
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "fds: " + msg
	}
	summary := fmt.Sprintf("fds %d allocated", s.Allocated)
	if s.Max > 0 {
		usedPct := float64(s.Allocated) / float64(s.Max) * 100
		summary = fmt.Sprintf("fds %d/%d allocated (%.1f%%)", s.Allocated, s.Max, usedPct)
	}
	if meter := countMeter("fds", s.Allocated, s.Max); meter != nil {
		return meter, nil, summary
	}
	return nil, []web.WatchReading{{Field: "count", Label: "Allocated", Value: fmt.Sprintf("%d", s.Allocated)}}, summary
}

func (b *WebBackend) pidsWatchView() (*web.WatchMeter, []web.WatchReading, string) {
	sampler := b.pidsSampler
	if sampler == nil {
		sampler = checks.SamplePids
	}
	s, err := sampler()
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "pids: " + msg
	}
	summary := fmt.Sprintf("pids %d in use", s.Threads)
	if s.Max > 0 {
		usedPct := float64(s.Threads) / float64(s.Max) * 100
		summary = fmt.Sprintf("pids %d/%d in use (%.1f%%)", s.Threads, s.Max, usedPct)
	}
	if meter := countMeter("pids", s.Threads, s.Max); meter != nil {
		return meter, nil, summary
	}
	return nil, []web.WatchReading{{Field: "count", Label: "In use", Value: fmt.Sprintf("%d", s.Threads)}}, summary
}

func (b *WebBackend) pressureWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	resource := cfgval.AsString(w.check["resource"])
	if resource == "" {
		msg := "missing resource"
		return nil, watchErrorReadings(msg), "pressure: " + msg
	}
	sampler := b.pressureSampler
	if sampler == nil {
		sampler = checks.SamplePressure
	}
	s, err := sampler(resource)
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "pressure " + resource + ": " + msg
	}
	readings := []web.WatchReading{
		{Field: "resource", Label: "Resource", Value: resource},
		{Field: "some_avg10", Label: "Some avg10", Value: watchPercent(s.Some.Avg10)},
		{Field: "some_avg60", Label: "Some avg60", Value: watchPercent(s.Some.Avg60)},
		{Field: "some_avg300", Label: "Some avg300", Value: watchPercent(s.Some.Avg300)},
		{Field: "full_avg10", Label: "Full avg10", Value: watchPercent(s.Full.Avg10)},
		{Field: "full_avg60", Label: "Full avg60", Value: watchPercent(s.Full.Avg60)},
		{Field: "full_avg300", Label: "Full avg300", Value: watchPercent(s.Full.Avg300)},
	}
	summary := fmt.Sprintf("pressure %s some %.2f/%.2f/%.2f full %.2f/%.2f/%.2f",
		resource, s.Some.Avg10, s.Some.Avg60, s.Some.Avg300, s.Full.Avg10, s.Full.Avg60, s.Full.Avg300)
	return nil, readings, summary
}

func (b *WebBackend) conntrackWatchView() (*web.WatchMeter, []web.WatchReading, string) {
	sampler := b.conntrackSampler
	if sampler == nil {
		sampler = checks.SampleConntrack
	}
	s, err := sampler()
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "conntrack: " + msg
	}
	summary := fmt.Sprintf("conntrack %d entries", s.Count)
	if s.Max > 0 {
		usedPct := float64(s.Count) / float64(s.Max) * 100
		summary = fmt.Sprintf("conntrack %d/%d entries (%.1f%%)", s.Count, s.Max, usedPct)
	}
	if meter := countMeter("conntrack", s.Count, s.Max); meter != nil {
		return meter, nil, summary
	}
	return nil, []web.WatchReading{{Field: "count", Label: "Count", Value: fmt.Sprintf("%d entries", s.Count)}}, summary
}

func (b *WebBackend) entropyWatchView() (*web.WatchMeter, []web.WatchReading, string) {
	sampler := b.entropySampler
	if sampler == nil {
		sampler = checks.SampleEntropy
	}
	avail, ok := sampler()
	if !ok {
		msg := "entropy_avail unavailable"
		return nil, watchErrorReadings(msg), "entropy: " + msg
	}
	return nil,
		[]web.WatchReading{{Field: "avail", Label: "Available", Value: fmt.Sprintf("%d bits", avail)}},
		fmt.Sprintf("%d available bits", avail)
}

func (b *WebBackend) zombieWatchView() (*web.WatchMeter, []web.WatchReading, string) {
	sampler := b.zombieSampler
	if sampler == nil {
		sampler = checks.SampleZombies
	}
	count, ok := sampler()
	if !ok {
		msg := "cannot read /proc"
		return nil, watchErrorReadings(msg), "zombies: " + msg
	}
	return nil,
		[]web.WatchReading{{Field: "count", Label: "Zombies", Value: fmt.Sprintf("%d", count)}},
		fmt.Sprintf("%d zombie processes", count)
}

func watchErrorReadings(message string) []web.WatchReading {
	return []web.WatchReading{{Field: "sample", Label: "Sample", Error: message}}
}

func watchPercent(value float64) string {
	return fmt.Sprintf("%.2f%%", value)
}

func watchMetricEnabled(metrics map[string]any, metric string) bool {
	if len(metrics) == 0 {
		return true
	}
	_, ok := metrics[metric]
	return ok
}

func netErrorTotal(metrics map[string]any, counters map[string]uint64) uint64 {
	names := []string{"rx_errors", "tx_errors"}
	if entry, ok := metrics["errors"].(map[string]any); ok {
		if configured := cfgval.StringArray(entry["counters"]); len(configured) > 0 {
			names = configured
		}
	}
	var total uint64
	for _, name := range names {
		total += counters[name]
	}
	return total
}

// pluralSuffix returns the suffix to append to singular to form its plural for
// count items: "" when count is 1, "es" for sibilant endings (process ->
// processes, address -> addresses) and "s" otherwise (mountpoint -> mountpoints).
func pluralSuffix(count int, singular string) string {
	if count == 1 {
		return ""
	}
	switch {
	case strings.HasSuffix(singular, "s"), strings.HasSuffix(singular, "x"),
		strings.HasSuffix(singular, "z"), strings.HasSuffix(singular, "ch"),
		strings.HasSuffix(singular, "sh"):
		return "es"
	default:
		return "s"
	}
}

func processPIDList(samples []ProcInfo) string {
	const limit = 20
	parts := make([]string, 0, min(len(samples), limit)+1)
	for i, sample := range samples {
		if i >= limit {
			break
		}
		parts = append(parts, fmt.Sprintf("%d", sample.PID))
	}
	if extra := len(samples) - limit; extra > 0 {
		parts = append(parts, fmt.Sprintf("+%d more", extra))
	}
	return strings.Join(parts, ", ")
}

func autofsMountpoints(mounts []checks.Mount) []string {
	var points []string
	for _, mount := range mounts {
		if mount.FSType == "autofs" {
			points = append(points, mount.MountPoint)
		}
	}
	sort.Strings(points)
	return points
}

func matchingDefaultRoutes(routes []checks.DefaultRoute, iface string) []checks.DefaultRoute {
	if iface == "" {
		return routes
	}
	var matched []checks.DefaultRoute
	for _, route := range routes {
		if route.Iface == iface {
			matched = append(matched, route)
		}
	}
	return matched
}

// swapWatchInfo reads the host swap usage from the collector's cached system
// snapshot (shared with the overview tiles, no extra probe). nil when the host
// has no swap or no collector is wired.
func swapWatchInfo(system metrics.Snapshot) *web.SwapWatchInfo {
	r := system["total_swap"]
	used, total, free, ok := byteUsage(r)
	if !ok {
		return nil
	}
	return &web.SwapWatchInfo{
		TotalBytes: total,
		UsedBytes:  used,
		FreeBytes:  free,
		UsedPct:    r.Percent,
	}
}

// byteUsage reads a capacity-carrying usage Reading (memory/swap) as used/total/
// free bytes, clamping free so a "used" momentarily above total cannot underflow
// the unsigned subtraction. ok is false when the reading carries no capacity
// (no total), including the zero Reading a missing metric yields.
func byteUsage(r metrics.Reading) (used, total, free uint64, ok bool) {
	if !r.HasTotal || r.Total <= 0 {
		return 0, 0, 0, false
	}
	used, total = uint64(r.Absolute), uint64(r.Total)
	return used, total, total - min(used, total), true
}

// watchMeter builds the generic usage gauge (progress bar) for host watch types
// served by the collector's cached system snapshot (shared with overview tiles,
// no extra probe). nil for any other type, or when the needed data is unavailable.
func watchMeter(checkType string, system metrics.Snapshot) *web.WatchMeter {
	switch checkType {
	case "memory":
		r := system["total_memory"]
		used, total, free, ok := byteUsage(r)
		if !ok {
			return nil
		}
		return &web.WatchMeter{
			Kind:       "memory",
			UsedPct:    r.Percent,
			TotalBytes: total,
			UsedBytes:  used,
			FreeBytes:  free,
		}
	case "load":
		r, ok := system["load1"]
		if !ok || !r.HasAbsolute {
			return nil
		}
		ncpu := runtime.NumCPU()
		pct := 0.0
		if ncpu > 0 {
			pct = r.Absolute / float64(ncpu) * 100
		}
		return &web.WatchMeter{Kind: "load", UsedPct: pct, Load: r.Absolute, NumCPU: ncpu}
	}
	return nil
}

// countMeter builds a count-vs-limit gauge (fds, pids) as a percentage of the
// kernel maximum. nil when the limit is unknown (limit == 0), so the meter is
// simply absent rather than dividing by zero.
func countMeter(kind string, count, limit uint64) *web.WatchMeter {
	if limit == 0 {
		return nil
	}
	return &web.WatchMeter{
		Kind:    kind,
		UsedPct: float64(count) / float64(limit) * 100,
		Count:   count,
		Max:     limit,
	}
}

// monitorView reads one monitor record and renders the view fields services
// and watches share: active flag, source, and the RFC3339 change time ("" when
// unknown). ok is false when there is no store or no record.
func (b *WebBackend) monitorView(key string) (active bool, source, changedAt string, ok bool) {
	if b.store == nil {
		return false, "", "", false
	}
	rec, found, err := b.store.MonitorState(key)
	if err != nil || !found {
		return false, "", "", false
	}
	changed := ""
	if !rec.UpdatedAt.IsZero() {
		changed = rec.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return rec.Active, rec.Source, changed, true
}

func storageWatchInfo(w *webWatch, b *WebBackend) *web.StorageWatchInfo {
	if w == nil || w.check == nil {
		return nil
	}
	path := cfgval.String(w.check["path"])
	if path == "" {
		return nil
	}
	info := &web.StorageWatchInfo{Path: path}

	usage := b.storageUsage
	if usage == nil {
		usage = checks.DefaultStorageUsage
	}
	if st, err := usage(path); err != nil {
		info.SampleError = err.Error()
	} else {
		info.TotalBytes = st.TotalBytes
		info.FreeBytes = st.FreeBytes
		info.UsedBytes = st.UsedBytes
		if info.UsedBytes == 0 && st.TotalBytes >= st.FreeBytes {
			info.UsedBytes = st.TotalBytes - st.FreeBytes
		}
		info.UsedPct = st.UsedPct
		info.FreePct = st.FreePct
		info.InodesTotal = st.InodesTotal
		info.InodesFree = st.InodesFree
		info.InodesUsedPct = st.InodesUsedPct
		info.InodesFreePct = st.InodesFreePct
	}

	mountSampler := b.mountSampler
	if mountSampler == nil {
		mountSampler = checks.DefaultMounts
	}
	mounts, err := mountSampler()
	if err != nil {
		info.MountSampleError = err.Error()
		return info
	}
	if mount := checks.MountForPath(mounts, path); mount != nil {
		info.Mounted = true
		info.MountPoint = mount.MountPoint
		info.Device = mount.Device
		info.FileSystem = mount.FSType
		info.Options = slices.Clone(mount.Options)
	}
	return info
}

// Notifiers returns the configured notification targets.
func (b *WebBackend) Notifiers(ctx context.Context) []web.Notifier {
	if len(b.notifierOrder) == 0 {
		return nil
	}
	usedBy := map[string]int{}
	for _, w := range b.watches {
		if w == nil {
			continue
		}
		for _, n := range w.notifiers {
			usedBy[n]++
		}
	}
	out := make([]web.Notifier, 0, len(b.notifierOrder))
	for _, name := range b.notifierOrder {
		n := b.notifiers[name]
		if n == nil {
			continue
		}
		out = append(out, web.Notifier{
			Name:    n.name,
			Type:    n.typ,
			Enabled: n.enabled,
			Summary: n.summary,
			UsedBy:  usedBy[name],
		})
	}
	return out
}

// Applications returns the installed applications (catalog app daemons whose
// binary is present) with their version and binary location, reusing the same
// inspection the sermoctl `apps` listing uses so both surfaces agree.
func (b *WebBackend) Applications(ctx context.Context) []web.Application {
	b.applicationsMu.Lock()
	defer b.applicationsMu.Unlock()

	if !b.applicationsAt.IsZero() && time.Since(b.applicationsAt) < applicationsCacheTTL {
		return slices.Clone(b.applicationsCache)
	}
	apps := b.loadApplications(ctx)
	b.applicationsAt = time.Now()
	b.applicationsCache = slices.Clone(apps)
	return apps
}

func (b *WebBackend) loadApplications(ctx context.Context) []web.Application {
	if b.applicationsList != nil {
		return b.withApplicationSLA(b.applicationsList(ctx))
	}
	reports := appinspect.List(ctx, execx.CommandRunner{}, b.cfg, config.CategoryApp, false, appinspect.WithUserLookup(b.userLookup))
	if len(reports) == 0 {
		return nil
	}
	out := make([]web.Application, 0, len(reports))
	for _, r := range reports {
		out = append(out, web.Application{
			Name:          r.Name,
			DisplayName:   r.DisplayName,
			Category:      r.Category,
			Binary:        r.Binary,
			Permissions:   r.Permissions,
			User:          r.User,
			Group:         r.Group,
			Version:       r.Version,
			VersionShort:  r.VersionShort,
			VersionSource: r.VersionSource,
			Status:        r.Status,
		})
	}
	return b.withApplicationSLA(out)
}

func (b *WebBackend) withApplicationSLA(apps []web.Application) []web.Application {
	if len(apps) == 0 {
		return apps
	}
	out := slices.Clone(apps)
	now := b.webNow()
	for i := range out {
		if b.entries[out[i].Name] != nil {
			out[i].SLA = b.serviceSLAWindows(out[i].Name, now)
		}
	}
	return out
}

// DaemonInfo returns the daemon's effective configuration and host identity.
func (b *WebBackend) DaemonInfo(ctx context.Context) web.DaemonInfo {
	info := web.DaemonInfo{}

	if h, err := os.Hostname(); err == nil {
		info.Hostname = h
	}
	info.OS = osPrettyName()
	if up, ok := hostUptime(); ok {
		info.HostUptimeSeconds = int64(up.Seconds())
		info.HostUptime = formatInterval(up.Round(time.Second))
	}

	if b.cfg != nil {
		g := b.cfg.Global
		info.ConfigPath = g.Path
		info.RuntimeDir = g.RuntimeDir()
		info.StateDir = g.StateDir()

		// Engine block (effective values with documented fallbacks)
		info.Interval = formatInterval(config.EngineInterval(b.cfg, 30*time.Second))
		info.MaxParallelChecks = EngineInt(b.cfg, "max_parallel_checks", 8)
		info.MaxParallelOperations = EngineInt(b.cfg, "max_parallel_operations", 2)
		info.DefaultTimeout = formatInterval(EngineDuration(b.cfg, "default_timeout", 10*time.Second))
		info.OperationTimeout = formatInterval(EngineDuration(b.cfg, "operation_timeout", 90*time.Second))
		info.StartupDelay = formatInterval(EngineDuration(b.cfg, "startup_delay", 0))

		if em := engineMap(b.cfg); em != nil {
			if be, ok := em["backend"].(string); ok && be != "" {
				info.Backend = be
			}
		}
		if info.Backend == "" {
			info.Backend = "auto"
		}
	}

	return info
}

// DaemonMetrics returns current and historical resource usage for the running
// sermod process.
func (b *WebBackend) DaemonMetrics(_ context.Context, since time.Duration) web.DaemonMetrics {
	if b.daemonMetrics == nil {
		return web.DaemonMetrics{Since: since.String()}
	}
	return b.daemonMetrics.Series(since)
}

// formatInterval renders a duration for display, dropping zero components so a
// whole-hour interval reads "1h" instead of Go's default "1h0m0s". It extends
// Go's units upward with day (d), week (w) and month (mo, taken as 30 days) so
// long intervals stay compact: 24h reads "1d", 7d "1w", 30d "1mo", and mixed
// values chain greatest-first ("1mo1w", "1d6h", "1h30m"). A zero (or negative)
// duration is shown as "0s" — the only case where a 0 component survives.
// Sub-second durations keep the standard library formatting (e.g. "1.5s").
func formatInterval(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d%time.Second != 0 {
		return d.String()
	}
	total := int64(d / time.Second)
	const (
		minute = 60
		hour   = 60 * minute
		day    = 24 * hour
		week   = 7 * day
		month  = 30 * day // display approximation
	)
	units := []struct {
		secs   int64
		suffix string
	}{
		{month, "mo"},
		{week, "w"},
		{day, "d"},
		{hour, "h"},
		{minute, "m"},
		{1, "s"},
	}
	var b strings.Builder
	for _, u := range units {
		if total >= u.secs {
			fmt.Fprintf(&b, "%d%s", total/u.secs, u.suffix)
			total %= u.secs
		}
	}
	return b.String()
}

// hostUptime returns how long the host/server has been running since boot,
// read natively from /proc/uptime. The second return is false when the host
// uptime is unavailable (e.g. the file is missing on non-Linux systems).
func hostUptime() (time.Duration, bool) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, false
	}
	return parseProcUptime(data)
}

// parseProcUptime extracts the boot-relative uptime from the contents of
// /proc/uptime, whose first whitespace-separated field is the number of
// seconds (a float) since boot. It returns false when the value is missing or
// unparseable.
func parseProcUptime(data []byte) (time.Duration, bool) {
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, false
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || secs < 0 {
		return 0, false
	}
	return time.Duration(secs * float64(time.Second)), true
}

// osPrettyName returns a human-friendly OS label (PRETTY_NAME from os-release on
// Linux, e.g. "Debian GNU/Linux 12 (bookworm)"), falling back to runtime.GOOS.
func osPrettyName() string {
	for _, path := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		if data, err := os.ReadFile(path); err == nil {
			if name := parseOSReleasePrettyName(data); name != "" {
				return name
			}
		}
	}
	return runtime.GOOS
}

// parseOSReleasePrettyName extracts the (unquoted) PRETTY_NAME value from
// os-release content, or "" when absent. Pure, so it is testable without the
// host files.
func parseOSReleasePrettyName(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "PRETTY_NAME="); ok {
			if name := strings.Trim(v, `"'`); name != "" {
				return name
			}
		}
	}
	return ""
}

// HostMetrics returns the current host-level readings from the collector.
func (b *WebBackend) HostMetrics(ctx context.Context) []web.HostMetric {
	if b.collector == nil {
		return nil
	}
	snap := b.collector.SampleSystem()
	if len(snap) == 0 {
		return nil
	}

	out := make([]web.HostMetric, 0, len(snap))
	order := []string{"load1", "load5", "load15", "total_cpu", "total_memory", "total_swap"} // nice display order
	seen := map[string]bool{}
	for _, k := range order {
		if r, ok := snap[k]; ok {
			out = append(out, hostMetric(k, r))
			seen[k] = true
		}
	}
	for k, r := range snap { // any others the collector reported, after the ordered ones
		if !seen[k] {
			out = append(out, hostMetric(k, r))
		}
	}
	return out
}

// hostMetric maps a collector reading to the web view, applying the metric's
// display specifics: a bytes unit for memory/swap, and a 0-100% saturation
// reading for load1 (load vs logical CPUs, capacity = CPU count) so the overview
// tile can draw a bar like cpu/mem/swap. The raw load stays in Absolute.
func hostMetric(name string, r metrics.Reading) web.HostMetric {
	m := web.HostMetric{Name: name, Ready: r.Ready}
	if r.HasPercent {
		m.Percent = r.Percent
	}
	if r.HasAbsolute {
		m.Absolute = r.Absolute
	}
	if r.HasTotal {
		m.Total = r.Total
	}
	switch name {
	case "total_memory", "total_swap":
		m.Unit = "bytes"
	case "load1":
		// Only derive the per-CPU percentage from a real reading; guarding on
		// HasAbsolute (as watchMeter does) avoids fabricating Total/Percent when
		// load1 has no absolute value.
		if r.HasAbsolute {
			if ncpu := runtime.NumCPU(); ncpu > 0 {
				m.Total = float64(ncpu)
				m.Percent = r.Absolute / float64(ncpu) * 100
			}
		}
	}
	return m
}

// Locks returns the active and stale runtime locks across services.
func (b *WebBackend) Locks(_ context.Context) []web.Lock {
	var out []web.Lock
	now := time.Now()
	reports := b.lockReportsByService()
	for _, name := range b.order {
		e := b.entries[name]
		if e == nil || e.disabled {
			continue
		}
		report := reports[name]
		for _, lk := range report.Locks {
			out = append(out, lockToWebAt(lk, name, now))
		}
	}
	return out
}

// ReleaseLock explicitly removes a stale or expired named runtime lock. Active
// locks continue to block service actions until their owner releases them or the
// TTL/staleness rules make them inactive.
func (b *WebBackend) ReleaseLock(_ context.Context, service, name string) web.ActionResult {
	if _, ok := b.entries[service]; !ok {
		msg := "unknown service " + service
		b.emitLockReleaseEvent(service, name, "error", "failed", msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	if b.cfg == nil {
		msg := "runtime locks are unavailable"
		b.emitLockReleaseEvent(service, name, "error", "failed", msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	locker := locks.NewNamedLocker(filepath.Join(b.cfg.Global.RuntimeDir(), "locks"))
	lk, err := locker.ReleaseInactive(service, name)
	if err != nil {
		msg := err.Error()
		if lk.State == locks.StateActive {
			b.emitLockReleaseEvent(service, name, "suppressed", "blocked", msg)
		} else {
			b.emitLockReleaseEvent(service, name, "error", "failed", msg)
		}
		return web.ActionResult{OK: false, Message: msg}
	}
	id := service
	if name != "" {
		id += "." + name
	}
	msg := "released inactive runtime lock " + id
	b.emitLockReleaseEvent(service, name, "action", "ok", msg)
	return web.ActionResult{OK: true, Message: msg}
}

func (b *WebBackend) emitLockReleaseEvent(service, name, kind, status, message string) {
	if b.emit == nil {
		return
	}
	rule := name
	if rule == "" {
		rule = "default"
	}
	b.emit(Event{
		Service: service,
		Kind:    kind,
		Rule:    rule,
		Action:  "release-lock",
		Status:  status,
		Message: message,
	})
}

func isServiceOperationAction(action string) bool {
	switch action {
	case "start", "stop", "restart", "reload", "resume":
		return true
	default:
		return false
	}
}

func serviceOperationActionList() []string {
	return []string{"start", "stop", "restart", "reload", "resume"}
}

// ActivitySummary returns a rollup of recent events for the dashboard.
func (b *WebBackend) ActivitySummary(ctx context.Context) web.ActivitySummary {
	summary := web.ActivitySummary{}

	if b.events == nil {
		return summary
	}

	// Scan a reasonable number of recent events (cheap for the UI)
	events := b.events.Recent("", 500)
	summary.TotalEvents = len(events)

	if len(events) > 0 {
		latest := events[0]
		summary.LastEventTime = latest.Time.Format(time.RFC3339)
		summary.LastEventKind = latest.Kind
		summary.LastEventService = latest.Service
		summary.LastEventWatch = latest.Watch
	}

	for _, e := range events {
		switch {
		case e.Kind == "action" && isServiceOperationAction(e.Action):
			summary.ServiceActions++
		case e.Kind == "hook" || e.Kind == "hook-failed":
			summary.WatchHooks++
		case e.Kind == "notify" || e.Kind == "notify-failed":
			summary.WatchNotifies++
		case e.Kind == "error":
			summary.Errors++
		}
	}

	return summary
}

// MonitoringStatus returns how many services are monitored versus paused.
func (b *WebBackend) MonitoringStatus(_ context.Context) web.MonitoringStatus {
	total := len(b.order)
	monitored := 0
	for _, name := range b.order {
		e := b.entries[name]
		if e == nil || e.disabled {
			continue
		}
		active := true
		if monitoredState, _, _, ok := b.monitorView(name); ok {
			active = monitoredState
		}
		if active {
			monitored++
		}
	}
	return web.MonitoringStatus{
		Total:     total,
		Monitored: monitored,
		Paused:    total - monitored,
	}
}

// Detail returns the full detail view for one service.
func (b *WebBackend) Detail(ctx context.Context, name string) (web.Detail, bool) {
	e := b.entries[name]
	if e == nil {
		return web.Detail{}, false
	}
	if e.disabled {
		return web.Detail{Service: b.view(ctx, name, e), NoResidentProcess: e.noResidentProcess}, true
	}
	d := web.Detail{Service: b.view(ctx, name, e), NoResidentProcess: e.noResidentProcess}
	now := time.Now()

	snap := b.snapshots.Get(name)
	for _, cn := range e.checkNames {
		cs, seen := snap[cn]
		ch := web.Check{
			Name:     cn,
			Type:     e.checkTypes[cn],
			OK:       cs.OK,
			Optional: cs.Optional,
			Skipped:  cs.Skipped,
			Message:  cs.Message,
			Readings: checkReadings(e.checkTypes[cn], cs.Data),
			Ran:      seen && cs.Ran,
		}
		if seen && !cs.At.IsZero() {
			ch.At = cs.At.UTC().Format(time.RFC3339)
		}
		for _, m := range checks.GraphMetrics(e.checkTypes[cn]) {
			ch.Metrics = append(ch.Metrics, web.CheckMetric{Name: m.Key, Unit: m.Unit})
		}
		ch.SLA = b.checkSLAWindows(name, cn, now)
		d.Checks = append(d.Checks, ch)
	}

	d.SLA = b.serviceSLAWindows(name, now)

	if report, err := serviceLocksReport(b.cfg, name); err == nil {
		for _, lk := range report.Locks {
			d.Locks = append(d.Locks, lockToWeb(lk, name))
		}
		if len(report.Warnings) > 0 {
			d.LockWarnings = slices.Clone(report.Warnings)
		}
	}

	if !e.noResidentProcess {
		procs, procWarnings := e.discoverer.Discover(e.selectors)
		procWarnings = append(slices.Clone(e.processWarnings), procWarnings...)
		if len(procWarnings) > 0 {
			d.ProcessWarnings = procWarnings
		}
		d.Processes, d.ProcessTotals = aggregateProcesses(procs, b.runtimeMetricReader())
		attachLiveCPU(&d, b.live, name)
	}

	if b.remediation != nil {
		if rep, ok := b.remediation.Get(name); ok {
			r := remediationToWeb(rep)
			d.Remediation = &r
		}
	}
	if b.ruleWindows != nil {
		if reps, ok := b.ruleWindows.Get(name); ok {
			for _, rep := range reps {
				d.Rules = append(d.Rules, ruleWindowToWeb(rep))
			}
		}
	}
	return d, true
}

func (b *WebBackend) serviceSLAWindows(name string, now time.Time) []web.SLAWindow {
	return b.cachedSLAWindows(name, "", now)
}

func (b *WebBackend) checkSLAWindows(service, check string, now time.Time) []web.SLAWindow {
	return b.cachedSLAWindows(service, check, now)
}

func (b *WebBackend) cachedSLAWindows(service, check string, now time.Time) []web.SLAWindow {
	if b.sla == nil {
		return nil
	}
	key := slaCacheKey{service: service, check: check}
	b.slaCacheMu.Lock()
	if b.slaCache == nil {
		b.slaCache = map[slaCacheKey]cachedSLATimelines{}
	}
	if cached, ok := b.slaCache[key]; ok && now.Sub(cached.at) < slaTimelineCacheTTL {
		out := cached.windows
		b.slaCacheMu.Unlock()
		return slices.Clone(out)
	}
	b.slaCacheMu.Unlock()

	var tls []state.SLAWindowTimeline
	var err error
	if check == "" {
		tls, err = b.sla.SLATimelines(service, now)
	} else {
		tls, err = b.sla.CheckSLATimelines(service, check, now)
	}
	if err != nil {
		return nil
	}
	windows := toWebSLAWindows(tls)

	b.slaCacheMu.Lock()
	b.slaCache[key] = cachedSLATimelines{windows: slices.Clone(windows), at: now}
	b.slaCacheMu.Unlock()
	return windows
}

func toWebSLAWindows(tls []state.SLAWindowTimeline) []web.SLAWindow {
	out := make([]web.SLAWindow, 0, len(tls))
	for _, t := range tls {
		win := web.SLAWindow{Window: t.Window, Up: t.Up, Total: t.Total}
		if t.Total > 0 {
			ratio := float64(t.Up) / float64(t.Total)
			win.Ratio = &ratio
		}
		if len(t.Segments) > 0 {
			segs := make([]*float64, len(t.Segments))
			for i, g := range t.Segments {
				if g.Total > 0 {
					ratio := float64(g.Up) / float64(g.Total)
					segs[i] = &ratio
				}
			}
			win.Segments = segs
		}
		out = append(out, win)
	}
	return out
}

func ruleWindowToWeb(rep rules.RuleWindowReport) web.RuleWindow {
	return web.RuleWindow{
		Name:          rep.Name,
		Type:          rep.Type,
		Action:        rep.Action,
		Condition:     rep.Condition,
		ConditionTrue: rep.ConditionTrue,
		Window:        rep.Window,
		Progress:      rep.Progress,
		Firing:        rep.Firing,
	}
}

func remediationToWeb(rep rules.RemediationReport) web.Remediation {
	r := web.Remediation{
		Allowed:       rep.Allowed,
		Reason:        rep.Reason,
		MaxActions:    rep.MaxActions,
		RecentActions: rep.RecentActions,
	}
	if rep.Cooldown > 0 {
		r.Cooldown = rep.Cooldown.String()
	}
	if rep.EffectiveCooldown > 0 {
		r.EffectiveCooldown = rep.EffectiveCooldown.String()
	}
	if rep.CurrentBackoff > 0 {
		r.CurrentBackoff = rep.CurrentBackoff.String()
	}
	if !rep.LastActionAt.IsZero() {
		r.LastActionAt = rep.LastActionAt.UTC().Format(time.RFC3339)
	}
	if !rep.CooldownUntil.IsZero() {
		r.CooldownUntil = rep.CooldownUntil.UTC().Format(time.RFC3339)
	}
	if !rep.NextEligibleAt.IsZero() {
		r.NextEligibleAt = rep.NextEligibleAt.UTC().Format(time.RFC3339)
	}
	if rep.MaxActionsWindow > 0 {
		r.MaxActionsWindow = rep.MaxActionsWindow.String()
	}
	return r
}

func processToWeb(p process.Process) web.Process {
	return web.Process{
		PID:         p.PID,
		PPID:        p.PPID,
		User:        p.User,
		Exe:         p.Exe,
		ExeResolved: p.ExeOK,
		Role:        p.Role,
		Source:      p.Source,
		Cmdline:     p.Cmdline,
	}
}

// procMetricReader is the subset of metrics.OSReader the process table needs;
// injectable so aggregation is testable without real /proc.
type procMetricReader interface {
	ProcessRSS(pid int) (uint64, bool)
	ProcessIO(pid int) (read, write uint64, ok bool)
	ProcessFDs(pid int) (uint64, bool)
	ProcessThreads(pid int) (uint64, bool)
}

// aggregateProcesses builds the per-process rows and the service total. Because
// procs is the whole discovered tree (matched processes plus their children),
// the total reflects the service's workers and helpers, not just its main
// process. The total is nil when there are no processes.
func aggregateProcesses(procs []process.Process, r procMetricReader) ([]web.Process, *web.ProcessTotals) {
	if len(procs) == 0 {
		return nil, nil
	}
	out := make([]web.Process, 0, len(procs))
	totals := web.ProcessTotals{Count: len(procs)}
	for _, p := range procs {
		wp := processToWeb(p)
		if rss, ok := r.ProcessRSS(p.PID); ok {
			wp.RSS = int64(rss)
			totals.RSS += int64(rss)
		}
		if rd, wr, ok := r.ProcessIO(p.PID); ok {
			wp.IORead, wp.IOWrite = int64(rd), int64(wr)
			totals.IORead += int64(rd)
			totals.IOWrite += int64(wr)
		}
		if n, ok := r.ProcessFDs(p.PID); ok {
			wp.FDs = int64(n)
			totals.FDs += int64(n)
		}
		if n, ok := r.ProcessThreads(p.PID); ok {
			wp.Threads = int64(n)
			totals.Threads += int64(n)
		}
		out = append(out, wp)
	}
	return out, &totals
}

// attachLiveCPU folds the per-cycle live CPU sample into a service's detail: the
// per-process single-core rate onto each Process, and the whole-machine /
// single-core aggregates onto ProcessTotals. No-op when no sample exists yet
// (CPU stays unset and the UI shows "measuring"). aggregateProcesses can't
// compute these — a CPU rate needs two samples over time, which the live
// collector tracks; a one-shot /proc read cannot.
func attachLiveCPU(d *web.Detail, live *LiveMetrics, service string) {
	if live == nil {
		return
	}
	sl, ok := live.Get(service)
	if !ok {
		return
	}
	if sl.PerProcCPU != nil {
		for i := range d.Processes {
			if pct, ok := sl.PerProcCPU[d.Processes[i].PID]; ok {
				d.Processes[i].CPU = pct
				d.Processes[i].HasCPU = true
			}
		}
	}
	attachLiveTotals(d.ProcessTotals, live, service)
}

func attachLiveTotals(totals *web.ProcessTotals, live *LiveMetrics, service string) {
	if totals == nil || live == nil {
		return
	}
	sl, ok := live.Get(service)
	if !ok {
		return
	}
	totals.NumCPU = sl.NumCPU
	if sl.CPUReady {
		totals.CPU = sl.CPU
		totals.CPUThread = sl.CPUThread
		totals.HasCPU = true
	}
}

func lockToWeb(lk locks.Lock, service string) web.Lock {
	return lockToWebAt(lk, service, time.Now())
}

func lockToWebAt(lk locks.Lock, service string, now time.Time) web.Lock {
	w := web.Lock{
		Service:     service,
		Name:        lk.Name,
		Reason:      lk.Reason,
		State:       string(lk.State),
		OwnerPID:    lk.OwnerPID,
		OwnerStatus: lockOwnerStatus(lk),
		StaleReason: lk.StaleReason,
		Releaseable: lk.State == locks.StateExpired || lk.State == locks.StateStale,
	}
	if lk.State == locks.StateActive {
		w.BlockedActions = serviceOperationActionList()
	}
	if !lk.CreatedAt.IsZero() {
		w.CreatedAt = lk.CreatedAt.UTC().Format(time.RFC3339)
		if now.After(lk.CreatedAt) {
			w.CreatedAgeSeconds = int64(now.Sub(lk.CreatedAt).Seconds())
		}
	}
	if !lk.ExpiresAt.IsZero() {
		w.ExpiresAt = lk.ExpiresAt.UTC().Format(time.RFC3339)
		if lk.ExpiresAt.After(now) {
			w.TTLRemainingSeconds = int64(lk.ExpiresAt.Sub(now).Seconds())
		}
	}
	return w
}

func lockOwnerStatus(lk locks.Lock) string {
	if lk.OwnerPID <= 0 {
		return "none"
	}
	switch lk.State {
	case locks.StateActive:
		return "live"
	case locks.StateStale:
		return "stale"
	case locks.StateExpired:
		return "expired"
	default:
		return string(lk.State)
	}
}

// Series returns a service's SLA availability series over the window.
func (b *WebBackend) Series(_ context.Context, name string, since time.Duration) ([]web.SeriesPoint, bool) {
	e := b.entries[name]
	if e == nil {
		return nil, false
	}
	if b.sla == nil {
		return []web.SeriesPoint{}, true
	}
	now := time.Now()
	pts, err := b.sla.SLASeries(name, now.Add(-since), now)
	if err != nil {
		return []web.SeriesPoint{}, true
	}
	out := make([]web.SeriesPoint, 0, len(pts))
	for _, p := range pts {
		sp := web.SeriesPoint{Start: p.Start.Format(time.RFC3339), Up: p.Up, Total: p.Total}
		if p.Total > 0 {
			ratio := float64(p.Up) / float64(p.Total)
			sp.Ratio = &ratio
		}
		out = append(out, sp)
	}
	return out, true
}

// Diagnostics runs configuration and host diagnostics and returns the findings.
func (b *WebBackend) Diagnostics(_ context.Context) []web.Finding {
	r := diag.Diagnose(b.cfg, b.diagStore, b.host)
	out := make([]web.Finding, 0, len(r.Findings)+1)
	for _, f := range r.Findings {
		out = append(out, web.Finding{Level: string(f.Level), Scope: f.Scope, Message: f.Message})
	}
	if b.opGate != nil {
		inUse, total := b.opGate.Usage()
		out = append(out, operationSlotFindings(inUse, total)...)
	}
	out = append(out, lockScanFindings(b.cfg)...)
	return out
}

// CleanDiagnostics removes stale runtime control state for services and
// watches that are no longer configured.
func (b *WebBackend) CleanDiagnostics(_ context.Context) web.DiagnosticCleanResult {
	if b.diagCleaner == nil {
		return web.DiagnosticCleanResult{OK: false, Message: "state database is unavailable"}
	}
	result, err := b.diagCleaner.PruneUnconfiguredControlStates(diag.ConfiguredStoredNames(b.cfg))
	if err != nil {
		return web.DiagnosticCleanResult{OK: false, Message: err.Error()}
	}
	if len(result.Services) == 0 {
		return web.DiagnosticCleanResult{OK: true, Message: "no unconfigured control state found"}
	}
	return web.DiagnosticCleanResult{
		OK:       true,
		Message:  fmt.Sprintf("cleared control state for %d unconfigured target(s)", len(result.Services)),
		Pruned:   result.Rows,
		Services: result.Services,
	}
}

func lockScanFindings(cfg *config.Config) []web.Finding {
	if cfg == nil {
		return nil
	}
	warnings, err := locksScanner(cfg).ScanDir()
	var out []web.Finding
	if err != nil {
		out = append(out, web.Finding{Level: "error", Scope: "locks", Message: err.Error()})
	}
	for _, w := range warnings {
		out = append(out, web.Finding{Level: "warning", Scope: "locks", Message: w})
	}
	return out
}

func operationSlotFindings(inUse, total int) []web.Finding {
	if total <= 0 || inUse <= 0 {
		return nil
	}
	if inUse >= total {
		return []web.Finding{{
			Level:   "warning",
			Scope:   "operations",
			Message: fmt.Sprintf("operation slots saturated (%d/%d in use)", inUse, total),
		}}
	}
	return []web.Finding{{
		Level:   "info",
		Scope:   "operations",
		Message: fmt.Sprintf("operation slots %d/%d in use", inUse, total),
	}}
}

// Operations returns current operation-slot usage.
func (b *WebBackend) Operations(_ context.Context) web.OperationSlots {
	if b.opGate == nil {
		return web.OperationSlots{}
	}
	inUse, total := b.opGate.Usage()
	return web.OperationSlots{InUse: inUse, Total: total}
}

// Metrics returns a check's measured metric series over the window.
func (b *WebBackend) Metrics(_ context.Context, name, check, metric string, since time.Duration) (web.MetricSeries, bool) {
	e := b.entries[name]
	if e == nil || e.disabled {
		return web.MetricSeries{}, false
	}
	typ, ok := e.checkTypes[check]
	if !ok {
		return web.MetricSeries{}, false
	}
	now := time.Now()

	// metric == "" is the built-in latency series; otherwise a named metric the
	// check type declares (e.g. hdparm read/cached).
	if metric == "" {
		if !measuredCheckTypes[typ] {
			return web.MetricSeries{}, false
		}
		out := web.MetricSeries{Check: check, Since: since.String(), Unit: "ms"}
		if b.measure == nil {
			return out, true
		}
		if stat, err := b.measure.MeasurementSummary(name, check, since, now); err == nil {
			out.Summary = web.MetricSummary{Count: stat.Count, Avg: stat.Avg, Min: stat.Min, Max: stat.Max}
		}
		points, err := b.measure.MeasurementSeries(name, check, now.Add(-since), now)
		if err == nil {
			out.Points = measurementPoints(points)
		}
		return out, true
	}

	unit := checks.GraphMetricUnit(typ, metric)
	if unit == "" {
		return web.MetricSeries{}, false // not a declared metric for this check type
	}
	out := web.MetricSeries{Check: check, Metric: metric, Since: since.String(), Unit: unit}
	if b.measure == nil {
		return out, true
	}
	if stat, err := b.measure.MetricSummary(name, check, metric, since, now); err == nil {
		out.Summary = web.MetricSummary{Count: stat.Count, Avg: stat.Avg, Min: stat.Min, Max: stat.Max}
	}
	if points, err := b.measure.MetricSeries(name, check, metric, now.Add(-since), now); err == nil {
		out.Points = measurementPoints(points)
	}
	return out, true
}

// measurementPoints converts store points to the web shape.
func measurementPoints(points []state.MeasurementPoint) []web.MetricPoint {
	out := make([]web.MetricPoint, 0, len(points))
	for _, p := range points {
		out = append(out, web.MetricPoint{Start: p.Start.Format(time.RFC3339), N: p.N, Avg: p.Avg, Min: p.Min, Max: p.Max})
	}
	return out
}

// Events returns the most recent events, newest first.
func (b *WebBackend) Events(_ context.Context, limit int) []web.Event {
	if b.events == nil {
		return nil
	}
	return toWebEvents(b.events.Recent("", limit))
}

// ServiceEvents returns one service's recent events.
func (b *WebBackend) ServiceEvents(_ context.Context, name string, limit int) ([]web.Event, bool) {
	if _, ok := b.entries[name]; !ok {
		return nil, false
	}
	if b.events == nil {
		return nil, true
	}
	return toWebEvents(b.events.Recent(name, limit)), true
}

// PruneEvents removes events older than 'before' (all if zero) from the live log.
func (b *WebBackend) PruneEvents(_ context.Context, before time.Time) int {
	if b.events == nil {
		return 0
	}
	return b.events.Prune(before)
}

func toWebEvents(events []LoggedEvent) []web.Event {
	out := make([]web.Event, 0, len(events))
	for _, e := range events {
		out = append(out, loggedEventToWeb(e))
	}
	return out
}

func loggedEventToWeb(e LoggedEvent) web.Event {
	return web.Event{
		Time:    e.Time.Format(time.RFC3339),
		Service: e.Service,
		Watch:   e.Watch,
		Kind:    e.Kind,
		Rule:    e.Rule,
		Action:  e.Action,
		Status:  e.Status,
		Message: e.Message,
	}
}

func (b *WebBackend) lastServiceEvent(name string) *web.Event {
	if b.events == nil {
		return nil
	}
	ev, ok := b.events.LastService(name)
	if !ok {
		return nil
	}
	webEv := loggedEventToWeb(ev)
	return &webEv
}

// Operate runs a start/stop/restart/reload/resume action on a service.
func (b *WebBackend) Operate(ctx context.Context, name, action string, opts web.OperateOpts) web.ActionResult {
	e := b.entries[name]
	if e == nil {
		msg := "unknown service " + name
		if b.emit != nil {
			b.emit(Event{Service: name, Kind: "error", Action: action, Message: msg})
		}
		return web.ActionResult{OK: false, Message: msg}
	}
	if e.disabled {
		msg := "service " + name + " is disabled in configuration"
		if b.emit != nil {
			b.emit(Event{Service: name, Kind: "error", Action: action, Message: msg})
		}
		return web.ActionResult{OK: false, Message: msg}
	}

	var r operation.Result
	if opts.NoCascade || action == "reload" || action == "resume" || len(e.alsoApply) == 0 {
		r = b.operationResult(ctx, name, action)
	} else {
		lookup := func(svc string) []string {
			ent := b.entries[svc]
			if ent == nil {
				return nil
			}
			return ent.alsoApply
		}
		c := cascader{
			op:     b.operationResult,
			lookup: lookup,
			emit:   b.emit,
			sleep:  time.Sleep,
		}
		r = c.run(ctx, name, action)
	}
	return webActionResultFrom(r, name, action)
}

func webActionResultFrom(r operation.Result, name, action string) web.ActionResult {
	if r.Action == "" && action != "" {
		r.Action = action
	}
	if r.Service == "" {
		r.Service = name
	}
	msg := r.Message
	if msg == "" {
		msg = string(r.Status)
	}
	return web.ActionResult{OK: r.OK(), Message: msg}
}

func (b *WebBackend) operationResult(ctx context.Context, name, action string) operation.Result {
	e := b.entries[name]
	if e == nil {
		return operation.Result{Service: name, Action: action, Status: operation.ResultFailed, Message: "unknown service " + name}
	}
	if e.disabled {
		return operation.Result{Service: name, Action: action, Status: operation.ResultFailed, Message: "service " + name + " is disabled in configuration"}
	}
	timeout := b.operationTimeout
	if timeout <= 0 {
		timeout = operation.DefaultOperationTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	run := func(ctx context.Context) operation.Result {
		return e.engine.Do(ctx, action)
	}
	var r operation.Result
	if b.opGate != nil {
		r = b.opGate.Run(ctx, name, action, run)
	} else {
		r = run(ctx)
	}
	if r.Action == "" && action != "" {
		r.Action = action
	}
	if r.Service == "" {
		r.Service = name
	}
	e.invalidateStatusCache()
	return r
}

// CompactState prunes old persisted history and vacuums the state database.
func (b *WebBackend) CompactState(ctx context.Context, before time.Time) web.StateCompactResult {
	maint, ok := b.store.(stateMaintainer)
	if !ok || maint == nil {
		return web.StateCompactResult{OK: false, Message: "state store unavailable"}
	}
	now := b.webNow()
	if before.IsZero() {
		before = now.Add(-state.DefaultHistoryRetention)
	}
	timeout := b.operationTimeout
	if timeout <= 0 {
		timeout = b.defaultTimeout
	}
	if timeout <= 0 {
		timeout = operation.DefaultOperationTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := maint.PruneHistory(before)
	if err != nil {
		return web.StateCompactResult{OK: false, Message: "prune state history: " + err.Error()}
	}
	if err := maint.Compact(ctx); err != nil {
		return web.StateCompactResult{OK: false, Message: "compact state database: " + err.Error()}
	}
	return web.StateCompactResult{
		OK:             true,
		Pruned:         result.Rows,
		Before:         before.UTC().Format(time.RFC3339),
		SLA:            result.SLA,
		Measurements:   result.Measurements,
		Metrics:        result.Metrics,
		DaemonMetrics:  result.DaemonMetrics,
		ServiceMetrics: result.ServiceMetrics,
		Events:         result.Events,
		Vacuum:         true,
	}
}

// ExpandWatch runs a configured storage watch's then.expand action on demand.
func (b *WebBackend) ExpandWatch(ctx context.Context, name string) web.ActionResult {
	w := b.watches[name]
	if w == nil {
		msg := fmt.Sprintf("unknown watch %q", name)
		b.emitWatchExpandEvent(name, "expand-failed", "failed", msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	if w.disabled {
		msg := fmt.Sprintf("watch %q is disabled in configuration", name)
		b.emitWatchExpandEvent(name, "expand-skipped", "blocked", msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	if !isStorageCheckType(w.checkType) {
		msg := fmt.Sprintf("watch %q is %q, not storage", name, w.checkType)
		b.emitWatchExpandEvent(name, "expand-skipped", "blocked", msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	if w.expand == nil {
		msg := fmt.Sprintf("watch %q has no then.expand action configured", name)
		b.emitWatchExpandEvent(name, "expand-skipped", "blocked", msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	path := cfgval.AsString(w.check["path"])
	if path == "" {
		msg := fmt.Sprintf("watch %q storage check has no path", name)
		b.emitWatchExpandEvent(name, "expand-failed", "failed", msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	expander := b.expander
	if expander == nil {
		msg := "volume expander is unavailable"
		b.emitWatchExpandEvent(name, "expand-failed", "failed", msg)
		return web.ActionResult{OK: false, Message: msg}
	}

	timeout := b.operationTimeout
	if timeout <= 0 {
		timeout = operation.DefaultOperationTimeout
	}
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := expander.ExpandPath(opCtx, path, w.expand.By)
	if err != nil {
		msg := err.Error()
		b.emitWatchExpandEvent(name, "expand-failed", "failed", msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	msg := expandSuccessMessage(path, res)
	b.emitWatchExpandEvent(name, "expand", "ok", msg)
	return web.ActionResult{OK: true, Message: msg}
}

// SetMonitored enables or disables monitoring for a service.
func (b *WebBackend) SetMonitored(_ context.Context, name string, monitored bool) error {
	action := "monitor"
	if !monitored {
		action = "unmonitor"
	}
	if _, ok := b.entries[name]; !ok {
		msg := fmt.Sprintf("unknown service %q", name)
		b.emitMonitorEvent(name, action, "error", "", msg)
		return fmt.Errorf("%s", msg)
	}
	if b.store == nil {
		msg := "monitoring state is unavailable"
		b.emitMonitorEvent(name, action, "error", "", msg)
		return fmt.Errorf("%s", msg)
	}
	priorActive, found, err := b.store.Active(name)
	if err != nil {
		msg := fmt.Sprintf("%s failed: %v", action, err)
		b.emitMonitorEvent(name, action, "error", "", msg)
		return fmt.Errorf("%s", msg)
	}
	if err := b.store.SetActive(name, monitored, state.SourceWeb); err != nil {
		msg := fmt.Sprintf("%s failed: %v", action, err)
		b.emitMonitorEvent(name, action, "error", "", msg)
		return fmt.Errorf("%s", msg)
	}
	if found && priorActive == monitored {
		msg := "already monitored"
		if !monitored {
			msg = "already paused"
		}
		b.emitMonitorEvent(name, action, "suppressed", "", msg)
		return nil
	}
	msg := "monitoring resumed"
	if !monitored {
		msg = "monitoring paused"
	}
	b.emitMonitorEvent(name, action, "action", "ok", msg)
	return nil
}

// SetWatchMonitored enables or disables monitoring for a host watch.
func (b *WebBackend) SetWatchMonitored(_ context.Context, name string, monitored bool) error {
	action := "monitor"
	if !monitored {
		action = "unmonitor"
	}
	if _, ok := b.watches[name]; !ok {
		msg := fmt.Sprintf("unknown watch %q", name)
		b.emitWatchMonitorEvent(name, action, "error", "", msg)
		return fmt.Errorf("%s", msg)
	}
	if b.store == nil {
		msg := "monitoring state is unavailable"
		b.emitWatchMonitorEvent(name, action, "error", "", msg)
		return fmt.Errorf("%s", msg)
	}
	key := watchMonitorKey(name)
	priorActive, found, err := b.store.Active(key)
	if err != nil {
		msg := fmt.Sprintf("%s failed: %v", action, err)
		b.emitWatchMonitorEvent(name, action, "error", "", msg)
		return fmt.Errorf("%s", msg)
	}
	if err := b.store.SetActive(key, monitored, state.SourceWeb); err != nil {
		msg := fmt.Sprintf("%s failed: %v", action, err)
		b.emitWatchMonitorEvent(name, action, "error", "", msg)
		return fmt.Errorf("%s", msg)
	}
	if found && priorActive == monitored {
		msg := "already monitored"
		if !monitored {
			msg = "already paused"
		}
		b.emitWatchMonitorEvent(name, action, "suppressed", "", msg)
		return nil
	}
	msg := "monitoring resumed"
	if !monitored {
		msg = "monitoring paused"
	}
	b.emitWatchMonitorEvent(name, action, "action", "ok", msg)
	return nil
}

func (b *WebBackend) emitMonitorEvent(service, action, kind, status, message string) {
	if b.emit == nil {
		return
	}
	b.emit(Event{
		Service: service,
		Kind:    kind,
		Action:  action,
		Status:  status,
		Message: message,
	})
}

func (b *WebBackend) emitWatchMonitorEvent(watch, action, kind, status, message string) {
	if b.emit == nil {
		return
	}
	b.emit(Event{
		Watch:   watch,
		Kind:    kind,
		Action:  action,
		Status:  status,
		Message: message,
	})
}

func (b *WebBackend) emitWatchExpandEvent(watch, kind, status, message string) {
	if b.emit == nil {
		return
	}
	b.emit(Event{
		Watch:   watch,
		Kind:    kind,
		Action:  "expand",
		Status:  status,
		Message: message,
	})
}
