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
	"strconv"
	"strings"
	"time"
)

// Watches returns the configured host watches, including disabled ones.
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
	storage, swap, meter, readings, summary := b.watchPresentation(w, system)
	monitorMode := w.monitorMode
	if monitorMode == "" {
		monitorMode = config.MonitorEnabled
	}
	view := web.Watch{
		Name: w.name, DisplayName: w.displayName, Category: w.category, CheckType: w.checkType,
		Summary: watchSummary(w, storage, summary), SummaryConfigured: cfgval.String(w.check[checks.CheckKeySummary]) != "",
		Interval: units.HumanizeDuration(w.interval), Enabled: !w.disabled, Monitor: monitorMode,
		Monitored: !w.disabled && monitorMode != config.MonitorDisabled, FireOnFail: w.fireOnFail,
		HasHook: w.hasHook, HookCommand: slices.Clone(w.hookCommand), Notifiers: slices.Clone(w.notifiers),
		NotifierCount: w.notifierCount, DryRun: w.dryRun, Conditions: watchConditions(w.check, w.metrics),
		Storage: storage, Swap: swap, Meter: meter, Readings: readings,
		CanProbe:       !w.disabled && !w.serviceScoped && manualProbeCheckType(w.checkType),
		CanControlRAID: !w.disabled && w.raidControl, RAIDArray: cfgval.String(w.check[checks.CheckKeyArray]),
	}
	b.applyWatchRuntimeView(&view, w, activity)
	return view
}

func (b *WebBackend) watchPresentation(w *webWatch, system metrics.Snapshot) (*web.StorageWatchInfo, *web.SwapWatchInfo, *web.WatchMeter, []web.WatchReading, string) {
	if w.disabled {
		return nil, nil, nil, nil, ""
	}
	var storage *web.StorageWatchInfo
	if isStorageCheckType(w.checkType) {
		storage = b.storageWatchInfo(w)
	}
	var swap *web.SwapWatchInfo
	if w.checkType == checks.CheckTypeSwap {
		swap = swapWatchInfo(system)
	}
	meter, readings, summary := b.watchDashboardView(w, system)
	return storage, swap, meter, readings, summary
}

func (b *WebBackend) applyWatchRuntimeView(view *web.Watch, w *webWatch, activity watchActivity) {
	if w.expand != nil {
		view.Expand = &web.WatchExpand{ByBytes: w.expand.By}
	}
	if !w.disabled {
		if active, source, changed, ok := b.monitorView(watchMonitorKey(w.name)); ok {
			view.Monitored, view.MonitorSource, view.MonitorChangedAt = active, source, changed
		}
	}
	checkedAt := b.watchLastCheckedAt(w)
	if !checkedAt.IsZero() {
		view.LastCheckedAt = checkedAt.Format(time.RFC3339)
	}
	if startedAt, running := b.watchProbeStartedAt(w.name); running {
		view.Probe = &web.WatchProbe{State: eventStatusRunning, StartedAt: startedAt.Format(time.RFC3339)}
	}
	if activity.At != "" {
		view.LastActivity, view.LastActivityKind = activity.At, activity.Kind
	}
	if view.Enabled && view.Monitored {
		view.SampleState = b.watchSampleState(w, checkedAt)
	}
	observed := b.settling == nil || b.settling.Observed(SettlingWatchKey(w.name))
	view.State = WatchState(view.Enabled, view.Monitored, observed && watchViewFailed(*view), observed)
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

func watchDeviceState(readings []web.WatchReading) string {
	for _, reading := range readings {
		if reading.Field == checks.DataKeyDeviceState && reading.Error == "" {
			return reading.Value
		}
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
	status, _ := e.backendStatusSnapshot(ctx, now)
	return status
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
		if !storageUsagePredicatesConfigured(w.check) {
			if storage.Mounted {
				return fmt.Sprintf("%s: mounted on %s", storage.Path, fs)
			}
			return storage.Path + ": not mounted as expected"
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
