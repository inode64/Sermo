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
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/diag"
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

// serviceRuntime builds the per-service runtime pieces shared by a worker and the
// web backend: a process discoverer, the check deps (with a backend-status
// closure), and the safe operation engine. The engine's per-service operation
// lock serializes start/stop/restart across the worker and the web.
func serviceRuntime(name, unit string, tree map[string]any, deps Deps, recordOperation func(operation.Result)) (operation.Engine, checks.Deps, process.Discoverer) {
	discoverer := process.NewDiscoverer()
	discoverer.BackendPIDs = servicemgr.BackendPIDsFunc(deps.Backend, unit)
	checkDeps := checks.Deps{
		Service:        name,
		DefaultTimeout: deps.DefaultTimeout,
		Status: func(ctx context.Context) (servicemgr.Status, error) {
			st, err := deps.Manager.Status(ctx, unit)
			if err != nil {
				return "", err
			}
			return st.Status, nil
		},
		Processes:    discoverer.ObserveState,
		DiskUsage:    deps.DiskUsage,
		MountSampler: deps.MountSampler,
	}
	locker := configureOperationLocker(deps.Runtime, operationLockReclaimEvent(deps.Emit))
	engine := operation.New(operation.Config{
		Service:          name,
		Unit:             unit,
		Backend:          string(deps.Backend),
		Tree:             tree,
		Manager:          deps.Manager,
		Locker:           &locker,
		Scanner:          locks.NewScanner(filepath.Join(deps.Runtime, "locks")),
		Discoverer:       discoverer,
		CheckDeps:        checkDeps,
		Sleep:            deps.Sleep,
		OperationTimeout: deps.OperationTimeout,
		Emit:             recordOperation,
	})
	return engine, checkDeps, discoverer
}

// webEntry is one service's web-backend record.
type webEntry struct {
	displayName    string
	unit           string
	backend        string
	interval       time.Duration // resolved per-service cycle cadence (own interval or engine default)
	policyCooldown time.Duration
	engine         operation.Engine
	status         func(context.Context) (servicemgr.Status, error)
	checkNames     []string          // sorted
	checkTypes     map[string]string // check name -> type
	discoverer     process.Discoverer
	selectors      []process.Selector
	disabled       bool // true when the service had `enabled: false` (still listed for visibility)
}

// webWatch is a configured host watch for UI visibility (services may be 0).
type webWatch struct {
	name          string
	checkType     string
	interval      time.Duration
	disabled      bool
	monitorMode   string
	fireOnFail    bool
	hasHook       bool
	hookCommand   []string
	notifiers     []string
	notifierCount int
	check         map[string]any
	expand        *ExpandSpec
}

// webNotifier is a configured notification target (used by watches).
type webNotifier struct {
	name    string
	typ     string
	enabled bool
}

// WebBackend implements web.Backend over the daemon's services: status from the
// backend, monitoring state and SLA from the store, the latest check results from
// the shared snapshots, and start/stop/restart through the same safe operation
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
	host             diag.Host
	measure          MeasurementReader
	collector        *metrics.Collector
	diskUsage        checks.DiskUsageFunc
	mountSampler     checks.MountSamplerFunc
	expander         VolumeExpander
	emit             func(Event)
	opGate           *OpGate
	defaultTimeout   time.Duration
	operationTimeout time.Duration
}

// NewWebBackend resolves services for the web UI. All services present in the
// loaded configuration are included in the listing (even those with `enabled: false`)
// so that the dashboard can show the full fleet and let operators see what can be
// activated (by editing the service file and reloading). Only non-disabled services
// get a full runtime engine, checks, and operation support.
func NewWebBackend(cfg *config.Config, deps Deps) (*WebBackend, []string) {
	wb := &WebBackend{
		entries:   map[string]*webEntry{},
		watches:   map[string]*webWatch{},
		notifiers: map[string]*webNotifier{},
		store:     deps.Monitor, snapshots: deps.Snapshots,
		events: deps.Events, remediation: deps.Remediation, ruleWindows: deps.RuleWindows,
		cfg: cfg, host: diag.OSHost{},
		collector: deps.Collector,
		diskUsage: deps.DiskUsage, mountSampler: deps.MountSampler,
		expander: configuredVolumeExpander(deps),
		emit:     deps.Emit, opGate: deps.OpGate, defaultTimeout: deps.DefaultTimeout, operationTimeout: deps.OperationTimeout,
	}
	wb.sla, _ = deps.SLA.(SLAReader)
	wb.measure, _ = deps.SLA.(MeasurementReader)
	wb.diagStore, _ = deps.Monitor.(diag.Store)
	var warnings []string
	resolver := servicemgr.NewUnitResolver()

	for _, name := range serviceNames(cfg) {
		doc := cfg.Services[name]
		if doc == nil {
			continue
		}
		resolved, errs := cfg.Resolve(name)
		if len(errs) > 0 {
			warnings = append(warnings, "skip service "+name+": "+errs[0])
			continue
		}
		disabled := isDisabled(doc.Body)
		base := config.ServiceUnit(resolved.Tree, name)
		candidates, trust := config.ServiceCandidates(resolved.Tree, string(deps.Backend), name)
		unit, err := resolver.Resolve(context.Background(), deps.Backend, candidates, trust)
		if err != nil {
			unit = base
		}
		iv := durationField(resolved.Tree["interval"])
		if iv <= 0 {
			iv = EngineInterval(cfg, 30*time.Second)
		}
		entry := &webEntry{
			displayName:    config.DisplayName(resolved.Tree, name),
			unit:           unit,
			backend:        string(deps.Backend),
			interval:       iv,
			policyCooldown: rules.ParsePolicy(resolved.Tree).Cooldown,
		}
		if disabled {
			entry.disabled = true
		} else {
			engine, checkDeps, discoverer := serviceRuntime(name, unit, resolved.Tree, deps, operationEventEmitter(deps.Emit))
			selectors, _ := process.ParseSelectors(resolved.Tree)
			names, types := checkCatalog(resolved.Tree)
			entry.engine = engine
			entry.status = checkDeps.Status
			entry.checkNames = names
			entry.checkTypes = types
			entry.discoverer = discoverer
			entry.selectors = selectors
		}
		wb.entries[name] = entry
		wb.order = append(wb.order, name)
	}

	// Also surface host watches in the web UI (including disabled ones). This is
	// important when services=0 but watches=N (the main dashboard would otherwise
	// be empty). We read the raw global watches section (same source BuildWatches
	// uses) so listing is independent of whether the watch runner is active.
	if raw, ok := cfg.Global.Raw["watches"].(map[string]any); ok && len(raw) > 0 {
		for _, name := range slices.Sorted(maps.Keys(raw)) {
			entry, _ := raw[name].(map[string]any)
			disabled := isDisabled(entry)
			ctype := ""
			fireOnFail := false
			if ce, ok := entry["check"].(map[string]any); ok {
				ctype = canonicalWatchCheckType(cfgval.AsString(ce["type"]))
			}
			fireOnFail = isHealthCheckType(ctype)
			iv := durationField(entry["interval"])
			if iv <= 0 {
				iv = 30 * time.Second
			}
			hasHook := false
			var hookCommand []string
			var notifierNames []string
			var expand *ExpandSpec
			if then, ok := entry["then"].(map[string]any); ok {
				if h, ok := then["hook"].(map[string]any); ok && len(h) > 0 {
					if cmd := h["command"]; cmd != nil {
						hookCommand = cfgval.StringArray(cmd)
						hasHook = len(hookCommand) > 0
					}
				}
				notifierNames = effectiveNotify(cfgval.StringList(then["notify"]), deps.GlobalNotify)
				if parsed, err := parseExpand(then, ctype); err != nil {
					warnings = append(warnings, "watch "+name+": "+err.Error())
				} else {
					expand = parsed
				}
			}
			ww := &webWatch{
				name:          name,
				checkType:     ctype,
				interval:      iv,
				disabled:      disabled,
				monitorMode:   config.MonitorMode(entry),
				fireOnFail:    fireOnFail,
				hasHook:       hasHook,
				hookCommand:   hookCommand,
				notifiers:     notifierNames,
				notifierCount: len(notifierNames),
				check:         checkMap(entry),
				expand:        expand,
			}
			wb.watches[name] = ww
			wb.watchOrder = append(wb.watchOrder, name)
		}
	}

	// Surface configured notifiers (useful to know what watches can notify to).
	if raw, ok := cfg.Global.Raw["notifiers"].(map[string]any); ok && len(raw) > 0 {
		for _, name := range slices.Sorted(maps.Keys(raw)) {
			entry, _ := raw[name].(map[string]any)
			typ := cfgval.AsString(entry["type"])
			wn := &webNotifier{name: name, typ: typ, enabled: notify.Enabled(entry)}
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
	svc := web.Service{
		Name:        name,
		DisplayName: e.displayName,
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
	if ev := b.lastServiceEvent(name); ev != nil {
		svc.LastEvent = ev
	}
	if e.disabled {
		svc.Status = "disabled"
		svc.State = ServiceState(false, false, svc.Status, "")
		svc.Monitored = false
		svc.CheckHealth = ""
		svc.RemediationState = "disabled"
		return svc
	}
	status := "unknown"
	if e.status != nil {
		if st, err := e.status(ctx); err != nil {
			status = "error"
		} else {
			status = string(st)
		}
	}
	svc.Status = status
	if b.store != nil {
		if rec, found, err := b.store.MonitorState(name); err == nil && found {
			svc.Monitored = rec.Active
			svc.MonitorSource = rec.Source
			if !rec.UpdatedAt.IsZero() {
				svc.MonitorChangedAt = rec.UpdatedAt.UTC().Format(time.RFC3339)
			}
		}
	}
	failing, health := checkHealthSummary(b.snapshots.Get(name), e.checkNames, svc.Monitored)
	svc.CheckHealth = health
	if failing > 0 {
		svc.ChecksFailing = failing
	}
	if locks := activeLockNames(b.cfg, name); len(locks) > 0 {
		svc.ActiveLocks = locks
	}
	b.decorateRemediation(name, &svc)
	svc.State = ServiceState(svc.Enabled, svc.Monitored, svc.Status, svc.CheckHealth)
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
		if cs.Skipped || cs.Optional || cs.OK {
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
	for _, name := range b.order {
		out = append(out, b.view(ctx, name, b.entries[name]))
	}
	return out
}

// Watches returns the configured host watches, including disabled ones.
func (b *WebBackend) Watches(ctx context.Context) []web.Watch {
	if len(b.watchOrder) == 0 {
		return nil
	}
	out := make([]web.Watch, 0, len(b.watchOrder))
	for _, name := range b.watchOrder {
		w := b.watches[name]
		if w == nil {
			continue
		}
		iv := formatInterval(w.interval)
		var disk *web.DiskWatchInfo
		if isStorageCheckType(w.checkType) {
			disk = diskWatchInfo(w, b)
		}
		monitorMode := w.monitorMode
		if monitorMode == "" {
			monitorMode = config.MonitorEnabled
		}
		ww := web.Watch{
			Name:          w.name,
			CheckType:     w.checkType,
			Summary:       watchSummary(w, disk),
			Interval:      iv,
			Enabled:       !w.disabled,
			Monitor:       monitorMode,
			Monitored:     !w.disabled && monitorMode != config.MonitorDisabled,
			FireOnFail:    w.fireOnFail,
			HasHook:       w.hasHook,
			HookCommand:   slices.Clone(w.hookCommand),
			Notifiers:     slices.Clone(w.notifiers),
			NotifierCount: w.notifierCount,
			Conditions:    watchConditions(w.check),
			Disk:          disk,
		}
		if w.expand != nil {
			ww.Expand = &web.WatchExpand{ByBytes: w.expand.By}
		}
		if !w.disabled && b.store != nil {
			if rec, found, err := b.store.MonitorState(watchMonitorKey(name)); err == nil && found {
				ww.Monitored = rec.Active
				ww.MonitorSource = rec.Source
				if !rec.UpdatedAt.IsZero() {
					ww.MonitorChangedAt = rec.UpdatedAt.UTC().Format(time.RFC3339)
				}
			}
		}
		// Compute last activity for this watch from the event log (best effort)
		if b.events != nil {
			for _, e := range b.events.Recent("", 200) { // small scan is fine for UI
				if e.Watch == name && isWatchActivityKind(e.Kind) {
					ww.LastActivity = e.Time.Format(time.RFC3339)
					ww.LastActivityKind = e.Kind
					break
				}
			}
		}
		ww.State = WatchState(ww.Enabled, ww.Monitored, watchViewFailed(ww))
		out = append(out, ww)
	}
	return out
}

func watchViewFailed(w web.Watch) bool {
	if WatchActivityFailed(w.LastActivityKind) {
		return true
	}
	return w.Disk != nil && (w.Disk.SampleError != "" || w.Disk.MountSampleError != "")
}

func isWatchActivityKind(kind string) bool {
	switch kind {
	case "hook", "notify", "hook-failed", "notify-failed", "expand", "expand-skipped", "expand-failed":
		return true
	default:
		return false
	}
}

func checkMap(entry map[string]any) map[string]any {
	check, _ := entry["check"].(map[string]any)
	return check
}

func watchSummary(w *webWatch, disk *web.DiskWatchInfo) string {
	if w == nil {
		return ""
	}
	if isStorageCheckType(w.checkType) && disk != nil {
		if disk.SampleError != "" {
			return disk.Path + ": " + disk.SampleError
		}
		fs := disk.FileSystem
		if fs == "" {
			fs = "filesystem"
		}
		return fmt.Sprintf("%s: %.1f%% free (%d bytes) on %s", disk.Path, disk.FreePct, disk.FreeBytes, fs)
	}
	conds := watchConditions(w.check)
	if len(conds) == 0 {
		return ""
	}
	parts := make([]string, 0, len(conds))
	for _, c := range conds {
		parts = append(parts, strings.TrimSpace(c.Field+" "+c.Op+" "+c.Value))
	}
	return strings.Join(parts, ", ")
}

func watchConditions(check map[string]any) []web.WatchCondition {
	if check == nil {
		return nil
	}
	var out []web.WatchCondition
	for _, field := range []string{"used_pct", "free_pct", "used_bytes", "free_bytes", "inodes_used_pct", "inodes_free_pct", "inodes_free"} {
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
	if v, ok := check["mounted"].(bool); ok {
		out = append(out, web.WatchCondition{Field: "mounted", Op: "==", Value: fmt.Sprintf("%t", v)})
	}
	return out
}

func diskWatchInfo(w *webWatch, b *WebBackend) *web.DiskWatchInfo {
	if w == nil || w.check == nil {
		return nil
	}
	path := cfgval.String(w.check["path"])
	if path == "" {
		return nil
	}
	info := &web.DiskWatchInfo{Path: path}

	usage := b.diskUsage
	if usage == nil {
		usage = checks.DefaultDiskUsage
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
		})
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

	if b.cfg != nil {
		g := b.cfg.Global
		info.ConfigPath = g.Path
		info.RuntimeDir = g.RuntimeDir()
		info.StateDir = g.StateDir()

		// Engine block (effective values with documented fallbacks)
		info.Interval = formatInterval(EngineInterval(b.cfg, 30*time.Second))
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

// osPrettyName returns a human-friendly OS label (PRETTY_NAME from os-release on
// Linux, e.g. "Debian GNU/Linux 12 (bookworm)"), falling back to runtime.GOOS.
func osPrettyName() string {
	for _, path := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "PRETTY_NAME="); ok {
				if name := strings.Trim(v, `"'`); name != "" {
					return name
				}
			}
		}
	}
	return runtime.GOOS
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
	// Nice display order
	order := []string{"load1", "load5", "load15", "total_cpu", "total_memory", "total_swap"}
	seen := map[string]bool{}
	for _, k := range order {
		if r, ok := snap[k]; ok {
			m := web.HostMetric{Name: k, Ready: r.Ready}
			if r.HasPercent {
				m.Percent = r.Percent
			}
			if r.HasAbsolute {
				m.Absolute = r.Absolute
			}
			if k == "total_memory" || k == "total_swap" {
				m.Unit = "bytes"
			}
			out = append(out, m)
			seen[k] = true
		}
	}
	// Add any others
	for k, r := range snap {
		if seen[k] {
			continue
		}
		m := web.HostMetric{Name: k, Ready: r.Ready}
		if r.HasPercent {
			m.Percent = r.Percent
		}
		if r.HasAbsolute {
			m.Absolute = r.Absolute
		}
		out = append(out, m)
	}
	return out
}

// Locks returns the active and stale runtime locks across services.
func (b *WebBackend) Locks(ctx context.Context) []web.Lock {
	var out []web.Lock
	now := time.Now()
	for _, name := range b.order {
		e := b.entries[name]
		if e == nil || e.disabled {
			continue
		}
		report, err := serviceLocksReport(b.cfg, name)
		if err != nil {
			continue
		}
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
		case e.Kind == "action" && (e.Action == "start" || e.Action == "stop" || e.Action == "restart"):
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
func (b *WebBackend) MonitoringStatus(ctx context.Context) web.MonitoringStatus {
	svcs := b.Services(ctx) // this already includes the live Monitored flag from store
	total := len(svcs)
	monitored := 0
	for _, s := range svcs {
		if s.Monitored {
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
		return web.Detail{Service: b.view(ctx, name, e)}, true
	}
	d := web.Detail{Service: b.view(ctx, name, e)}

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
			Ran:      seen && cs.Ran,
		}
		if seen && !cs.At.IsZero() {
			ch.At = cs.At.UTC().Format(time.RFC3339)
		}
		for _, m := range checks.GraphMetrics(e.checkTypes[cn]) {
			ch.Metrics = append(ch.Metrics, web.CheckMetric{Name: m.Key, Unit: m.Unit})
		}
		d.Checks = append(d.Checks, ch)
	}

	if b.sla != nil {
		if vals, err := b.sla.SLAReport(name, time.Now()); err == nil {
			for _, v := range vals {
				win := web.SLAWindow{Window: v.Window, Up: v.Up, Total: v.Total}
				if ratio, ok := v.Ratio(); ok {
					win.Ratio = &ratio
				}
				d.SLA = append(d.SLA, win)
			}
		}
	}

	if report, err := serviceLocksReport(b.cfg, name); err == nil {
		for _, lk := range report.Locks {
			d.Locks = append(d.Locks, lockToWeb(lk, name))
		}
		if len(report.Warnings) > 0 {
			d.LockWarnings = append([]string(nil), report.Warnings...)
		}
	}

	procs, procWarnings := e.discoverer.Discover(e.selectors)
	if len(procWarnings) > 0 {
		d.ProcessWarnings = append([]string(nil), procWarnings...)
	}
	d.Processes, d.ProcessTotals = aggregateProcesses(procs, metrics.OSReader{})

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

// ConfigRender returns a fully resolved service config for operator review.
func (b *WebBackend) ConfigRender(ctx context.Context, name, format string) (web.ConfigRender, bool, error) {
	if _, ok := b.entries[name]; !ok {
		return web.ConfigRender{}, false, nil
	}
	resolved, errs := b.cfg.Resolve(name)
	if len(errs) > 0 {
		return web.ConfigRender{}, true, fmt.Errorf("resolve %s: %s", name, strings.Join(errs, "; "))
	}
	data, err := renderResolvedConfig(resolved, format)
	if err != nil {
		return web.ConfigRender{}, true, err
	}
	return web.ConfigRender{
		Name:        name,
		Format:      format,
		Content:     string(data),
		SourceFiles: b.configSources(name),
	}, true, nil
}

// ConfigDiff compares two fully resolved service configs line-by-line.
func (b *WebBackend) ConfigDiff(ctx context.Context, base, service string) (web.ConfigDiff, bool, error) {
	if _, ok := b.entries[base]; !ok {
		return web.ConfigDiff{}, false, nil
	}
	if _, ok := b.entries[service]; !ok {
		return web.ConfigDiff{}, false, nil
	}
	baseRender, ok, err := b.ConfigRender(ctx, base, "yaml")
	if !ok || err != nil {
		return web.ConfigDiff{}, ok, err
	}
	serviceRender, ok, err := b.ConfigRender(ctx, service, "yaml")
	if !ok || err != nil {
		return web.ConfigDiff{}, ok, err
	}
	removed, added := lineDiff(baseRender.Content, serviceRender.Content)
	return web.ConfigDiff{
		Base:      base,
		Service:   service,
		Identical: len(removed) == 0 && len(added) == 0,
		Removed:   removed,
		Added:     added,
	}, true, nil
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
		w.BlockedActions = []string{"start", "stop", "restart"}
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
	events := b.events.Recent(name, 1)
	if len(events) == 0 {
		return nil
	}
	ev := loggedEventToWeb(events[0])
	return &ev
}

// Operate runs a start/stop/restart/monitor action on a service.
func (b *WebBackend) Operate(ctx context.Context, name, action string) web.ActionResult {
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
	run := func(ctx context.Context) operation.Result {
		switch action {
		case "start":
			return e.engine.Start(ctx)
		case "stop":
			return e.engine.Stop(ctx)
		case "restart":
			return e.engine.Restart(ctx)
		case "reload":
			return e.engine.Reload(ctx)
		default:
			return operation.Result{Service: name, Action: action, Status: operation.ResultFailed, Message: "unknown action " + action}
		}
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
	msg := r.Message
	if msg == "" {
		msg = string(r.Status)
	}
	return web.ActionResult{OK: r.OK(), Message: msg}
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

func renderResolvedConfig(resolved config.Resolved, format string) ([]byte, error) {
	switch format {
	case "json":
		data, err := config.RenderJSON(resolved)
		if err != nil {
			return nil, fmt.Errorf("render %s as json: %w", resolved.Name, err)
		}
		return data, nil
	default:
		data, err := config.RenderYAML(resolved)
		if err != nil {
			return nil, fmt.Errorf("render %s as yaml: %w", resolved.Name, err)
		}
		return data, nil
	}
}

func (b *WebBackend) configSources(name string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(path string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	add(b.cfg.Global.Path)
	var addService func(string)
	addService = func(service string) {
		doc := b.cfg.Services[service]
		if doc == nil {
			return
		}
		if clone := webScalarString(doc.Body["clone"]); clone != "" {
			addService(clone)
		}
		if uses := webScalarString(doc.Body["uses"]); uses != "" {
			if daemon := b.cfg.Daemons[uses]; daemon != nil {
				add(daemon.Path)
			}
		}
		add(doc.Path)
	}
	addService(name)
	return out
}

func lineDiff(base, other string) (removed, added []string) {
	baseSet := lineCount(base)
	otherSet := lineCount(other)
	for _, l := range strings.Split(strings.TrimRight(base, "\n"), "\n") {
		if otherSet[l] == 0 && !slices.Contains(removed, l) {
			removed = append(removed, l)
		}
	}
	for _, l := range strings.Split(strings.TrimRight(other, "\n"), "\n") {
		if baseSet[l] == 0 && !slices.Contains(added, l) {
			added = append(added, l)
		}
	}
	return removed, added
}

func lineCount(s string) map[string]int {
	out := map[string]int{}
	for _, l := range strings.Split(s, "\n") {
		out[l]++
	}
	return out
}

func webScalarString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int:
		return fmt.Sprint(x)
	case int64:
		return fmt.Sprint(x)
	case float64:
		return fmt.Sprint(x)
	default:
		return ""
	}
}
