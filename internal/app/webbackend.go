package app

import (
	"context"
	"maps"
	"slices"
	"sort"
	"sync"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/control"
	"sermo/internal/execx"
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
	// catalogInspectionParallelism bounds concurrent version/health probes so a
	// cold dashboard load is faster without spawning one command per entry.
	catalogInspectionParallelism = 4
	// serviceStatusCacheTTL bounds how often the web list re-queries systemd/OpenRC.
	// The dashboard refreshes every 30s by default, so keep status warm across
	// ordinary refreshes instead of running one init status probe per service.
	serviceStatusCacheTTL = 2 * time.Minute
	// serviceInitQueryTimeout bounds every init-system query from the web
	// backend, including status polling and reload-capability detection.
	serviceInitQueryTimeout = 2 * time.Second
	processPIDListLimit     = 20
)

const (
	remediationStatePaused   = TargetStatePaused
	remediationStatePending  = "pending"
	remediationStateEligible = "eligible"
	remediationStateBlocked  = "blocked"
)

const (
	backendStatusError        = "error"
	watchReadingFieldError    = "error"
	watchReadingFieldCPUTicks = "cpu_ticks"
	watchReadingFieldMatches  = "matches"
	watchReadingFieldProcess  = checks.CheckTypeProcess
	watchReadingFieldResult   = checks.DataKeyResult
	watchReadingFieldRSS      = "rss"
	// watchReadingFieldRAIDArrayPrefix namespaces the per-array reading fields
	// (one reading per RAID member, suffixed with the array name).
	watchReadingFieldRAIDArrayPrefix = "raid_array_"
	watchReadingFieldSample          = "sample"
	watchCategoryFallback            = config.WatchCategoryWatch
	watchReadingFieldState           = checks.CheckKeyState
	watchReadingFieldUser            = checks.CheckKeyUser
	watchReadingStateActive          = string(servicemgr.StatusActive)
	watchReadingStateBaseline        = "baseline"
	watchReadingStateMissing         = "missing"
	lockOwnerStatusLive              = "live"
	lockReleaseDefaultRule           = "default"
	unknownServiceMessage            = "unknown service "
	unknownServiceMessageFmt         = unknownServiceMessage + "%q"
	unknownWatchMessageFmt           = "unknown watch %q"
	watchReadingValueNone            = "none"
	watchReadingValueUnknown         = checks.NetStateUnknown
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
	checkIntervals    map[string]time.Duration
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
	raidControl   bool
	// serviceScoped marks a watch declared inside a service's `watches:` section
	// (named "<service>:<watch>"). It is listed and controllable like any watch,
	// but does not support manual host-watch probes.
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
	notifierRegistry  map[string]notify.Notifier
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
	daemonMetrics     *DaemonMetricSampler
	serviceMetrics    *ServiceMetricSampler
	live              *LiveMetrics
	mountSampler      checks.MountSamplerFunc
	raidSampler       checks.RaidSamplerFunc
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

	probeMu sync.Mutex
	probes  map[string]time.Time

	applications catalogInventoryCache
	libraries    catalogInventoryCache

	slaCacheMu sync.Mutex
	slaCache   map[slaCacheKey]cachedSLATimelines

	mountUsageMu     sync.Mutex
	mountUsageAt     time.Time
	mountUsage       map[string][]process.Process
	mountUsageErrors map[string]string

	mountOperationsMu sync.Mutex
	mountOperations   map[string]web.MountOperation
}

// DashboardSnapshot collects every reload-sensitive dashboard section from one
// backend generation. The holder calls it after taking one pointer, so a reload
// cannot combine services from one configuration with daemon data from another.
func (b *WebBackend) DashboardSnapshot(ctx context.Context, since time.Duration) web.DashboardSnapshot {
	var snapshot web.DashboardSnapshot
	var wg sync.WaitGroup
	run := func(fn func()) {
		wg.Go(fn)
	}

	run(func() { snapshot.Services = b.Services(ctx) })
	run(func() { snapshot.Mounts = b.Mounts(ctx) })
	run(func() { snapshot.Notifiers = b.Notifiers(ctx) })
	run(func() { snapshot.Daemon = b.DaemonInfo(ctx) })
	run(func() { snapshot.DaemonMetrics = b.DaemonMetrics(ctx, since) })
	run(func() { snapshot.Locks = b.Locks(ctx) })
	run(func() { snapshot.Activity = b.ActivitySummary(ctx) })
	run(func() { snapshot.Monitoring = b.MonitoringStatus(ctx) })
	run(func() { snapshot.Operations = b.Operations(ctx) })
	run(func() { snapshot.HostMetrics = b.HostMetrics(ctx) })
	wg.Wait()
	return snapshot
}

func (b *WebBackend) maxOperationTimeout() time.Duration {
	if b == nil {
		return 0
	}
	if b.cfg == nil {
		return b.operationTimeout
	}
	return MaxOperationTimeout(b.cfg, b.operationTimeout)
}

// NewWebBackend resolves services for the web UI. All services present in the
// loaded configuration are included in the listing (even those with `enabled: false`)
// so that the dashboard can show the full fleet and let operators see what can be
// activated (by editing the service file and reloading). Only non-disabled services
// get a full runtime engine, checks, and operation support.
func NewWebBackend(ctx context.Context, cfg *config.Config, deps Deps) (*WebBackend, []string) {
	if deps.UserLookup == nil {
		deps.UserLookup = EngineUserLookup(cfg, deps.ExecxRunner)
	}
	operationSettling := deps.OperationSettling
	if operationSettling == nil {
		if store, ok := deps.Monitor.(OperationSettlingStore); ok {
			operationSettling = store
		}
	}
	daemonMetrics := deps.DaemonMetricSampler
	if daemonMetrics == nil {
		daemonMetrics = NewDaemonMetricSampler(deps.Collector, deps.Now, deps.DaemonMetrics)
	}
	wb := &WebBackend{
		entries:           map[string]*webEntry{},
		watches:           map[string]*webWatch{},
		notifiers:         map[string]*webNotifier{},
		notifierRegistry:  deps.Notifiers,
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
		daemonMetrics:     daemonMetrics,
		serviceMetrics:    deps.ServiceMetrics,
		live:              deps.Live,
		mountSampler:      deps.MountSampler,
		raidSampler:       deps.RaidSampler,
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
		probes:            map[string]time.Time{},
	}
	if wb.serviceMetrics == nil {
		wb.serviceMetrics = NewServiceMetricSampler()
	}
	wb.sla, _ = deps.SLA.(SLAReader)
	wb.measure, _ = deps.SLA.(MeasurementReader)
	resolver := servicemgr.NewUnitResolver()
	resolver.Manager = deps.Manager

	names := cfg.SortedServiceNames()
	warnings := make([]string, 0, len(names))
	for _, name := range names {
		warnings = append(warnings, wb.registerService(ctx, cfg, name, resolver, deps)...)
	}

	warnings = append(warnings, wb.registerHostWatches(cfg, deps)...)
	wb.registerNotifiers(cfg)

	return wb, warnings
}

// registerService resolves one configured service and adds its entry (and any
// service-scoped watches) to the backend. Disabled services are still listed,
// but only enabled ones get a runtime engine.
func (b *WebBackend) registerService(ctx context.Context, cfg *config.Config, name string, resolver servicemgr.UnitResolver, deps Deps) []string {
	doc := cfg.Services[name]
	if doc == nil {
		return nil
	}
	resolved, errs := cfg.Resolve(name)
	if len(errs) > 0 {
		return []string{"skip service " + name + ": " + errs[0]}
	}
	var warnings []string
	disabled := cfgval.Disabled(doc.Body)
	target, warn := control.ResolveWithFallback(ctx, name, resolved.Tree, deps.Backend, deps.Manager, resolver)
	if warn != "" {
		warnings = append(warnings, serviceSubjectPrefix+name+": "+warn)
	}
	if target.Unit == "" {
		return warnings
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
		warnings = append(warnings, attachServiceRuntime(ctx, entry, name, resolved.Tree, target, deps)...)
	}
	b.entries[name] = entry
	b.order = append(b.order, name)

	b.registerServiceWatches(name, resolved.Tree, deps.GlobalNotify, iv, disabled)
	return warnings
}

// attachServiceRuntime wires an enabled service's entry with its operation
// engine, checks, process discovery, and reload capability.
func attachServiceRuntime(ctx context.Context, entry *webEntry, name string, tree map[string]any, target control.Target, deps Deps) []string {
	serviceDeps := deps
	serviceDeps.Backend = target.Backend
	serviceDeps.Manager = target.Manager
	serviceDeps.BackendPIDs = target.BackendPIDs
	engine, checkDeps, discoverer := serviceRuntime(ctx, name, target.Unit, tree, serviceDeps, map[string]string{}, operationEventEmitter(deps.Emit))
	selectors, processWarnings := serviceProcessSelectors(ctx, tree, serviceDeps, target.Unit)
	names, types, intervals := checkCatalog(tree, entry.interval)
	entry.noResidentProcess = serviceNoResidentProcess(tree, selectors, serviceBackendPIDs(ctx, serviceDeps, target.Unit))
	entry.engine = engine
	entry.status = checkDeps.Status
	entry.checkNames = names
	entry.checkTypes = types
	entry.checkIntervals = intervals
	entry.discoverer = discoverer
	entry.selectors = selectors
	entry.processWarnings = processWarnings
	reloadCtx, cancel := context.WithTimeout(ctx, serviceInitQueryTimeout)
	canReload, reloadErr := operation.ReloadSupported(reloadCtx, tree, target.Manager, target.Unit)
	cancel()
	entry.canReload = canReload
	if reloadErr != nil {
		return []string{serviceSubjectPrefix + name + ": reload support unavailable: " + reloadErr.Error()}
	}
	return nil
}

func (b *WebBackend) registerServiceWatches(service string, tree map[string]any, globalNotify []string, interval time.Duration, disabled bool) {
	watches, ok := tree[config.SectionWatches].(map[string]any)
	if !ok {
		return
	}
	for _, name := range slices.Sorted(maps.Keys(watches)) {
		entry, ok := watches[name].(map[string]any)
		if !ok || reservedServiceWatchName(name) || unsupportedServiceWatchType(entry) != "" {
			continue
		}
		fullName := service + ":" + name
		watch, _ := newWebWatch(fullName, entry, globalNotify, interval, true)
		if disabled {
			watch.disabled = true
		}
		b.watches[fullName] = watch
		b.watchOrder = append(b.watchOrder, fullName)
	}
}

func (b *WebBackend) registerHostWatches(cfg *config.Config, deps Deps) []string {
	raw, _ := cfg.ResolveWatches()
	if len(raw) == 0 {
		return nil
	}
	var warnings []string
	for _, name := range slices.Sorted(maps.Keys(raw)) {
		entry, _ := raw[name].(map[string]any)
		watch, warn := newWebWatch(name, entry, deps.GlobalNotify, config.DefaultEngineInterval, false)
		if warn != "" {
			warnings = append(warnings, watchSubjectPrefix+name+": "+warn)
		}
		b.watches[name] = watch
		b.watchOrder = append(b.watchOrder, name)
	}
	return warnings
}

func (b *WebBackend) registerNotifiers(cfg *config.Config) {
	for _, name := range slices.Sorted(maps.Keys(cfg.Notifiers())) {
		entry, _ := cfg.Notifiers()[name].(map[string]any)
		typ := cfgval.AsString(entry[notify.KeyType])
		b.notifiers[name] = &webNotifier{name: name, typ: typ, enabled: notify.Enabled(entry), summary: notify.ConfigSummary(typ, entry)}
		b.notifierOrder = append(b.notifierOrder, name)
	}
}

// newWebWatch builds the web-listing model for one watch entry, shared by host
// watches and service-embedded watches. defaultInterval is used when the entry
// sets no interval; serviceScoped marks it as a service watch (manual probing
// disabled). warn is a non-empty message when the expand action is malformed.
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
	raidControl := false
	if raidControlConfig, ok := entry[config.WatchKeyRAIDControl].(map[string]any); ok {
		raidControl = cfgval.Bool(raidControlConfig[config.RAIDControlKeyPauseResume])
	}
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
		raidControl:   raidControl,
		serviceScoped: serviceScoped,
	}, warn
}

// checkCatalog returns a service's check names (sorted), types and effective
// intervals from the resolved `checks` section.
func checkCatalog(tree map[string]any, defaultInterval time.Duration) ([]string, map[string]string, map[string]time.Duration) {
	section, ok := tree[config.SectionChecks].(map[string]any)
	if !ok {
		return nil, nil, nil
	}
	types := make(map[string]string, len(section))
	intervals := make(map[string]time.Duration, len(section))
	names := make([]string, 0, len(section))
	for name, raw := range section {
		typ := ""
		if m, ok := raw.(map[string]any); ok {
			typ, _ = m[checks.CheckKeyType].(string)
			intervals[name] = effectiveCheckInterval(cfgval.Duration(m[config.EntryKeyInterval]), defaultInterval)
		} else {
			intervals[name] = defaultInterval
		}
		types[name] = typ
		names = append(names, name)
	}
	sort.Strings(names)
	return names, types, intervals
}
