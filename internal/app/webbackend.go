package app

import (
	"context"
	"fmt"
	"maps"
	"os"
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
	"sermo/internal/execx"
	"sermo/internal/locks"
	"sermo/internal/metrics"
	"sermo/internal/notify"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
	"sermo/internal/units"
	"sermo/internal/web"
)

const (
	// applicationsCacheTTL keeps the web app inventory from running version and
	// health commands on ordinary dashboard refreshes. Apps change rarely, and
	// app watches use a 5m default cadence for the same probes.
	applicationsCacheTTL = 5 * time.Minute
	// appInspectionParallelism bounds concurrent version/health probes for
	// /api/applications so a cold dashboard load is faster without spawning one
	// command per catalog app at once.
	appInspectionParallelism = 4
	// serviceStatusCacheTTL bounds how often the web list re-queries systemd/OpenRC.
	// The dashboard refreshes every 30s by default, so keep status warm across
	// ordinary refreshes instead of running one init status probe per service.
	serviceStatusCacheTTL = 2 * time.Minute
	// diskIORateMinWindow is the shortest elapsed span disk I/O rates may be
	// computed over. The delta baseline is shared by every dashboard viewer, so
	// without a floor two tabs polling moments apart would re-base the deltas
	// over a near-zero window and report garbage rates; polls arriving inside
	// the window keep the previous baseline and serve the last computed rates.
	diskIORateMinWindow = time.Second
	// serviceReloadCapabilityTimeout bounds init metadata checks used only to
	// decide whether the dashboard should offer a per-service reload action.
	serviceReloadCapabilityTimeout = 2 * time.Second
	// slaTimelineCacheTTL caches SLA timeline strips for detail/expansion views.
	slaTimelineCacheTTL = 45 * time.Second
	// activitySummaryEventScanLimit bounds the recent event scan used for the
	// dashboard rollup; event list endpoints keep their own request limits.
	activitySummaryEventScanLimit = 500

	procUptimePath         = "/proc/uptime"
	procUptimeValueIndex   = 0
	procUptimeFloatBits    = 64
	processPIDListLimit    = 20
	osReleasePrettyNameKey = "PRETTY_NAME="
	osReleaseValueTrimSet  = `"'`
)

const (
	remediationStatePaused   = TargetStatePaused
	remediationStatePending  = "pending"
	remediationStateEligible = "eligible"
	remediationStateBlocked  = "blocked"
)

const (
	backendStatusError         = "error"
	watchConditionFieldGrowth  = "growth"
	watchMetricFieldSeparator  = "."
	watchMetricSuffixChange    = "change"
	watchMetricSuffixDelta     = checks.CheckKeyDelta
	watchMetricSuffixExpect    = checks.CheckKeyExpect
	watchMetricSuffixOn        = checks.CheckKeyOn
	watchMetricSuffixThreshold = "threshold"
	watchReadingFieldError     = "error"
	watchReadingFieldCPUTicks  = "cpu_ticks"
	watchReadingFieldMatches   = "matches"
	watchReadingFieldProcess   = checks.CheckTypeProcess
	watchReadingFieldResult    = checks.DataKeyResult
	watchReadingFieldRSS       = "rss"
	watchReadingFieldSample    = "sample"
	watchCategoryFallback      = config.WatchCategoryWatch
	watchReadingFieldState     = checks.CheckKeyState
	watchReadingFieldUser      = checks.CheckKeyUser
	watchReadingStateActive    = string(servicemgr.StatusActive)
	watchReadingStateBaseline  = "baseline"
	watchReadingStateMissing   = "missing"
	lockOwnerStatusLive        = "live"
	lockReleaseDefaultRule     = "default"
	unknownServiceMessage      = "unknown service "
	unknownServiceMessageFmt   = unknownServiceMessage + "%q"
	watchReadingValueNone      = "none"
	watchReadingValueUnknown   = checks.NetStateUnknown
)

// webEntry is one service's web-backend record.
type webEntry struct {
	displayName       string
	category          string
	unit              string
	backend           string
	interval          time.Duration // resolved per-service cycle cadence (own interval or engine default)
	dryRun            bool
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
	canReload         bool
	disabled          bool // true when the service had `enabled: false` (still listed for visibility)

	statusMu     sync.Mutex
	cachedStatus string
	statusAt     time.Time
}

// webWatch is a configured host watch for UI visibility (services may be 0).
type webWatch struct {
	name          string
	displayName   string
	category      string
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
	// serviceScoped marks a watch declared inside a service's `watches:` section
	// (named "<service>:<watch>"). It is listed and controllable like any watch,
	// but its live meter/readings are omitted: its checks are scoped to the
	// service's PID tree, which the host-scoped web live-view path does not model.
	serviceScoped bool
}

// webNotifier is a configured notification target (used by watches).
type webNotifier struct {
	name    string
	typ     string
	enabled bool
	summary string
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
	order             []string
	entries           map[string]*webEntry
	watchOrder        []string
	watches           map[string]*webWatch
	notifierOrder     []string
	notifiers         map[string]*webNotifier
	store             MonitorStore
	operationSettling OperationSettlingStore
	snapshots         *Snapshots
	watchSnapshots    *WatchSnapshots
	settling          *Settling
	observability     *ObservabilityRegistry
	sla               SLAReader
	events            *EventLog
	remediation       *RemediationRegistry
	ruleWindows       *RuleWindowRegistry
	cfg               *config.Config
	hostType          *web.HostTypeInfo
	measure           MeasurementReader
	collector         *metrics.Collector
	daemonMetrics     *daemonMetricSampler
	serviceMetrics    *ServiceMetricSampler
	live              *LiveMetrics
	storageUsage      checks.StorageUsageFunc
	mountSampler      checks.MountSamplerFunc
	openFilesSampler  func(mounts []checks.Mount) map[string]int64
	netSampler        checks.NetSamplerFunc
	pingSampler       checks.PingSamplerFunc
	oomSampler        checks.OomSamplerFunc
	fdsSampler        checks.FdsSamplerFunc
	pidsSampler       checks.PidsSamplerFunc
	pressureSampler   checks.PressureSamplerFunc
	conntrackSampler  checks.ConntrackSamplerFunc
	entropySampler    checks.EntropySamplerFunc
	zombieSampler     checks.ZombieSamplerFunc
	procSampler       ProcSampler
	diskIOSampler     checks.DiskIOSamplerFunc
	sensorSampler     checks.SensorSamplerFunc
	raidSampler       checks.RaidSamplerFunc
	edacSampler       checks.EdacSamplerFunc
	routeSampler      checks.RouteSamplerFunc
	firewallSampler   checks.FirewallRulesSamplerFunc
	execRunner        execx.Runner
	expander          VolumeExpander
	userLookup        *process.UserLookup
	mountUsers        func(string) ([]process.Process, error)
	mountSignaler     process.Signaler
	mountAlerter      MountUserAlerter
	emit              func(Event)
	opGate            *OpGate
	defaultTimeout    time.Duration
	operationTimeout  time.Duration
	now               func() time.Time

	diskIOMu    sync.Mutex
	diskIOState map[string]webDiskIOState

	applicationsMu      sync.Mutex
	applicationsAt      time.Time
	applicationsCache   []web.Application
	applicationsRefresh chan struct{} // non-nil while a scan is rebuilding the cache; closed when it finishes
	applicationsList    func(context.Context) []web.Application

	slaCacheMu sync.Mutex
	slaCache   map[slaCacheKey]cachedSLATimelines

	openFilesMu      sync.Mutex
	openFilesTally   map[string]int64
	openFilesTallyAt time.Time

	mountUsageMu     sync.Mutex
	mountUsageAt     time.Time
	mountUsage       map[string][]process.Process
	mountUsageErrors map[string]string
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
	primed   bool
	at       time.Time
	sample   checks.DiskIOSample
	rates    checks.DiskIORates
	hasRates bool
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
	operationSettling := deps.OperationSettling
	if operationSettling == nil {
		if store, ok := deps.Monitor.(OperationSettlingStore); ok {
			operationSettling = store
		}
	}
	wb := &WebBackend{
		entries:           map[string]*webEntry{},
		watches:           map[string]*webWatch{},
		notifiers:         map[string]*webNotifier{},
		store:             deps.Monitor,
		operationSettling: operationSettling,
		snapshots:         deps.Snapshots,
		watchSnapshots:    deps.WatchSnapshots,
		settling:          deps.Settling,
		observability:     deps.Observability,
		events:            deps.Events,
		remediation:       deps.Remediation,
		ruleWindows:       deps.RuleWindows,
		cfg:               cfg,
		hostType:          hostTypeInfo(),
		collector:         deps.Collector,
		daemonMetrics:     newDaemonMetricSampler(deps.Collector, deps.Now, deps.DaemonMetrics),
		serviceMetrics:    deps.ServiceMetrics,
		live:              deps.Live,
		storageUsage:      deps.StorageUsage,
		mountSampler:      deps.MountSampler,
		openFilesSampler:  deps.OpenFilesByMount,
		netSampler:        deps.NetSampler,
		pingSampler:       deps.PingSampler,
		oomSampler:        deps.OomSampler,
		fdsSampler:        deps.FdsSampler,
		pidsSampler:       deps.PidsSampler,
		pressureSampler:   deps.PressureSampler,
		conntrackSampler:  deps.ConntrackSampler,
		entropySampler:    deps.EntropySampler,
		zombieSampler:     deps.ZombieSampler,
		procSampler:       deps.ProcSampler,
		diskIOSampler:     deps.DiskIOSampler,
		sensorSampler:     deps.SensorSampler,
		raidSampler:       deps.RaidSampler,
		edacSampler:       deps.EdacSampler,
		routeSampler:      deps.RouteSampler,
		firewallSampler:   deps.FirewallRulesSampler,
		execRunner:        deps.ExecxRunner,
		expander:          configuredVolumeExpander(deps),
		userLookup:        deps.UserLookup,
		mountUsers:        deps.MountDiscoverUsers,
		mountSignaler:     deps.MountSignaler,
		mountAlerter:      deps.MountUserAlerter,
		emit:              deps.Emit,
		opGate:            deps.OpGate,
		defaultTimeout:    deps.DefaultTimeout,
		operationTimeout:  deps.OperationTimeout,
		now:               deps.Now,
		slaCache:          map[slaCacheKey]cachedSLATimelines{},
	}
	if wb.serviceMetrics == nil {
		wb.serviceMetrics = NewServiceMetricSampler()
	}
	wb.sla, _ = deps.SLA.(SLAReader)
	wb.measure, _ = deps.SLA.(MeasurementReader)
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
		iv := cfgval.Duration(resolved.Tree[config.EntryKeyInterval])
		if iv <= 0 {
			iv = config.EngineInterval(cfg, config.DefaultEngineInterval)
		}
		entry := &webEntry{
			displayName:    config.DisplayName(resolved.Tree, name),
			category:       config.CategoryLabel(resolved.Tree, config.CategoryService),
			unit:           target.Unit,
			backend:        string(target.Backend),
			interval:       iv,
			dryRun:         config.DryRun(resolved.Tree),
			policyCooldown: rules.ParsePolicy(resolved.Tree).Cooldown,
			alsoApply:      config.CascadeTargets(resolved.Tree),
		}
		if disabled {
			entry.disabled = true
			entry.noResidentProcess = noResidentProcess(resolved.Tree)
		} else {
			serviceDeps := deps
			serviceDeps.Backend = target.Backend
			serviceDeps.Manager = target.Manager
			serviceDeps.BackendPIDs = target.BackendPIDs
			engine, checkDeps, discoverer := serviceRuntime(name, target.Unit, resolved.Tree, serviceDeps, map[string]string{}, operationEventEmitter(deps.Emit))
			selectors, processWarnings := serviceProcessSelectors(context.Background(), resolved.Tree, serviceDeps, target.Unit)
			names, types := checkCatalog(resolved.Tree)
			entry.noResidentProcess = serviceNoResidentProcess(resolved.Tree, selectors, serviceBackendPIDs(serviceDeps, target.Unit))
			entry.engine = engine
			entry.status = checkDeps.Status
			entry.checkNames = names
			entry.checkTypes = types
			entry.discoverer = discoverer
			entry.selectors = selectors
			entry.processWarnings = processWarnings
			reloadCtx, cancel := context.WithTimeout(context.Background(), serviceReloadCapabilityTimeout)
			canReload, reloadErr := operation.ReloadSupported(reloadCtx, resolved.Tree, target.Manager, target.Unit)
			cancel()
			entry.canReload = canReload
			if reloadErr != nil {
				warnings = append(warnings, "service "+name+": reload support unavailable: "+reloadErr.Error())
			}
		}
		wb.entries[name] = entry
		wb.order = append(wb.order, name)

		// Surface this service's embedded watches ("<service>:<watch>") so they are
		// listed and controllable (monitor/unmonitor) in the web UI like host
		// watches. Their live meter is omitted — the checks are scoped to the
		// service PID tree, which the host-scoped web live-view path does not model.
		if watchesSection, ok := resolved.Tree[config.SectionWatches].(map[string]any); ok {
			for _, wn := range slices.Sorted(maps.Keys(watchesSection)) {
				wentry, ok := watchesSection[wn].(map[string]any)
				if !ok || reservedServiceWatchName(wn) || unsupportedServiceWatchType(wentry) != "" {
					continue
				}
				full := name + ":" + wn
				ww, _ := newWebWatch(full, wentry, deps.GlobalNotify, iv, true)
				if disabled {
					ww.disabled = true
				}
				wb.watches[full] = ww
				wb.watchOrder = append(wb.watchOrder, full)
			}
		}
	}

	// Also surface host watches in the web UI (including disabled ones). This is
	// important when services=0 but watches=N (the main dashboard would otherwise
	// be empty). We read the raw global watches section (same source BuildWatches
	// uses) so listing is independent of whether the watch runner is active.
	if raw, _ := cfg.ResolveWatches(); len(raw) > 0 {
		for _, name := range slices.Sorted(maps.Keys(raw)) {
			entry, _ := raw[name].(map[string]any)
			ww, warn := newWebWatch(name, entry, deps.GlobalNotify, config.DefaultEngineInterval, false)
			if warn != "" {
				warnings = append(warnings, "watch "+name+": "+warn)
			}
			wb.watches[name] = ww
			wb.watchOrder = append(wb.watchOrder, name)
		}
	}

	// Surface configured notifiers (useful to know what watches can notify to).
	if raw := cfg.Notifiers(); len(raw) > 0 {
		for _, name := range slices.Sorted(maps.Keys(raw)) {
			entry, _ := raw[name].(map[string]any)
			typ := cfgval.AsString(entry[notify.KeyType])
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

// newWebWatch builds the web-listing model for one watch entry, shared by host
// watches and service-embedded watches. defaultInterval is used when the entry
// sets no interval; serviceScoped marks it as a service watch (live view
// omitted). warn is a non-empty message when the expand action is malformed.
func newWebWatch(name string, entry map[string]any, globalNotify []string, defaultInterval time.Duration, serviceScoped bool) (*webWatch, string) {
	ctype := ""
	if ce, ok := entry[config.WatchKeyCheck].(map[string]any); ok {
		ctype = cfgval.AsString(ce[checks.CheckKeyType])
	}
	iv := cfgval.Duration(entry[config.EntryKeyInterval])
	if iv <= 0 {
		iv = defaultInterval
	}
	hasHook := false
	var hookCommand []string
	var notifierNames []string
	var expand *ExpandSpec
	var warn string
	if then, ok := entry[rules.RuleFieldThen].(map[string]any); ok {
		if h, ok := then[config.WatchThenKeyHook].(map[string]any); ok && len(h) > 0 {
			if cmd := h[config.WatchHookKeyCommand]; cmd != nil {
				hookCommand = cfgval.StringArray(cmd)
				hasHook = len(hookCommand) > 0
			}
		}
		notifierNames = effectiveNotify(cfgval.StringList(then[rules.RuleFieldNotify]), globalNotify)
		if parsed, err := parseExpand(then, ctype); err != nil {
			warn = err.Error()
		} else {
			expand = parsed
		}
	}
	return &webWatch{
		name:          name,
		displayName:   config.DisplayName(entry, name),
		category:      config.CategoryLabel(entry, watchCategoryFallback),
		checkType:     ctype,
		interval:      iv,
		disabled:      cfgval.Disabled(entry),
		monitorMode:   config.MonitorMode(entry),
		fireOnFail:    checks.IsHealthType(ctype),
		hasHook:       hasHook,
		hookCommand:   hookCommand,
		notifiers:     notifierNames,
		dryRun:        config.DryRun(entry),
		notifierCount: len(notifierNames),
		check:         checkMap(entry),
		metrics:       metricsMap(entry),
		expand:        expand,
		serviceScoped: serviceScoped,
	}, warn
}

// checkCatalog returns a service's check names (sorted) and their types, from the
// resolved `checks` section.
func checkCatalog(tree map[string]any) ([]string, map[string]string) {
	section, ok := tree[config.SectionChecks].(map[string]any)
	if !ok {
		return nil, nil
	}
	types := make(map[string]string, len(section))
	names := make([]string, 0, len(section))
	for name, raw := range section {
		typ := ""
		if m, ok := raw.(map[string]any); ok {
			typ, _ = m[checks.CheckKeyType].(string)
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
		Name:              name,
		DisplayName:       e.displayName,
		Category:          e.category,
		Backend:           e.backend,
		Unit:              e.unit,
		Enabled:           !e.disabled,
		DryRun:            e.dryRun,
		Monitored:         true, // no recorded state defaults to monitored
		CanReload:         e.canReload,
		NoResidentProcess: e.noResidentProcess,
	}
	if e.interval > 0 {
		svc.Interval = formatInterval(e.interval)
	}
	if e.policyCooldown > 0 {
		svc.PolicyCooldown = formatInterval(e.policyCooldown)
	}
	svc.LastEvent = lastEvent
	if e.disabled {
		svc.Status = TargetStateDisabled
		svc.State = ServiceState(false, false, svc.Status, "", true, false)
		svc.Monitored = false
		svc.CheckHealth = ""
		svc.RemediationState = TargetStateDisabled
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
	observed := (b.settling == nil || b.settling.Observed(SettlingServiceKey(name))) && !b.operationSettlingPending(name)
	svc.ObservabilityReady, svc.ObservabilityMissing = b.serviceObservability(name, e, svc.Status, svc.CheckHealth, svc.Monitored, observed)
	svc.State = ServiceState(svc.Enabled, svc.Monitored, svc.Status, svc.CheckHealth, observed, svc.ObservabilityReady)
	if len(e.alsoApply) > 0 {
		svc.AlsoApply = slices.Clone(e.alsoApply)
	}
	b.decorateServiceRuntime(name, e, &svc)
	return svc
}

func (b *WebBackend) serviceObservability(name string, e *webEntry, status, checkHealth string, monitored, observed bool) (bool, []string) {
	if e == nil || e.disabled {
		return false, nil
	}
	active := strings.EqualFold(status, string(servicemgr.StatusActive))
	if !active || !monitored || !observed {
		if b.observability != nil {
			b.observability.Clear(name)
		}
		if monitored && !observed {
			return false, []string{observabilityMissingStartup}
		}
		return false, nil
	}

	missing := make([]string, 0, 3)
	addMissing := func(label string) {
		if !slices.Contains(missing, label) {
			missing = append(missing, label)
		}
	}
	if len(e.checkNames) > 0 {
		snap := b.snapshots.Get(name)
		for _, check := range e.checkNames {
			if _, ok := snap[check]; !ok {
				addMissing(config.SectionChecks)
				break
			}
		}
		if checkHealth == checkHealthUnknown {
			addMissing(config.SectionChecks)
		}
	}
	if b.observability != nil {
		if _, ready := b.observability.Ready(name); !ready {
			addMissing(observabilityMissingHistory)
		}
		if !e.noResidentProcess && !b.serviceRuntimeObservabilityReady(name, e) {
			addMissing(observabilityMissingRuntime)
		}
	}
	if len(missing) > 0 {
		return false, missing
	}
	return true, nil
}

func (b *WebBackend) serviceRuntimeObservabilityReady(name string, e *webEntry) bool {
	if e == nil || e.noResidentProcess || b.serviceMetrics == nil {
		return true
	}
	cur, at, ok := b.serviceMetrics.LatestWithAt(name)
	if !ok || b.webNow().Sub(at) > runtimePublishMaxAge(e.interval) {
		return false
	}
	return cur.Count > 0 && cur.HasCPU && cur.IOReady
}

func (b *WebBackend) decorateRemediation(name string, svc *web.Service) {
	if svc == nil {
		return
	}
	if !svc.Monitored {
		svc.RemediationState = remediationStatePaused
		return
	}
	if b.remediation == nil {
		svc.RemediationState = remediationStatePending
		return
	}
	rep, ok := b.remediation.Get(name)
	if !ok {
		svc.RemediationState = remediationStatePending
		return
	}
	if rep.Allowed {
		svc.RemediationState = remediationStateEligible
	} else if rep.Reason != "" {
		svc.RemediationState = rep.Reason
	} else {
		svc.RemediationState = remediationStateBlocked
	}
	if !rep.NextEligibleAt.IsZero() {
		svc.NextEligibleAt = rep.NextEligibleAt.UTC().Format(time.RFC3339)
	}
}

func (b *WebBackend) operationSettlingPending(name string) bool {
	if b.operationSettling == nil {
		return false
	}
	rec, found, err := b.operationSettling.OperationSettling(name)
	if err != nil {
		b.emitMonitorEvent(name, eventActionOperationSettling, eventKindError, "", err.Error())
		return false
	}
	if !found {
		return false
	}
	if !rec.UpdatedAt.IsZero() && b.webNow().Sub(rec.UpdatedAt) > operationSettlingMaxAge {
		if err := b.operationSettling.ClearOperationSettling(name); err != nil {
			b.emitMonitorEvent(name, eventActionOperationSettling, eventKindError, "", err.Error())
		}
		return false
	}
	return rec.Phase == state.OperationSettlingRunning || rec.Phase == state.OperationSettlingSettling
}

// lockProcProber answers lock-owner liveness for the web backend's lock views.
// Production uses the real /proc-backed prober; tests substitute a deterministic
// one so lock state does not depend on the host's /proc.
var lockProcProber locks.ProcessProber = locks.OSProcessProber{}

func locksScanner(cfg *config.Config) locks.Scanner {
	s := locks.NewScanner(locks.RuntimeLocksDir(cfg.Global.RuntimeDir()))
	s.Proc = lockProcProber
	return s
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
			n = watchDefaultLockName
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
		return 0, TargetStatePaused
	}
	if len(checkNames) == 0 {
		return 0, ""
	}
	if snap == nil {
		return 0, checkHealthUnknown
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
		return 0, checkHealthUnknown
	}
	if failing > 0 {
		return failing, checkHealthFailing
	}
	return 0, TargetStateOK
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
		return []web.Watch{}
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
		if w.checkType == checks.CheckTypeSwap && !w.disabled {
			swap = swapWatchInfo(system)
		}
		// memory/load/fds/pids carry a natural capacity, so render them with the
		// same progress bar as swap. Skip disabled watches (config, not a live
		// concern) so the UI never probes /proc for something switched off.
		var meter *web.WatchMeter
		var readings []web.WatchReading
		liveSummary := ""
		if !w.disabled && !w.serviceScoped {
			meter, readings, liveSummary = b.watchDashboardView(w, system)
		}
		monitorMode := w.monitorMode
		if monitorMode == "" {
			monitorMode = config.MonitorEnabled
		}
		ww := web.Watch{
			Name:          w.name,
			DisplayName:   w.displayName,
			Category:      w.category,
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
		observed := b.settling == nil || b.settling.Observed(SettlingWatchKey(name))
		failed := observed && watchViewFailed(ww)
		ww.State = WatchState(ww.Enabled, ww.Monitored, failed, observed)
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
		return string(servicemgr.StatusUnknown)
	}
	e.statusMu.Lock()
	defer e.statusMu.Unlock()
	if !e.statusAt.IsZero() && now.Sub(e.statusAt) < serviceStatusCacheTTL {
		return e.cachedStatus
	}
	st, err := e.status(ctx)
	if err != nil {
		if ctx.Err() != nil {
			// The viewer cancelled the request mid-probe (e.g. closed the tab).
			// Don't poison the shared cache with "error" for everyone else;
			// keep the previous entry and let the next poll retry.
			if !e.statusAt.IsZero() {
				return e.cachedStatus
			}
			return string(servicemgr.StatusUnknown)
		}
		e.cachedStatus = backendStatusError
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
	if watchStorageMountFailed(w) {
		return true
	}
	return (w.Storage != nil && (w.Storage.SampleError != "" || w.Storage.MountSampleError != "")) || watchReadingsFailed(w.Readings)
}

func watchStorageMountFailed(w web.Watch) bool {
	if w.Storage == nil {
		return false
	}
	for _, cond := range w.Conditions {
		if cond.Field != checks.DataKeyMounted || cond.Op != cfgval.CompareOpEqual {
			continue
		}
		expect, err := strconv.ParseBool(cond.Value)
		if err != nil {
			continue
		}
		return w.Storage.Mounted != expect
	}
	return false
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
	case eventKindFiring, eventKindRecovered, eventKindDryRun, eventKindHook, eventKindNotify, eventKindHookFail, eventKindNotifyFail, eventKindExpand, eventKindExpandSkipped, eventKindExpandFailed:
		return true
	default:
		return false
	}
}

func checkMap(entry map[string]any) map[string]any {
	check, _ := entry[config.WatchKeyCheck].(map[string]any)
	return check
}

func metricsMap(entry map[string]any) map[string]any {
	metrics, _ := entry[config.SectionMetrics].(map[string]any)
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
		if expect, ok := storageMountExpectation(w.check); ok && storage.Mounted != expect {
			if expect {
				return storage.Path + ": not mounted"
			}
			return storage.Path + ": mounted"
		}
		fs := storage.FileSystem
		if fs == "" {
			fs = watchFallbackFilesystem
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
	return strings.Join(parts, displayListSeparator)
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
			Op:    cfgval.AsString(m[checks.CheckKeyOp]),
			Value: cfgval.String(m[checks.CheckKeyValue]),
		})
	}
	switch cfgval.AsString(check[checks.CheckKeyType]) {
	case checks.CheckTypeAutofs:
		if path := cfgval.AsString(check[checks.CheckKeyPath]); path != "" {
			out = append(out, web.WatchCondition{Field: checks.DataKeyPath, Op: cfgval.CompareOpEqual, Value: path})
		} else if _, ok := check[checks.CheckKeyCount].(map[string]any); !ok {
			out = append(out, web.WatchCondition{Field: checks.DataKeyCount, Op: cfgval.CompareOpGreaterEqual, Value: watchConditionDefaultMinimum})
		}
	case checks.CheckTypeCount:
		if path := cfgval.AsString(check[checks.CheckKeyPath]); path != "" {
			out = append(out, web.WatchCondition{Field: checks.DataKeyPath, Value: path})
		}
		if kind := cfgval.AsString(check[checks.CheckKeyOf]); kind != "" {
			out = append(out, web.WatchCondition{Field: checks.DataKeyOf, Value: kind})
		}
		if recursive, ok := check[checks.CheckKeyRecursive].(bool); ok {
			out = append(out, web.WatchCondition{Field: checks.DataKeyRecursive, Op: cfgval.CompareOpEqual, Value: strconv.FormatBool(recursive)})
		}
		if m, ok := check[checks.CheckKeyCount].(map[string]any); ok {
			out = append(out, web.WatchCondition{Field: checks.DataKeyCount, Op: cfgval.AsString(m[checks.CheckKeyOp]), Value: cfgval.String(m[checks.CheckKeyValue])})
		} else if op := cfgval.AsString(check[checks.CheckKeyOp]); op != "" {
			out = append(out, web.WatchCondition{Field: checks.DataKeyCount, Op: op, Value: cfgval.String(check[checks.CheckKeyValue])})
		}
	case checks.CheckTypeFile:
		out = append(out, fileWatchConditions(check)...)
	case checks.CheckTypeProcess:
		if value := cfgval.String(check[checks.CheckKeyFor]); value != "" {
			out = append(out, web.WatchCondition{Field: checks.CheckKeyFor, Op: cfgval.CompareOpGreaterEqual, Value: value})
		}
		if gone, ok := check[checks.CheckKeyGone].(bool); ok && gone {
			out = append(out, web.WatchCondition{Field: checks.CheckKeyGone, Op: cfgval.CompareOpEqual, Value: strconv.FormatBool(true)})
		}
	case checks.CheckTypeRoute:
		family := cfgval.AsString(check[checks.CheckKeyFamily])
		if family == "" {
			family = checks.FamilyIPv4
		}
		out = append(out, web.WatchCondition{Field: checks.DataKeyFamily, Op: cfgval.CompareOpEqual, Value: family})
		if iface := cfgval.AsString(check[checks.CheckKeyInterface]); iface != "" {
			out = append(out, web.WatchCondition{Field: checks.DataKeyInterface, Op: cfgval.CompareOpEqual, Value: iface})
		}
	case checks.CheckTypeFirewallRules:
		backend := cfgval.AsString(check[checks.CheckKeyBackend])
		if backend == "" {
			backend = checks.FirewallBackendAuto
		}
		minRules := cfgval.String(check[checks.CheckKeyMinRules])
		if minRules == "" {
			minRules = strconv.FormatUint(watchFirewallDefaultMinRules, watchReadingNumericBase)
		}
		out = append(out,
			web.WatchCondition{Field: checks.DataKeyBackend, Op: cfgval.CompareOpEqual, Value: backend},
			web.WatchCondition{Field: checks.DataKeyRules, Op: cfgval.CompareOpGreaterEqual, Value: minRules},
		)
	case checks.CheckTypeSize:
		if path := cfgval.AsString(check[checks.CheckKeyPath]); path != "" {
			out = append(out, web.WatchCondition{Field: checks.DataKeyPath, Value: path})
		}
		if growBy := cfgval.String(check[checks.CheckKeyGrowBy]); growBy != "" {
			out = append(out, web.WatchCondition{Field: watchConditionFieldGrowth, Op: cfgval.CompareOpGreaterEqual, Value: growBy})
		}
		if within := cfgval.String(check[checks.CheckKeyWithin]); within != "" {
			out = append(out, web.WatchCondition{Field: checks.CheckKeyWithin, Value: within})
		}
	}
	if v, ok := check[checks.CheckKeyMounted].(bool); ok {
		out = append(out, web.WatchCondition{Field: checks.DataKeyMounted, Op: cfgval.CompareOpEqual, Value: strconv.FormatBool(v)})
	}
	if cfgval.AsString(check[checks.CheckKeyType]) == checks.CheckTypeOOM {
		if _, ok := check[checks.CheckKeyDelta].(map[string]any); !ok {
			out = append(out, web.WatchCondition{Field: checks.CheckKeyDelta, Op: cfgval.CompareOpGreater, Value: watchConditionDefaultDelta})
		}
	}
	out = append(out, watchMetricConditions(metrics)...)
	return out
}

func watchConditionFields(check map[string]any) []string {
	checkType := cfgval.AsString(check[checks.CheckKeyType])
	switch checkType {
	case checks.CheckTypeStorage:
		return checks.StoragePredFields
	case checks.CheckTypeMemory:
		return checks.MemoryPredFields
	case checks.CheckTypePressure:
		return checks.PressurePredFields
	case checks.CheckTypeLoad:
		return checks.LoadPredFields
	case checks.CheckTypeFDS:
		return checks.FdsPredFields
	case checks.CheckTypePIDs:
		return checks.PidsPredFields
	case checks.CheckTypeConntrack:
		return checks.ConntrackPredFields
	case checks.CheckTypeEntropy:
		return checks.EntropyPredFields
	case checks.CheckTypeZombies:
		return checks.ZombiePredFields
	case checks.CheckTypeOOM:
		return []string{checks.CheckKeyDelta}
	case checks.CheckTypeProcess:
		return []string{metrics.MetricCPU, metrics.MetricMemory, metrics.MetricIO}
	case checks.CheckTypeDiskIO:
		return checks.DiskIOPredFields
	case checks.CheckTypeSensors:
		return checks.SensorPredFields
	case checks.CheckTypeHdparm:
		return checks.HdparmPredFields
	case checks.CheckTypeSmart:
		return checks.SmartPredFields
	case checks.CheckTypeRAID:
		return checks.RaidPredFields
	case checks.CheckTypeEDAC:
		return checks.EdacPredFields
	case checks.CheckTypeAutofs:
		return []string{checks.CheckKeyCount}
	default:
		return nil
	}
}

func fileWatchConditions(check map[string]any) []web.WatchCondition {
	var out []web.WatchCondition
	if path := cfgval.AsString(check[checks.CheckKeyPath]); path != "" {
		out = append(out, web.WatchCondition{Field: checks.DataKeyPath, Value: path})
	}
	if recursive, ok := check[checks.CheckKeyRecursive].(bool); ok {
		out = append(out, web.WatchCondition{Field: checks.DataKeyRecursive, Op: cfgval.CompareOpEqual, Value: strconv.FormatBool(recursive)})
	}
	if size, ok := check[checks.CheckKeySize].(map[string]any); ok {
		if on := cfgval.AsString(size[checks.CheckKeyOn]); on != "" {
			out = append(out, web.WatchCondition{Field: checks.DataKeySize, Value: on})
		} else {
			out = append(out, web.WatchCondition{Field: checks.DataKeySize, Op: cfgval.AsString(size[checks.CheckKeyOp]), Value: cfgval.String(size[checks.CheckKeyValue])})
		}
	}
	for _, field := range []string{checks.CheckKeyPermissions, checks.CheckKeyOwner} {
		if m, ok := check[field].(map[string]any); ok {
			out = append(out, web.WatchCondition{Field: field, Value: cfgval.AsString(m[checks.CheckKeyOn])})
		}
	}
	if m, ok := check[checks.CheckKeyExistence].(map[string]any); ok {
		out = append(out, web.WatchCondition{Field: checks.CheckKeyExistence, Value: cfgval.AsString(m[checks.CheckKeyOn])})
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
		if on := cfgval.AsString(entry[checks.CheckKeyOn]); on != "" {
			out = append(out, web.WatchCondition{Field: watchMetricConditionField(metric, watchMetricSuffixOn), Value: on})
		}
		if expect := cfgval.AsString(entry[checks.CheckKeyExpect]); expect != "" {
			out = append(out, web.WatchCondition{Field: watchMetricConditionField(metric, watchMetricSuffixExpect), Op: cfgval.CompareOpEqual, Value: expect})
		}
		if delta, ok := entry[checks.CheckKeyDelta].(map[string]any); ok {
			out = append(out, web.WatchCondition{
				Field: watchMetricConditionField(metric, watchMetricSuffixDelta),
				Op:    cfgval.AsString(delta[checks.CheckKeyOp]),
				Value: cfgval.String(delta[checks.CheckKeyValue]),
			})
		}
		if threshold, ok := entry[checks.CheckKeyThreshold].(map[string]any); ok {
			out = append(out, web.WatchCondition{
				Field: watchMetricConditionField(metric, watchMetricSuffixThreshold),
				Op:    cfgval.AsString(threshold[checks.CheckKeyOp]),
				Value: cfgval.String(threshold[checks.CheckKeyValue]),
			})
		}
		if change, ok := entry[checks.CheckKeyChange].(map[string]any); ok {
			out = append(out, web.WatchCondition{
				Field: watchMetricConditionField(metric, watchMetricSuffixChange),
				Op:    cfgval.CompareOpGreater,
				Value: cfgval.String(change[checks.CheckKeyDelta]),
			})
		}
		for _, field := range []string{checks.LevelFieldUsedPct, checks.LevelFieldFreePct, checks.LevelFieldFreeBytes} {
			m, ok := entry[field].(map[string]any)
			if !ok {
				continue
			}
			out = append(out, web.WatchCondition{
				Field: watchMetricConditionField(metric, field),
				Op:    cfgval.AsString(m[checks.CheckKeyOp]),
				Value: cfgval.String(m[checks.CheckKeyValue]),
			})
		}
	}
	return out
}

func watchMetricConditionField(metric, suffix string) string {
	return metric + watchMetricFieldSeparator + suffix
}

// heavyLiveViewTypes are the watch check types whose dashboard live view runs
// an expensive external command. The daemon watch cycle already owns those
// probes, so /api/watches only serves cached data for them and never starts a
// fresh disk command just because the panel opened.
// Deliberately excluded: cheap/proc/sys views (memory/load/net/sensors/process),
// filesystem state views used by tests and operators, and rate-based diskio,
// which must sample on every poll to compute deltas.
var heavyLiveViewTypes = map[string]bool{
	checks.CheckTypeHdparm: true,
	checks.CheckTypeSmart:  true,
}

func (b *WebBackend) watchDashboardView(w *webWatch, system metrics.Snapshot) (*web.WatchMeter, []web.WatchReading, string) {
	if w == nil {
		return nil, nil, ""
	}
	if b.watchSnapshots != nil && watchUsesDaemonSnapshot(w.checkType) {
		return b.watchSnapshotView(w, system)
	}
	return b.legacyWatchLiveView(w, system)
}

func watchUsesDaemonSnapshot(checkType string) bool {
	switch checkType {
	case checks.CheckTypeFile, checks.CheckTypeProcess:
		return false
	default:
		return true
	}
}

func (b *WebBackend) watchSnapshotView(w *webWatch, system metrics.Snapshot) (*web.WatchMeter, []web.WatchReading, string) {
	snaps := b.watchSnapshots.Get(w.name, w.checkType)
	if len(snaps) == 0 {
		if m := watchMeter(w.checkType, system); m != nil {
			return m, nil, ""
		}
		return nil, nil, ""
	}
	var meter *web.WatchMeter
	var readings []web.WatchReading
	var summaries []string
	for _, snap := range snaps {
		if !b.watchSnapshotCurrent(w, snap) || !watchSnapshotMetricConfigured(w, snap) {
			continue
		}
		rs := watchSnapshotReadings(w.checkType, snap)
		readings = append(readings, rs...)
		if meter == nil {
			meter = watchMeterFromSnapshot(w.checkType, snap.Data)
		}
		if summary := watchSnapshotSummary(snap, rs); summary != "" {
			summaries = append(summaries, summary)
		}
	}
	if meter == nil {
		meter = watchMeter(w.checkType, system)
	}
	return meter, readings, strings.Join(summaries, " · ")
}

func (b *WebBackend) watchSnapshotCurrent(w *webWatch, snap CheckSnapshot) bool {
	if snap.At.IsZero() {
		return false
	}
	return b.webNow().Sub(snap.At) <= runtimePublishMaxAge(w.interval)
}

func watchSnapshotMetricConfigured(w *webWatch, snap CheckSnapshot) bool {
	metric := cfgval.String(snap.Data[checks.DataKeyMetric])
	if metric == "" || len(w.metrics) == 0 {
		return true
	}
	_, ok := w.metrics[metric]
	return ok
}

func watchSnapshotReadings(checkType string, snap CheckSnapshot) []web.WatchReading {
	readings := checkReadings(checkType, snap.Data)
	if len(readings) == 0 && snap.Message != "" {
		readings = []web.WatchReading{{Field: watchReadingFieldResult, Label: watchReadingLabelResult, Value: snap.Message}}
	}
	if !snap.healthy() && snap.Message != "" {
		readings = append([]web.WatchReading{{Field: watchReadingFieldError, Label: watchReadingLabelError, Error: snap.Message}}, readings...)
	}
	return readings
}

func watchSnapshotSummary(snap CheckSnapshot, readings []web.WatchReading) string {
	if snap.Message != "" {
		return snap.Message
	}
	for _, r := range readings {
		if r.Error != "" {
			return r.Error
		}
		if r.Value != "" {
			return r.Value
		}
	}
	return ""
}

func watchMeterFromSnapshot(checkType string, data map[string]any) *web.WatchMeter {
	switch checkType {
	case checks.CheckTypeMemory:
		total, totalOK := uintField(data[checks.DataKeyTotalBytes])
		available, availableOK := uintField(data[checks.DataKeyAvailableBytes])
		usedPct, pctOK := cfgval.Float(data[checks.DataKeyUsedPct])
		if !totalOK || !availableOK || !pctOK {
			return nil
		}
		available = min(available, total)
		return &web.WatchMeter{
			Kind:       metrics.MetricMemory,
			UsedPct:    usedPct,
			TotalBytes: total,
			UsedBytes:  total - available,
			FreeBytes:  available,
		}
	case checks.CheckTypeLoad:
		load, loadOK := cfgval.Float(data[metrics.MetricLoad1])
		numCPU, cpuOK := cfgval.Int(data[checks.DataKeyNumCPU])
		if !loadOK || !cpuOK || numCPU <= 0 {
			return nil
		}
		return &web.WatchMeter{Kind: checks.CheckTypeLoad, UsedPct: load / float64(numCPU) * metrics.PercentScale, Load: load, NumCPU: numCPU}
	case checks.CheckTypeFDS:
		return watchCountMeter(checks.CheckTypeFDS, data, checks.DataKeyAllocated)
	case checks.CheckTypePIDs:
		return watchCountMeter(checks.CheckTypePIDs, data, checks.DataKeyCount)
	case checks.CheckTypeConntrack:
		return watchCountMeter(checks.CheckTypeConntrack, data, checks.DataKeyCount)
	default:
		return nil
	}
}

func watchCountMeter(kind string, data map[string]any, countKey string) *web.WatchMeter {
	count, countOK := uintField(data[countKey])
	limit, limitOK := uintField(data[checks.DataKeyMax])
	usedPct, pctOK := cfgval.Float(data[checks.DataKeyUsedPct])
	if !countOK || !limitOK || !pctOK || limit == 0 {
		return nil
	}
	return &web.WatchMeter{Kind: kind, UsedPct: usedPct, Count: count, Max: limit}
}

func uintField(v any) (uint64, bool) {
	switch n := v.(type) {
	case uint64:
		return n, true
	case int:
		if n >= 0 {
			return uint64(n), true
		}
	case int64:
		if n >= 0 {
			return uint64(n), true
		}
	case float64:
		if n >= 0 {
			return uint64(n), true
		}
	}
	return 0, false
}

// legacyWatchLiveView serves older in-process web backends that were not wired
// with WatchSnapshots. Expensive disk commands are still blocked here; sermod
// publishes their daemon-cycle results through WatchSnapshots instead.
func (b *WebBackend) legacyWatchLiveView(w *webWatch, system metrics.Snapshot) (*web.WatchMeter, []web.WatchReading, string) {
	if w == nil {
		return nil, nil, ""
	}
	if heavyLiveViewTypes[w.checkType] {
		return nil, nil, ""
	}
	return b.watchLiveView(w, system)
}

func (b *WebBackend) watchLiveView(w *webWatch, system metrics.Snapshot) (*web.WatchMeter, []web.WatchReading, string) {
	if w == nil {
		return nil, nil, ""
	}
	switch w.checkType {
	case checks.CheckTypeNet:
		return b.netWatchView(w)
	case checks.CheckTypeICMP:
		return b.icmpWatchView(w)
	case checks.CheckTypeSwap:
		return nil, nil, ""
	case checks.CheckTypeOOM:
		return b.oomWatchView()
	case checks.CheckTypeFDS:
		return b.fdsWatchView()
	case checks.CheckTypePIDs:
		return b.pidsWatchView()
	case checks.CheckTypePressure:
		return b.pressureWatchView(w)
	case checks.CheckTypeConntrack:
		return b.conntrackWatchView()
	case checks.CheckTypeEntropy:
		return b.entropyWatchView()
	case checks.CheckTypeZombies:
		return b.zombieWatchView()
	case checks.CheckTypeProcess:
		return b.processWatchView(w)
	case checks.CheckTypeAutofs:
		return b.autofsWatchView(w)
	case checks.CheckTypeDiskIO:
		return b.diskIOWatchView(w)
	case checks.CheckTypeSensors:
		return b.sensorsWatchView(w)
	case checks.CheckTypeRAID:
		return b.raidWatchView()
	case checks.CheckTypeEDAC:
		return b.edacWatchView()
	case checks.CheckTypeRoute:
		return b.routeWatchView(w)
	case checks.CheckTypeFile:
		return b.fileWatchView(w)
	case checks.CheckTypeCount:
		return b.countWatchView(w)
	case checks.CheckTypeFirewallRules:
		return b.firewallRulesWatchView(w)
	case checks.CheckTypeSize:
		return b.sizeWatchView(w)
	case checks.CheckTypeHdparm:
		return b.hdparmWatchView(w)
	case checks.CheckTypeSmart:
		return b.smartWatchView(w)
	default:
		if m := watchMeter(w.checkType, system); m != nil {
			return m, nil, ""
		}
		return b.probeWatchView(w)
	}
}

func (b *WebBackend) processWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	name := cfgval.AsString(w.check[checks.CheckKeyName])
	if name == "" {
		msg := watchMissingNameMessage
		return nil, watchErrorReadings(msg), "process: " + msg
	}
	user := cfgval.AsString(w.check[checks.CheckKeyUser])
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
		{Field: watchReadingFieldProcess, Label: watchReadingLabelProcess, Value: name},
		{Field: watchReadingFieldMatches, Label: watchReadingLabelMatches, Value: fmt.Sprintf("%d", len(samples))},
	}
	if user != "" {
		readings = append(readings, web.WatchReading{Field: watchReadingFieldUser, Label: watchReadingLabelUser, Value: user})
	}
	if len(samples) > 0 {
		readings = append(readings,
			web.WatchReading{Field: checks.DataKeyPIDs, Label: watchReadingLabelPIDs, Value: processPIDList(samples)},
			web.WatchReading{Field: watchReadingFieldRSS, Label: watchReadingLabelRSS, Value: fmt.Sprintf("%d %s", rssTotal, metrics.MetricUnitBytes)},
			web.WatchReading{Field: watchReadingFieldCPUTicks, Label: watchReadingLabelCPUTicks, Value: fmt.Sprintf("%d", cpuTicksTotal)},
		)
		if ioKnown {
			readings = append(readings, web.WatchReading{Field: metrics.MetricIO, Label: watchReadingLabelIO, Value: fmt.Sprintf("%d %s", ioTotal, metrics.MetricUnitBytes)})
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
	readings := []web.WatchReading{{Field: checks.DataKeyCount, Label: watchReadingLabelMountpoints, Value: fmt.Sprintf("%d", len(points))}}
	if len(points) > 0 {
		readings = append(readings, web.WatchReading{Field: checks.DataKeyMountpoints, Label: watchReadingLabelPaths, Value: strings.Join(points, displayListSeparator)})
	}
	if path := cfgval.AsString(w.check[checks.CheckKeyPath]); path != "" {
		state := watchReadingStateMissing
		if slices.Contains(points, path) {
			state = watchReadingStateActive
		}
		readings = append(readings, web.WatchReading{Field: checks.DataKeyPath, Label: watchReadingLabelConfiguredPath, Value: path})
		readings = append(readings, web.WatchReading{Field: watchReadingFieldState, Label: watchReadingLabelState, Value: state})
		return nil, readings, fmt.Sprintf("autofs %s %s (%d mountpoint%s)", path, state, len(points), pluralSuffix(len(points), "mountpoint"))
	}
	return nil, readings, fmt.Sprintf("%d autofs mountpoint%s active", len(points), pluralSuffix(len(points), "mountpoint"))
}

func (b *WebBackend) diskIOWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	device := cfgval.AsString(w.check[checks.CheckKeyDevice])
	if device == "" {
		msg := watchMissingDeviceMessage
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
	switch {
	case !st.primed:
		st = webDiskIOState{primed: true, at: at, sample: sample}
		b.diskIOState[key] = st
	case at.Sub(st.at) >= diskIORateMinWindow:
		next := webDiskIOState{primed: true, at: at, sample: sample}
		next.rates, next.hasRates = checks.CalculateDiskIORates(st.sample, sample, at.Sub(st.at))
		st = next
		b.diskIOState[key] = st
	}
	// Polls inside diskIORateMinWindow keep the previous baseline and serve
	// its last computed rates (st unchanged).
	b.diskIOMu.Unlock()

	readings := []web.WatchReading{{Field: checks.DataKeyDevice, Label: watchReadingLabelDevice, Value: device}}
	if !st.hasRates {
		readings = append(readings, web.WatchReading{Field: watchReadingFieldState, Label: watchReadingLabelState, Value: watchReadingStateBaseline})
		return nil, readings, "diskio " + device + " baseline"
	}
	rates := st.rates
	readings = append(readings,
		web.WatchReading{Field: checks.DiskIOFieldUtilPct, Label: watchReadingLabelUtilization, Value: watchPercent(rates.UtilPct)},
		web.WatchReading{Field: checks.DiskIOFieldReadBytes, Label: watchReadingLabelRead, Value: watchReadingMetricValue(rates.ReadBytes, 0, metrics.MetricUnitBytesPerSecond)},
		web.WatchReading{Field: checks.DiskIOFieldWriteBytes, Label: watchReadingLabelWrite, Value: watchReadingMetricValue(rates.WriteBytes, 0, metrics.MetricUnitBytesPerSecond)},
		web.WatchReading{Field: checks.DiskIOFieldAwaitMs, Label: watchReadingLabelAwait, Value: watchReadingMetricValue(rates.AwaitMs, 1, metrics.MetricUnitMilliseconds)},
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
	chip := cfgval.AsString(w.check[checks.CheckKeyChip])
	label := cfgval.AsString(w.check[checks.CheckKeyLabel])
	values := checks.SummarizeSensors(readings, chip, label)
	out := []web.WatchReading{{Field: checks.DataKeyInputs, Label: watchReadingLabelInputs, Value: fmt.Sprintf("%d", values.Count)}}
	if chip != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyChip, Label: watchReadingLabelChipFilter, Value: chip})
	}
	if label != "" {
		out = append(out, web.WatchReading{Field: checks.DataKeyLabel, Label: watchReadingLabelLabelFilter, Value: label})
	}
	parts := make([]string, 0, 3)
	if values.HasTemp {
		out = append(out, web.WatchReading{Field: checks.DataKeyTemp, Label: watchReadingLabelHottestTemp, Value: watchReadingMetricValue(values.Temp, 1, watchReadingUnitCelsius)})
		parts = append(parts, fmt.Sprintf("temp=%.1fC", values.Temp))
	}
	if values.HasFan {
		out = append(out, web.WatchReading{Field: checks.DataKeyFan, Label: watchReadingLabelSlowestFan, Value: watchReadingMetricValue(values.Fan, 0, watchReadingUnitRPM)})
		parts = append(parts, fmt.Sprintf("fan=%.0fRPM", values.Fan))
	}
	if values.HasVoltage {
		out = append(out, web.WatchReading{Field: checks.DataKeyVoltage, Label: watchReadingLabelVoltage, Value: watchReadingMetricValue(values.Voltage, 2, watchReadingUnitVolt)})
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
		{Field: checks.DataKeyArrays, Label: watchReadingLabelArrays, Value: fmt.Sprintf("%d", st.Arrays)},
		{Field: checks.DataKeyDegraded, Label: watchReadingLabelDegraded, Value: fmt.Sprintf("%d", st.Degraded)},
		{Field: checks.DataKeyRecovering, Label: watchReadingLabelRecovering, Value: fmt.Sprintf("%d", st.Recovering)},
	}
	summary := fmt.Sprintf("raid: %d arrays, %d degraded, %d recovering", st.Arrays, st.Degraded, st.Recovering)
	if len(st.DegradedNames) > 0 {
		names := strings.Join(st.DegradedNames, displayListSeparator)
		readings = append(readings, web.WatchReading{Field: checks.DataKeyDegradedArrays, Label: watchReadingLabelDegradedArrays, Value: names})
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
		return nil, []web.WatchReading{{Field: checks.DataKeyPresent, Label: watchReadingLabelEDAC, Error: msg}}, "edac: " + msg
	}
	return nil,
		[]web.WatchReading{
			{Field: checks.DataKeyCE, Label: watchReadingLabelCorrectable, Value: fmt.Sprintf("%d", st.CE)},
			{Field: checks.DataKeyUE, Label: watchReadingLabelUncorrectable, Value: fmt.Sprintf("%d", st.UE)},
		},
		fmt.Sprintf("edac: %d correctable, %d uncorrectable", st.CE, st.UE)
}

func (b *WebBackend) routeWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	family := cfgval.AsString(w.check[checks.CheckKeyFamily])
	if family == "" {
		family = checks.FamilyIPv4
	}
	iface := cfgval.AsString(w.check[checks.CheckKeyInterface])
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
		{Field: checks.DataKeyFamily, Label: watchReadingLabelFamily, Value: family},
		{Field: checks.DataKeyRoutes, Label: watchReadingLabelDefaultRoutes, Value: fmt.Sprintf("%d", len(routes))},
	}
	if iface != "" {
		readings = append(readings, web.WatchReading{Field: checks.DataKeyInterface, Label: watchReadingLabelRequiredInterface, Value: iface})
	}
	if len(matched) > 0 {
		readings = append(readings, web.WatchReading{Field: checks.DataKeyEgress, Label: watchReadingLabelEgress, Value: matched[0].Iface})
		if matched[0].Gateway != "" {
			readings = append(readings, web.WatchReading{Field: checks.DataKeyGateway, Label: watchReadingLabelGateway, Value: matched[0].Gateway})
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
	iface := cfgval.AsString(w.check[checks.CheckKeyInterface])
	if iface == "" {
		msg := watchMissingInterfaceMessage
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
		{Field: checks.DataKeyInterface, Label: watchReadingLabelInterface, Value: iface},
		{Field: checks.NetMetricState, Label: watchReadingLabelState, Value: s.State},
	}
	parts := []string{iface + " state " + s.State}
	if watchMetricEnabled(w.metrics, checks.NetMetricSpeed) {
		if s.SpeedKnown {
			readings = append(readings, web.WatchReading{Field: checks.NetMetricSpeed, Label: watchReadingLabelSpeed, Value: watchReadingIntMetricValue(s.SpeedMbps, watchReadingUnitMegabitsPerSecond)})
			parts = append(parts, fmt.Sprintf("speed %d Mbps", s.SpeedMbps))
		} else {
			readings = append(readings, web.WatchReading{Field: checks.NetMetricSpeed, Label: watchReadingLabelSpeed, Value: watchReadingValueUnknown})
			parts = append(parts, "speed "+watchReadingValueUnknown)
		}
	}
	if watchMetricEnabled(w.metrics, checks.NetMetricErrors) {
		total := netErrorTotal(w.metrics, s.Counters)
		readings = append(readings, web.WatchReading{Field: checks.NetMetricErrors, Label: watchReadingLabelErrorsTotal, Value: fmt.Sprintf("%d", total)})
		parts = append(parts, fmt.Sprintf("errors %d", total))
	}
	if watchMetricEnabled(w.metrics, checks.NetMetricAddress) {
		value := strings.Join(s.Addrs, displayListSeparator)
		if value == "" {
			value = watchReadingValueNone
		}
		readings = append(readings, web.WatchReading{Field: checks.NetMetricAddress, Label: watchReadingLabelAddresses, Value: value})
		parts = append(parts, fmt.Sprintf("%d address%s", len(s.Addrs), pluralSuffix(len(s.Addrs), "address")))
	}
	return nil, readings, strings.Join(parts, " · ")
}

func (b *WebBackend) icmpWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	host := cfgval.AsString(w.check[checks.CheckKeyHost])
	if host == "" {
		msg := "missing host"
		return nil, watchErrorReadings(msg), "icmp: " + msg
	}
	count := checks.DefaultPingCount
	if v, ok := cfgval.Int(w.check[checks.CheckKeyCount]); ok && v > 0 {
		count = v
	}
	timeout := cfgval.Duration(w.check[checks.CheckKeyTimeout])
	if timeout <= 0 {
		timeout = b.defaultTimeout
	}
	s, err := checks.SampleICMP(host, cfgval.StringList(w.check[checks.CheckKeyInterface]),
		cfgval.AsString(w.check[checks.CheckKeyInterfaceMatch]) == checks.InterfaceMatchAll, count, timeout, b.pingSampler)
	if err != nil {
		msg := err.Error()
		return nil, watchErrorReadings(msg), "icmp " + host + ": " + msg
	}
	state := checks.NetStateDown
	if s.Reachable {
		state = checks.NetStateUp
	}
	readings := []web.WatchReading{
		{Field: checks.DataKeyHost, Label: watchReadingLabelHost, Value: host},
		{Field: checks.NetMetricState, Label: watchReadingLabelState, Value: state},
	}
	parts := []string{host + " " + state}
	if s.RTTKnown {
		readings = append(readings, web.WatchReading{Field: checks.IcmpMetricLatency, Label: watchReadingLabelRTT, Value: watchReadingMetricValue(s.RTTms, 1, metrics.MetricUnitMilliseconds)})
		parts = append(parts, fmt.Sprintf("rtt %.1f ms", s.RTTms))
	} else if watchMetricEnabled(w.metrics, checks.IcmpMetricLatency) {
		readings = append(readings, web.WatchReading{Field: checks.IcmpMetricLatency, Label: watchReadingLabelRTT, Value: watchReadingValueUnknown})
		parts = append(parts, "rtt "+watchReadingValueUnknown)
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
		[]web.WatchReading{{Field: checks.DataKeyTotal, Label: watchReadingLabelOOMKills, Value: fmt.Sprintf("%d", count)}},
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
		usedPct := float64(s.Allocated) / float64(s.Max) * metrics.PercentScale
		summary = fmt.Sprintf("fds %d/%d allocated (%.1f%%)", s.Allocated, s.Max, usedPct)
	}
	if meter := countMeter(checks.CheckTypeFDS, s.Allocated, s.Max); meter != nil {
		return meter, nil, summary
	}
	return nil, []web.WatchReading{{Field: checks.DataKeyCount, Label: watchReadingLabelAllocated, Value: fmt.Sprintf("%d", s.Allocated)}}, summary
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
		usedPct := float64(s.Threads) / float64(s.Max) * metrics.PercentScale
		summary = fmt.Sprintf("pids %d/%d in use (%.1f%%)", s.Threads, s.Max, usedPct)
	}
	if meter := countMeter(checks.CheckTypePIDs, s.Threads, s.Max); meter != nil {
		return meter, nil, summary
	}
	return nil, []web.WatchReading{{Field: checks.DataKeyCount, Label: watchReadingLabelInUse, Value: fmt.Sprintf("%d", s.Threads)}}, summary
}

func (b *WebBackend) pressureWatchView(w *webWatch) (*web.WatchMeter, []web.WatchReading, string) {
	resource := cfgval.AsString(w.check[checks.CheckKeyResource])
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
		{Field: checks.DataKeyResource, Label: watchReadingLabelResource, Value: resource},
		{Field: checks.PressureFieldSomeAvg10, Label: watchReadingLabelSomeAvg10, Value: watchPercent(s.Some.Avg10)},
		{Field: checks.PressureFieldSomeAvg60, Label: watchReadingLabelSomeAvg60, Value: watchPercent(s.Some.Avg60)},
		{Field: checks.PressureFieldSomeAvg300, Label: watchReadingLabelSomeAvg300, Value: watchPercent(s.Some.Avg300)},
		{Field: checks.PressureFieldFullAvg10, Label: watchReadingLabelFullAvg10, Value: watchPercent(s.Full.Avg10)},
		{Field: checks.PressureFieldFullAvg60, Label: watchReadingLabelFullAvg60, Value: watchPercent(s.Full.Avg60)},
		{Field: checks.PressureFieldFullAvg300, Label: watchReadingLabelFullAvg300, Value: watchPercent(s.Full.Avg300)},
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
		usedPct := float64(s.Count) / float64(s.Max) * metrics.PercentScale
		summary = fmt.Sprintf("conntrack %d/%d entries (%.1f%%)", s.Count, s.Max, usedPct)
	}
	if meter := countMeter(checks.CheckTypeConntrack, s.Count, s.Max); meter != nil {
		return meter, nil, summary
	}
	return nil, []web.WatchReading{{Field: checks.DataKeyCount, Label: watchReadingLabelCount, Value: fmt.Sprintf("%d entries", s.Count)}}, summary
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
		[]web.WatchReading{{Field: checks.DataKeyAvail, Label: watchReadingLabelAvailable, Value: watchReadingUintMetricValue(avail, watchReadingUnitBits)}},
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
		[]web.WatchReading{{Field: checks.DataKeyCount, Label: watchReadingLabelZombies, Value: fmt.Sprintf("%d", count)}},
		fmt.Sprintf("%d zombie processes", count)
}

func watchErrorReadings(message string) []web.WatchReading {
	return []web.WatchReading{{Field: watchReadingFieldSample, Label: watchReadingLabelSample, Error: message}}
}

func watchPercent(value float64) string {
	return watchReadingMetricValue(value, 2, metrics.MetricUnitPercent)
}

func watchMetricEnabled(metrics map[string]any, metric string) bool {
	if len(metrics) == 0 {
		return true
	}
	_, ok := metrics[metric]
	return ok
}

func netErrorTotal(metrics map[string]any, counters map[string]uint64) uint64 {
	names := []string{checks.NetCounterRXErrors, checks.NetCounterTXErrors}
	if entry, ok := metrics[checks.NetMetricErrors].(map[string]any); ok {
		if configured := cfgval.StringArray(entry[checks.CheckKeyCounters]); len(configured) > 0 {
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
	parts := make([]string, 0, min(len(samples), processPIDListLimit)+1)
	for i, sample := range samples {
		if i >= processPIDListLimit {
			break
		}
		parts = append(parts, fmt.Sprintf("%d", sample.PID))
	}
	if extra := len(samples) - processPIDListLimit; extra > 0 {
		parts = append(parts, fmt.Sprintf("+%d more", extra))
	}
	return strings.Join(parts, displayListSeparator)
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
	r := system[metrics.MetricTotalSwap]
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
	case metrics.MetricMemory:
		r := system[metrics.MetricTotalMemory]
		used, total, free, ok := byteUsage(r)
		if !ok {
			return nil
		}
		return &web.WatchMeter{
			Kind:       metrics.MetricMemory,
			UsedPct:    r.Percent,
			TotalBytes: total,
			UsedBytes:  used,
			FreeBytes:  free,
		}
	case checks.CheckTypeLoad:
		r, ok := system[metrics.MetricLoad1]
		if !ok || !r.HasAbsolute {
			return nil
		}
		ncpu := runtime.NumCPU()
		pct := 0.0
		if ncpu > 0 {
			pct = r.Absolute / float64(ncpu) * metrics.PercentScale
		}
		return &web.WatchMeter{Kind: checks.CheckTypeLoad, UsedPct: pct, Load: r.Absolute, NumCPU: ncpu}
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
		UsedPct: float64(count) / float64(limit) * metrics.PercentScale,
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
	path := cfgval.String(w.check[checks.CheckKeyPath])
	if path == "" {
		return nil
	}
	info := &web.StorageWatchInfo{Path: path}

	mountSampler := b.mountSampler
	if mountSampler == nil {
		mountSampler = checks.DefaultMounts
	}
	mounts, err := mountSampler()
	if err != nil {
		info.MountSampleError = err.Error()
	} else {
		mount := checks.MountForPath(mounts, path)
		if _, ok := storageMountExpectation(w.check); ok {
			mount = checks.MountAtPath(mounts, path)
		}
		if mount != nil {
			info.Mounted = true
			info.MountPoint = mount.MountPoint
			info.Device = mount.Device
			info.FileSystem = mount.FSType
			info.Options = slices.Clone(mount.Options)
			info.OpenFiles = b.openFilesByMountCached(mounts)[mount.MountPoint]
		}
		if _, ok := storageMountExpectation(w.check); ok && !info.Mounted {
			return info
		}
	}

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
	return info
}

func storageMountExpectation(check map[string]any) (bool, bool) {
	v, ok := check[checks.CheckKeyMounted].(bool)
	return v, ok
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
	// The inventory scan runs version/binary probes and can take seconds, so it
	// must not run under applicationsMu: only one request rebuilds the cache
	// (applicationsRefresh) while every other viewer is served the previous
	// inventory — or, on a cold start, waits for that first scan.
	for {
		b.applicationsMu.Lock()
		cached := slices.Clone(b.applicationsCache)
		hasCache := !b.applicationsAt.IsZero()
		if hasCache && time.Since(b.applicationsAt) < applicationsCacheTTL {
			b.applicationsMu.Unlock()
			return b.withApplicationLastEvents(cached)
		}
		refresh := b.applicationsRefresh
		if refresh == nil {
			break // become the rebuilding request; lock still held
		}
		b.applicationsMu.Unlock()
		if hasCache {
			// An expired-but-complete inventory beats queueing every viewer
			// behind the scan that is already refreshing it.
			return b.withApplicationLastEvents(cached)
		}
		select {
		case <-refresh:
			// Re-check the cache the finished scan produced.
		case <-ctx.Done():
			return nil
		}
	}
	done := make(chan struct{})
	b.applicationsRefresh = done
	b.applicationsMu.Unlock()
	// Clear the in-flight marker in a defer so even a panicking scan cannot
	// leave cold-start viewers waiting on the channel forever. The deferred
	// close runs after the cache update below, so woken viewers always
	// re-check an already-updated cache.
	defer func() {
		b.applicationsMu.Lock()
		b.applicationsRefresh = nil
		b.applicationsMu.Unlock()
		close(done)
	}()

	apps := b.loadApplications(ctx)

	b.applicationsMu.Lock()
	if ctx.Err() != nil {
		// A cancelled request yields a partial inventory; caching it would
		// serve an incomplete app list to every viewer for the full TTL.
		// Prefer the previous complete cache when there is one.
		if !b.applicationsAt.IsZero() {
			apps = slices.Clone(b.applicationsCache)
		}
		b.applicationsMu.Unlock()
		return b.withApplicationLastEvents(apps)
	}
	b.applicationsAt = time.Now()
	b.applicationsCache = slices.Clone(apps)
	b.applicationsMu.Unlock()
	return b.withApplicationLastEvents(apps)
}

func (b *WebBackend) loadApplications(ctx context.Context) []web.Application {
	if b.applicationsList != nil {
		return b.withApplicationSLA(b.applicationsList(ctx))
	}
	if b.cfg == nil {
		return nil
	}
	names := b.cfg.CatalogNamesInCategory(config.CategoryApp)
	if len(names) == 0 {
		return nil
	}
	runner := b.execRunner
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	opts := appinspect.WithUserLookup(b.userLookup)
	type appResult struct {
		app web.Application
		ok  bool
	}
	results := make([]appResult, len(names))
	sem := make(chan struct{}, appInspectionParallelism)
	var wg sync.WaitGroup
	for i, name := range names {
		if b.settling != nil && !b.settling.Observed(SettlingAppKey(name)) {
			resolved, _ := b.cfg.ResolveCatalog(config.CategoryApp, name)
			results[i] = appResult{ok: true, app: web.Application{
				Name:        name,
				DisplayName: config.DisplayName(resolved.Tree, name),
				Category:    config.CategoryLabel(resolved.Tree, config.CategoryApp),
				State:       TargetStateStarting,
			}}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			r := appinspect.InspectOne(ctx, runner, b.cfg, name, opts)
			if r.Installed {
				results[i] = appResult{app: applicationFromReport(r), ok: true}
			}
		}()
	}
	wg.Wait()
	out := make([]web.Application, 0, len(names))
	for _, result := range results {
		if result.ok {
			out = append(out, result.app)
		}
	}
	return b.withApplicationSLA(out)
}

func applicationFromReport(r appinspect.Report) web.Application {
	return web.Application{
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
		State:         applicationStateFromReport(r),
	}
}

func applicationStateFromReport(r appinspect.Report) string {
	status := strings.TrimSpace(strings.ToLower(r.Status))
	if status == "" || status == appinspect.StatusOK || r.OK {
		return TargetStateOK
	}
	if status == appinspect.StatusNotInstalled || status == appinspect.StatusNoBinaryConfigured || strings.HasPrefix(status, appinspect.StatusPrefixError) {
		return TargetStateFailed
	}
	return TargetStateWarning
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

func (b *WebBackend) withApplicationLastEvents(apps []web.Application) []web.Application {
	if len(apps) == 0 || b.events == nil {
		return apps
	}
	out := slices.Clone(apps)
	for i := range out {
		ev, ok := b.events.LastApp(out[i].Name)
		if !ok {
			continue
		}
		webEv := loggedEventToWeb(ev)
		out[i].LastEvent = &webEv
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
	if b.hostType != nil {
		info.HostType = b.hostType
	} else {
		info.HostType = hostTypeInfo()
	}
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
		info.Interval = formatInterval(config.EngineInterval(b.cfg, config.DefaultEngineInterval))
		info.MaxParallelChecks = EngineInt(b.cfg, config.EngineKeyMaxParallelChecks, DefaultEngineMaxParallelChecks)
		info.MaxParallelOperations = EngineInt(b.cfg, config.EngineKeyMaxParallelOperations, DefaultEngineMaxParallelOperations)
		info.DefaultTimeout = formatInterval(EngineDuration(b.cfg, config.EngineKeyDefaultTimeout, DefaultEngineCheckTimeout))
		info.OperationTimeout = formatInterval(EngineDuration(b.cfg, config.EngineKeyOperationTimeout, DefaultEngineOperationTimeout))
		info.StartupDelay = formatInterval(EngineDuration(b.cfg, config.EngineKeyStartupDelay, 0))

		if em := engineMap(b.cfg); em != nil {
			if be, ok := em[config.EngineKeyBackend].(string); ok && be != "" {
				info.Backend = be
			}
		}
		if info.Backend == "" {
			info.Backend = string(servicemgr.BackendAuto)
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
		minute = units.SecondsPerMinute
		hour   = units.MinutesPerHour * minute
		day    = units.HoursPerDay * hour
		week   = units.DaysPerWeek * day
		month  = units.DaysPerMonthApprox * day // display approximation
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
	data, err := os.ReadFile(procUptimePath)
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
	secs, err := strconv.ParseFloat(fields[procUptimeValueIndex], procUptimeFloatBits)
	if err != nil || secs < 0 {
		return 0, false
	}
	return time.Duration(secs * float64(time.Second)), true
}

// osPrettyName returns a human-friendly OS label (PRETTY_NAME from os-release on
// Linux, e.g. "Debian GNU/Linux 12 (bookworm)"), falling back to runtime.GOOS.
func osPrettyName() string {
	for _, path := range config.OSReleasePaths() {
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
	for _, line := range strings.Split(string(data), appLineSeparator) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), osReleasePrettyNameKey); ok {
			if name := strings.Trim(v, osReleaseValueTrimSet); name != "" {
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
	order := []string{
		metrics.MetricLoad1,
		metrics.MetricLoad5,
		metrics.MetricLoad15,
		metrics.MetricTotalCPU,
		metrics.MetricTotalMemory,
		metrics.MetricTotalSwap,
	} // nice display order
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
	case metrics.MetricTotalMemory, metrics.MetricTotalSwap:
		m.Unit = metrics.MetricUnitBytes
	case metrics.MetricLoad1:
		// Only derive the per-CPU percentage from a real reading; guarding on
		// HasAbsolute (as watchMeter does) avoids fabricating Total/Percent when
		// load1 has no absolute value.
		if r.HasAbsolute {
			if ncpu := runtime.NumCPU(); ncpu > 0 {
				m.Total = float64(ncpu)
				m.Percent = r.Absolute / float64(ncpu) * metrics.PercentScale
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
		msg := unknownServiceMessage + service
		b.emitLockReleaseEvent(service, name, eventKindError, eventStatusFailed, msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	if b.cfg == nil {
		msg := "runtime locks are unavailable"
		b.emitLockReleaseEvent(service, name, eventKindError, eventStatusFailed, msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	locker := locks.NewNamedLocker(locks.RuntimeLocksDir(b.cfg.Global.RuntimeDir()))
	locker.Proc = lockProcProber
	lk, err := locker.ReleaseInactive(service, name)
	if err != nil {
		msg := err.Error()
		if lk.State == locks.StateActive {
			b.emitLockReleaseEvent(service, name, eventKindSuppressed, eventStatusBlocked, msg)
		} else {
			b.emitLockReleaseEvent(service, name, eventKindError, eventStatusFailed, msg)
		}
		return web.ActionResult{OK: false, Message: msg}
	}
	id := service
	if name != "" {
		id += "." + name
	}
	msg := "released inactive runtime lock " + id
	b.emitLockReleaseEvent(service, name, eventKindAction, eventStatusOK, msg)
	return web.ActionResult{OK: true, Message: msg}
}

func (b *WebBackend) emitLockReleaseEvent(service, name, kind, status, message string) {
	if b.emit == nil {
		return
	}
	rule := name
	if rule == "" {
		rule = lockReleaseDefaultRule
	}
	b.emit(Event{
		Service: service,
		Kind:    kind,
		Rule:    rule,
		Action:  eventActionReleaseLock,
		Status:  status,
		Message: message,
	})
}

func isServiceOperationAction(action string) bool {
	switch rules.ActionType(action) {
	case rules.ActionStart, rules.ActionStop, rules.ActionRestart, rules.ActionReload, rules.ActionResume:
		return true
	default:
		return false
	}
}

func serviceOperationActionList() []string {
	return []string{
		string(rules.ActionStart),
		string(rules.ActionStop),
		string(rules.ActionRestart),
		string(rules.ActionReload),
		string(rules.ActionResume),
	}
}

// ActivitySummary returns a rollup of recent events for the dashboard.
func (b *WebBackend) ActivitySummary(ctx context.Context) web.ActivitySummary {
	summary := web.ActivitySummary{}

	if b.events == nil {
		return summary
	}

	events := b.events.Recent("", activitySummaryEventScanLimit)
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
		case e.Kind == eventKindAction && isServiceOperationAction(e.Action):
			summary.ServiceActions++
		case e.Kind == eventKindHook || e.Kind == eventKindHookFail:
			summary.WatchHooks++
		case e.Kind == eventKindNotify || e.Kind == eventKindNotifyFail:
			summary.WatchNotifies++
		case e.Kind == eventKindError:
			summary.Errors++
		}
	}

	return summary
}

// MonitoringStatus returns how many services are monitored versus paused.
func (b *WebBackend) MonitoringStatus(_ context.Context) web.MonitoringStatus {
	total := 0
	monitored := 0
	for _, name := range b.order {
		e := b.entries[name]
		if e == nil || e.disabled {
			continue
		}
		total++
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
		return watchReadingValueNone
	}
	switch lk.State {
	case locks.StateActive:
		return lockOwnerStatusLive
	case locks.StateStale:
		return string(locks.StateStale)
	case locks.StateExpired:
		return string(locks.StateExpired)
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

// SetPanic enables or disables the daemon-wide panic mode, persisting the flag
// so it survives daemon restarts. The running workers pick up the change within
// the panic gate's refresh window.
func (b *WebBackend) SetPanic(_ context.Context, on bool) web.ActionResult {
	action := eventActionPanicOff
	if on {
		action = eventActionPanicOn
	}
	if b.store == nil {
		msg := "panic mode state is unavailable"
		b.emitMonitorEvent("", action, eventKindError, "", msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	prior, found, err := b.store.Panic()
	if err != nil {
		msg := fmt.Sprintf("panic mode failed: %v", err)
		b.emitMonitorEvent("", action, eventKindError, "", msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	if err := b.store.SetPanic(on, state.SourceWeb); err != nil {
		msg := fmt.Sprintf("panic mode failed: %v", err)
		b.emitMonitorEvent("", action, eventKindError, "", msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	if found && prior.On == on {
		msg := "panic mode already on"
		if !on {
			msg = "panic mode already off"
		}
		b.emitMonitorEvent("", action, eventKindSuppressed, "", msg)
		return web.ActionResult{OK: true, Message: msg}
	}
	msg := "panic mode enabled: hooks, alerts and automatic remediation suspended"
	if !on {
		msg = "panic mode disabled: normal operation resumed"
	}
	b.emitMonitorEvent("", action, eventKindAction, eventStatusOK, msg)
	return web.ActionResult{OK: true, Message: msg}
}

// Operations returns current operation-slot usage and the active-user count.
func (b *WebBackend) Operations(_ context.Context) web.OperationSlots {
	users := notify.ActiveUserCount()
	if b.opGate == nil {
		return web.OperationSlots{ActiveUsers: users}
	}
	inUse, total := b.opGate.Usage()
	return web.OperationSlots{InUse: inUse, Total: total, ActiveUsers: users}
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
		out := web.MetricSeries{Check: check, Since: since.String(), Unit: metrics.MetricUnitMilliseconds}
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

// ApplicationEvents returns one application's recent monitoring events
// (firing/recovered/notify on the App dimension); ok is false for unknown apps.
func (b *WebBackend) ApplicationEvents(_ context.Context, name string, limit int) ([]web.Event, bool) {
	if !b.knownApp(name) {
		return nil, false
	}
	if b.events == nil {
		return nil, true
	}
	return toWebEvents(b.events.RecentApp(name, limit)), true
}

func (b *WebBackend) knownApp(name string) bool {
	if name == "" || b.cfg == nil {
		return false
	}
	for _, n := range b.cfg.CatalogNamesInCategory(config.CategoryApp) {
		if n == name {
			return true
		}
	}
	return false
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
		App:     e.App,
		Kind:    e.Kind,
		Rule:    e.Rule,
		Action:  e.Action,
		Status:  e.Status,
		Message: e.Message,
		Output:  e.Output,
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
		msg := unknownServiceMessage + name
		if b.emit != nil {
			b.emit(Event{Service: name, Kind: eventKindError, Action: action, Message: msg})
		}
		return web.ActionResult{OK: false, Message: msg}
	}
	if e.disabled {
		msg := "service " + name + " is disabled in configuration"
		if b.emit != nil {
			b.emit(Event{Service: name, Kind: eventKindError, Action: action, Message: msg})
		}
		return web.ActionResult{OK: false, Message: msg}
	}
	if action == string(rules.ActionReload) && !e.canReload {
		msg := "service " + name + " does not support reload"
		if b.emit != nil {
			b.emit(Event{Service: name, Kind: eventKindError, Action: action, Message: msg})
		}
		return web.ActionResult{OK: false, Message: msg}
	}

	var r operation.Result
	if opts.NoCascade || action == string(rules.ActionReload) || action == string(rules.ActionResume) || len(e.alsoApply) == 0 {
		r = b.operationResultWithMonitor(ctx, name, action)
	} else {
		lookup := func(svc string) []string {
			ent := b.entries[svc]
			if ent == nil {
				return nil
			}
			return ent.alsoApply
		}
		c := cascader{
			op:     b.operationResultWithMonitor,
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

func (b *WebBackend) operationResultWithMonitor(ctx context.Context, name, action string) operation.Result {
	if err := beginOperationSettling(b.operationSettling, name, action, state.SourceWeb); err != nil {
		b.emitMonitorEvent(name, action, eventKindError, "", err.Error())
	}
	r := b.operationResult(ctx, name, action)
	activeAfterStart := b.manualActionActiveAfterStart(ctx, name, action, r)
	change, err := SyncManualActionMonitoringWithActive(b.store, name, action, r, state.SourceWebManualStop, state.SourceWeb, activeAfterStart)
	if err != nil {
		b.emitMonitorEvent(name, action, eventKindError, "", err.Error())
	} else if change.Changed {
		b.emitMonitorEvent(name, change.Action, eventKindAction, eventStatusOK, change.Message)
	}
	if err := finishOperationSettlingWithActive(b.operationSettling, name, action, state.SourceWeb, r, nil, activeAfterStart); err != nil {
		b.emitMonitorEvent(name, action, eventKindError, "", err.Error())
	}
	return r
}

func (b *WebBackend) manualActionActiveAfterStart(ctx context.Context, name, action string, result operation.Result) bool {
	if result.Status != operation.ResultPostflightFailed || !manualStartLikeAction(action) {
		return false
	}
	e := b.entries[name]
	if e == nil {
		return false
	}
	return e.backendStatus(ctx, b.webNow()) == string(servicemgr.StatusActive)
}

func (b *WebBackend) operationResult(ctx context.Context, name, action string) operation.Result {
	e := b.entries[name]
	if e == nil {
		return operation.Result{Service: name, Action: action, Status: operation.ResultFailed, Message: unknownServiceMessage + name}
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
		b.emitWatchExpandEvent(name, eventKindExpandFailed, eventStatusFailed, msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	if w.disabled {
		msg := fmt.Sprintf("watch %q is disabled in configuration", name)
		b.emitWatchExpandEvent(name, eventKindExpandSkipped, eventStatusBlocked, msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	if !isStorageCheckType(w.checkType) {
		msg := fmt.Sprintf("watch %q is %q, not storage", name, w.checkType)
		b.emitWatchExpandEvent(name, eventKindExpandSkipped, eventStatusBlocked, msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	if w.expand == nil {
		msg := fmt.Sprintf("watch %q has no then.expand action configured", name)
		b.emitWatchExpandEvent(name, eventKindExpandSkipped, eventStatusBlocked, msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	path := cfgval.AsString(w.check[checks.CheckKeyPath])
	if path == "" {
		msg := fmt.Sprintf("watch %q storage check has no path", name)
		b.emitWatchExpandEvent(name, eventKindExpandFailed, eventStatusFailed, msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	expander := b.expander
	if expander == nil {
		msg := "volume expander is unavailable"
		b.emitWatchExpandEvent(name, eventKindExpandFailed, eventStatusFailed, msg)
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
		b.emitWatchExpandEvent(name, eventKindExpandFailed, eventStatusFailed, msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	msg := expandSuccessMessage(path, res)
	b.emitWatchExpandEvent(name, eventKindExpand, eventStatusOK, msg)
	return web.ActionResult{OK: true, Message: msg}
}

// SetMonitored enables or disables monitoring for a service.
func (b *WebBackend) SetMonitored(_ context.Context, name string, monitored bool) error {
	action := eventActionMonitor
	if !monitored {
		action = eventActionUnmonitor
	}
	if _, ok := b.entries[name]; !ok {
		msg := fmt.Sprintf(unknownServiceMessageFmt, name)
		b.emitMonitorEvent(name, action, eventKindError, "", msg)
		return fmt.Errorf("%s", msg)
	}
	if b.store == nil {
		msg := eventMessageMonitoringStateUnavailable
		b.emitMonitorEvent(name, action, eventKindError, "", msg)
		return fmt.Errorf("%s", msg)
	}
	priorActive, found, err := b.store.Active(name)
	if err != nil {
		msg := fmt.Sprintf("%s failed: %v", action, err)
		b.emitMonitorEvent(name, action, eventKindError, "", msg)
		return fmt.Errorf("%s", msg)
	}
	if err := b.store.SetActive(name, monitored, state.SourceWeb); err != nil {
		msg := fmt.Sprintf("%s failed: %v", action, err)
		b.emitMonitorEvent(name, action, eventKindError, "", msg)
		return fmt.Errorf("%s", msg)
	}
	if found && priorActive == monitored {
		msg := eventMessageAlreadyMonitored
		if !monitored {
			msg = eventMessageAlreadyPaused
		}
		b.emitMonitorEvent(name, action, eventKindSuppressed, "", msg)
		return nil
	}
	msg := eventMessageMonitoringResumed
	if !monitored {
		msg = eventMessageMonitoringPaused
	}
	b.emitMonitorEvent(name, action, eventKindAction, eventStatusOK, msg)
	return nil
}

// SetWatchMonitored enables or disables monitoring for a host watch.
func (b *WebBackend) SetWatchMonitored(_ context.Context, name string, monitored bool) error {
	action := eventActionMonitor
	if !monitored {
		action = eventActionUnmonitor
	}
	if _, ok := b.watches[name]; !ok {
		msg := fmt.Sprintf("unknown watch %q", name)
		b.emitWatchMonitorEvent(name, action, eventKindError, "", msg)
		return fmt.Errorf("%s", msg)
	}
	if b.store == nil {
		msg := eventMessageMonitoringStateUnavailable
		b.emitWatchMonitorEvent(name, action, eventKindError, "", msg)
		return fmt.Errorf("%s", msg)
	}
	key := watchMonitorKey(name)
	priorActive, found, err := b.store.Active(key)
	if err != nil {
		msg := fmt.Sprintf("%s failed: %v", action, err)
		b.emitWatchMonitorEvent(name, action, eventKindError, "", msg)
		return fmt.Errorf("%s", msg)
	}
	if err := b.store.SetActive(key, monitored, state.SourceWeb); err != nil {
		msg := fmt.Sprintf("%s failed: %v", action, err)
		b.emitWatchMonitorEvent(name, action, eventKindError, "", msg)
		return fmt.Errorf("%s", msg)
	}
	if found && priorActive == monitored {
		msg := eventMessageAlreadyMonitored
		if !monitored {
			msg = eventMessageAlreadyPaused
		}
		b.emitWatchMonitorEvent(name, action, eventKindSuppressed, "", msg)
		return nil
	}
	msg := eventMessageMonitoringResumed
	if !monitored {
		msg = eventMessageMonitoringPaused
	}
	b.emitWatchMonitorEvent(name, action, eventKindAction, eventStatusOK, msg)
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
		Action:  eventActionExpand,
		Status:  status,
		Message: message,
	})
}
