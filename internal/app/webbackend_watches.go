package app

import (
	"context"
	"fmt"
	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/metrics"
	"sermo/internal/servicemgr"
	"sermo/internal/units"
	"sermo/internal/web"
	"slices"
	"strings"
	"time"
)

// Watches returns configured host-level and service-scoped watches, including
// disabled ones.
func (b *WebBackend) Watches(_ context.Context) []web.Watch {
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
		out = append(out, b.watchView(w, system, lastActivities[name]))
	}
	return out
}

func (b *WebBackend) watchView(w *webWatch, system metrics.Snapshot, activity watchActivity) web.Watch {
	observation := watchObservation{
		snapshots: b.watchSnapshots.Get(w.name, w.checkType),
		at:        b.webNow(), available: b.watchSnapshots != nil,
	}
	storage, swap, meter, readings, summary := observation.watchPresentation(w, system)
	monitorMode := w.monitorMode
	if monitorMode == "" {
		monitorMode = config.MonitorEnabled
	}
	scope := web.WatchScopeHost
	if w.serviceScoped {
		scope = web.WatchScopeService
	}
	view := web.Watch{
		Name: w.name, Scope: scope, DisplayName: w.displayName, Category: w.category, CheckType: w.checkType,
		SummaryConfigured: cfgval.String(w.check[checks.CheckKeySummary]) != "",
		Interval:          units.HumanizeDuration(w.interval), Enabled: !w.disabled, Monitor: monitorMode,
		Monitored: !w.disabled && monitorMode != config.MonitorDisabled, FireOnFail: w.fireOnFail,
		HasHook: len(w.hookCommand) > 0, HookCommand: slices.Clone(w.hookCommand), Notifiers: slices.Clone(w.notifiers),
		NotifierCount: len(w.notifiers), DryRun: w.dryRun, Conditions: watchConditions(w.check, w.metrics),
		Storage: storage, Swap: swap, Meter: meter, Readings: readings,
		CanProbe:       !w.disabled && !w.serviceScoped && ManualProbeCheckType(w.checkType),
		CanControlRAID: !w.disabled && w.raidControl, RAIDArray: cfgval.String(w.check[checks.CheckKeyArray]),
		CanControlReplication: !w.disabled && w.replicationControl && w.checkType == checks.CheckTypeReplication,
	}
	view.Summary = watchSummary(w, storage, summary, view.Conditions)
	b.applyWatchRuntimeView(&view, w, activity, observation)
	return view
}

func (o watchObservation) watchPresentation(w *webWatch, system metrics.Snapshot) (*web.StorageWatchInfo, *web.SwapWatchInfo, *web.WatchMeter, []web.WatchReading, string) {
	if w.disabled {
		return nil, nil, nil, nil, ""
	}
	var storage *web.StorageWatchInfo
	if isStorageCheckType(w.checkType) {
		storage = o.storageWatchInfo(w)
	}
	var swap *web.SwapWatchInfo
	if w.checkType == checks.CheckTypeSwap {
		swap = swapWatchInfo(system)
	}
	meter, readings, summary := o.watchSnapshotView(w, system)
	return storage, swap, meter, readings, summary
}

func (b *WebBackend) applyWatchRuntimeView(view *web.Watch, w *webWatch, activity watchActivity, observation watchObservation) {
	if w.expand != nil {
		view.Expand = &web.WatchExpand{ByBytes: w.expand.By}
	}
	var changedAt time.Time
	if !w.disabled {
		if monitoredState, ok := b.monitorView(WatchMonitorKey(w.name)); ok {
			changedAt = monitoredState.changedAt
			view.Monitored, view.MonitorSource, view.MonitorChangedAt = monitoredState.active, monitoredState.source, monitoredState.changedAtText()
		}
	}
	checkedAt := observation.watchLastCheckedAt(w)
	if !checkedAt.IsZero() {
		view.LastCheckedAt = checkedAt.Format(time.RFC3339)
	}
	if startedAt, running := b.watchProbeStartedAt(w.name); running {
		view.Probe = &web.WatchProbe{State: eventStatusRunning, StartedAt: startedAt.Format(time.RFC3339)}
	}
	if !activity.At.IsZero() {
		view.LastActivity, view.LastActivityKind = activity.At.UTC().Format(time.RFC3339), activity.Kind
	}
	if view.Enabled && view.Monitored {
		view.SampleState = observation.watchSampleState(w, checkedAt)
	}
	view.KeepsSLA = !w.disabled && watchRecordsAvailability(w)
	// Start from the same declaration the recorder persists from, then narrow a
	// device-dependent check to the attributes this device actually publishes.
	if !w.disabled {
		view.Metrics = webCheckMetricsForReadings(w.checkType, w.graphs, w.bands, view.Readings)
		observation.setWatchCurrentMetricValues(view, w)
	}
	observed := b.settling == nil || b.settling.Observed(SettlingWatchKey(w.name))
	failed, warning := watchViewState(w, *view, activity.At, changedAt)
	view.State = WatchState(view.Enabled, view.Monitored, observed && failed, observed && warning, observed)
	if view.State == TargetStateOK {
		switch view.SampleState {
		case web.WatchSampleStateCollecting:
			view.State = TargetStateCollecting
		case web.WatchSampleStateStale:
			view.State = TargetStateStale
		}
	}
	if deviceState := watchDeviceState(view.Readings); deviceState != "" && view.Enabled && view.Monitored && observed {
		view.State = deviceState
	}
}

// setWatchCurrentMetricValues projects the same fresh daemon snapshots the
// watch row just rendered into its metric declarations. It reads only the
// in-memory snapshot registry: the web request never starts a second probe.
func (o watchObservation) setWatchCurrentMetricValues(view *web.Watch, w *webWatch) {
	if view == nil || len(view.Metrics) == 0 {
		return
	}
	for _, snap := range o.snapshots {
		if !o.watchSnapshotCurrent(w, snap) || !watchSnapshotMetricConfigured(w, snap) {
			continue
		}
		setCurrentMetricValues(view.Metrics, snap.Data)
	}
}

func watchDeviceState(readings []web.WatchReading) string {
	if i := slices.IndexFunc(readings, func(reading web.WatchReading) bool {
		return reading.Field == checks.DataKeyDeviceState && reading.Error == ""
	}); i >= 0 {
		return readings[i].Value
	}
	return ""
}

func (b *WebBackend) watchSystemSnapshot() metrics.Snapshot {
	if b.collector == nil {
		return nil
	}
	return b.collector.SampleSystem()
}

type watchActivity struct {
	At   time.Time
	Kind string
}

func (b *WebBackend) lastServiceEvents() map[string]*web.Event {
	if b.events == nil {
		return nil
	}
	out := map[string]*web.Event{}
	for _, ev := range b.events.Page(0, activitySummaryEventScanLimit) {
		if b.entries[ev.Service] == nil || out[ev.Service] != nil {
			continue
		}
		webEv := loggedEventToWeb(ev)
		out[ev.Service] = &webEv
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
			At:   ev.Time,
			Kind: ev.Kind,
		}
	}
	return out
}

func (e *webEntry) backendStatusSnapshot(ctx context.Context, now time.Time) (string, time.Time) {
	if e == nil || e.status == nil {
		return string(servicemgr.StatusUnknown), time.Time{}
	}
	e.statusMu.Lock()
	defer e.statusMu.Unlock()
	if !e.statusAt.IsZero() && now.Sub(e.statusAt) < serviceStatusCacheTTL {
		return e.cachedStatus, e.statusAt
	}
	statusCtx, cancel := context.WithTimeout(ctx, serviceInitQueryTimeout)
	defer cancel()
	st, err := e.status(statusCtx)
	if err != nil {
		if ctx.Err() != nil {
			// The viewer cancelled the request mid-probe (e.g. closed the tab).
			// Don't poison the shared cache with "error" for everyone else;
			// keep the previous entry and let the next poll retry.
			if !e.statusAt.IsZero() {
				return e.cachedStatus, e.statusAt
			}
			return string(servicemgr.StatusUnknown), time.Time{}
		}
		e.cachedStatus = backendStatusError
	} else {
		e.cachedStatus = string(st)
	}
	e.statusAt = now
	return e.cachedStatus, e.statusAt
}

func (e *webEntry) invalidateStatusCache() {
	if e == nil {
		return
	}
	e.statusMu.Lock()
	e.statusAt = time.Time{}
	e.statusMu.Unlock()
}

// watchViewState grades a watch row: an outage, an advisory, or neither. The two
// signals are the last activity kind — an advisory watch records its own kind, so
// this stays right per metric and across a restart — and the published readings,
// where an advisory reports through Warning instead of Error.
//
// The readings are the newer signal. A firing episode announces itself once, so
// when the check has since regraded the same episode an advisory — a RAID
// member whose state recovered while its error counters remain, or a daemon
// upgraded to a build that grades SMART predicates — the newest snapshot carries
// a warning row and no error row, and that outranks the firing kind that opened
// the episode. A failed hook or notification stays an outage regardless.
func watchViewState(w *webWatch, view web.Watch, activityAt, changedAt time.Time) (failed, warning bool) {
	current := watchActivityCurrent(activityAt, changedAt)
	if WatchActivityFailed(view.LastActivityKind) && current {
		regraded := view.LastActivityKind == eventKindFiring && watchReadingsWarning(view.Readings) && !watchReadingsFailed(view.Readings)
		if !regraded {
			return true, false
		}
	}
	if watchStorageMountFailed(w, view.Storage) {
		return true, false
	}
	if view.Storage != nil && (view.Storage.SampleError != "" || view.Storage.MountSampleError != "") {
		return true, false
	}
	if watchReadingsFailed(view.Readings) {
		return true, false
	}
	return false, (view.LastActivityKind == eventKindWarning && current) || watchReadingsWarning(view.Readings)
}

func watchStorageMountFailed(w *webWatch, storage *web.StorageWatchInfo) bool {
	if storage == nil {
		return false
	}
	expect, ok := storageMountExpectation(w.check)
	return ok && storage.Mounted != expect
}

func watchActivityCurrent(activityAt, changedAt time.Time) bool {
	return activityAt.IsZero() || changedAt.IsZero() || !activityAt.Before(changedAt)
}

func watchReadingsFailed(readings []web.WatchReading) bool {
	return slices.ContainsFunc(readings, func(reading web.WatchReading) bool { return reading.Error != "" })
}

func watchReadingsWarning(readings []web.WatchReading) bool {
	return slices.ContainsFunc(readings, func(reading web.WatchReading) bool { return reading.Warning != "" })
}

func isWatchActivityKind(kind string) bool {
	switch kind {
	case eventKindFiring, eventKindWarning, eventKindRecovered, eventKindDryRun, eventKindHook, eventKindNotify, eventKindHookFail, eventKindNotifyFail, eventKindExpand, eventKindExpandSkipped, eventKindExpandFailed, eventKindKill, eventKindKillFailed,
		eventKindMakeStep, eventKindMakeStepSkipped, eventKindMakeStepFailed:
		return true
	default:
		return false
	}
}

func watchSummary(w *webWatch, storage *web.StorageWatchInfo, liveSummary string, conds []web.WatchCondition) string {
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
		if !storageUsagePredicatesConfigured(w.check) {
			if storage.Mounted {
				return fmt.Sprintf("%s: mounted on %s", storage.Path, fs)
			}
			return storage.Path + ": not mounted as expected"
		}
		return fmt.Sprintf("%s: %.1f%% free (%s) on %s", storage.Path, storage.FreePct, checks.HumanizeSignedBytes(uintToInt64(storage.FreeBytes)), fs)
	}
	if liveSummary != "" {
		return liveSummary
	}
	if len(conds) == 0 {
		return ""
	}
	parts := make([]string, 0, len(conds))
	for _, c := range conds {
		parts = append(parts, watchConditionText(c))
	}
	return strings.Join(parts, displayListSeparator)
}

// watchRecordsAvailability reports whether this watch keeps an availability
// series. A multi-metric watch declares its metrics beside the check and the
// daemon expands it into one watch per metric, so it counts when any of them is
// the availability metric.
func watchRecordsAvailability(w *webWatch) bool {
	return checks.ConfiguredRecordsAvailability(w.checkType, w.check, w.metrics)
}
