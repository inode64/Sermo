package app

import (
	"context"
	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
	"sermo/internal/units"
	"sermo/internal/web"
	"slices"
	"strings"
	"time"
)

func (b *WebBackend) view(ctx context.Context, name string, e *webEntry) web.Service {
	return b.viewWithEvent(ctx, name, e, b.lastServiceEvent(name))
}

func (b *WebBackend) viewWithEvent(ctx context.Context, name string, e *webEntry, lastEvent *web.Event) web.Service {
	return b.viewWithRuntime(ctx, name, e, lastEvent, serviceLockView{})
}

// serviceLockView carries one service's already-scanned lock state, so the
// batched Services path reads each lock directory once for the whole fleet
// instead of per row. ready distinguishes "scanned, nothing held" from "not
// scanned yet", which is what the single-service path reports.
type serviceLockView struct {
	active          []string
	operationActive bool
	ready           bool
}

func (b *WebBackend) viewWithRuntime(ctx context.Context, name string, e *webEntry, lastEvent *web.Event, lockView serviceLockView) web.Service {
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
		svc.Interval = units.HumanizeDuration(e.interval)
	}
	if e.policyCooldown > 0 {
		svc.PolicyCooldown = units.HumanizeDuration(e.policyCooldown)
	}
	svc.LastEvent = lastEvent
	if e.disabled {
		svc.Status = TargetStateDisabled
		svc.State = ServiceState(false, false, svc.Status, "", true, false, false, false)
		svc.Monitored = false
		svc.CheckHealth = ""
		svc.RemediationState = TargetStateDisabled
		return svc
	}
	monitorChangedAt := time.Time{}
	if active, source, changed, ok := b.monitorRecord(name); ok {
		svc.Monitored, svc.MonitorSource, monitorChangedAt = active, source, changed
		if !changed.IsZero() {
			svc.MonitorChangedAt = changed.UTC().Format(time.RFC3339)
		}
	}
	status, statusAt := e.backendStatusSnapshot(ctx, b.webNow())
	// A sermoctl action runs in a separate process, so it cannot invalidate this
	// backend's in-memory init-status cache directly. Its monitor-state transition
	// is persisted after the operation; a newer change is therefore the bounded
	// cross-process signal to refresh status before rendering the Web UI.
	if monitorChangedAt.After(statusAt) {
		e.invalidateStatusCache()
		status, statusAt = e.backendStatusSnapshot(ctx, b.webNow())
	}
	status, statusAt = b.freshServiceCheckStatus(name, e, status, statusAt)
	svc.Status = status
	if !statusAt.IsZero() {
		svc.StatusObservedAt = statusAt.UTC().Format(time.RFC3339)
	}
	failing, health := b.serviceCheckHealth(name, e, svc.Monitored)
	warningReason := b.serviceWarningReason(name, e)
	if health == TargetStateOK && warningReason != "" {
		health = checkHealthWarning
	}
	svc.CheckHealth = health
	if failing > 0 {
		svc.ChecksFailing = failing
	}
	svc.WarningReason = warningReason
	if !lockView.ready {
		lockView.active = activeLockNames(b.cfg, name)
		lockView.operationActive = operationActive(b.cfg, name)
	}
	if len(lockView.active) > 0 {
		svc.ActiveLocks = lockView.active
	}
	svc.OperationActive = lockView.operationActive
	b.decorateRemediation(name, &svc)
	observed := (b.settling == nil || b.settling.Observed(SettlingServiceKey(name))) && !b.operationSettlingPending(name)
	svc.ObservabilityReady, svc.ObservabilityMissing = b.serviceObservability(name, e, svc.Status, svc.CheckHealth, svc.Monitored, observed)
	svc.State = ServiceState(svc.Enabled, svc.Monitored, svc.Status, svc.CheckHealth, observed, svc.ObservabilityReady,
		b.serviceProcessActive(name, e), onlyMissingProcesses(svc.ObservabilityMissing))
	if len(e.alsoApply) > 0 {
		svc.AlsoApply = slices.Clone(e.alsoApply)
	}
	b.decorateServiceRuntime(name, e, &svc)
	return svc
}

// freshServiceCheckStatus prefers a newer service-check result over the web
// list's init-status cache. Service workers already query the same resolved
// unit, so this keeps a CLI or external restart from displaying the cache's
// former inactive state after monitoring has observed it active, without
// adding an init-system query to each dashboard request.
func (b *WebBackend) freshServiceCheckStatus(name string, e *webEntry, status string, statusAt time.Time) (string, time.Time) {
	if e == nil || b.snapshots == nil {
		return status, statusAt
	}
	snapshots := b.snapshots.Get(name)
	for _, check := range e.checkNames {
		if e.checkTypes[check] != checks.CheckTypeService {
			continue
		}
		snap, ok := snapshots[check]
		if !ok || !b.serviceCheckSnapshotCurrent(e, check, snap) || !snap.At.After(statusAt) {
			continue
		}
		observed := servicemgr.Status(cfgval.String(snap.Data[checks.DataKeyStatus]))
		if !normalizedServiceStatus(observed) {
			continue
		}
		return string(observed), snap.At
	}
	return status, statusAt
}

func normalizedServiceStatus(status servicemgr.Status) bool {
	switch status {
	case servicemgr.StatusActive, servicemgr.StatusInactive, servicemgr.StatusPaused, servicemgr.StatusFailed, servicemgr.StatusUnknown:
		return true
	default:
		return false
	}
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

	const observabilityMissingCapacity = 3

	missing := make([]string, 0, observabilityMissingCapacity)
	addMissing := func(label string) {
		if !slices.Contains(missing, label) {
			missing = append(missing, label)
		}
	}
	if len(e.checkNames) > 0 {
		snap := b.snapshots.Get(name)
		for _, check := range e.checkNames {
			cs, ok := snap[check]
			if !ok || !b.serviceCheckSnapshotCurrent(e, check, cs) {
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
		if !e.noResidentProcess {
			switch b.serviceRuntimeObservability(name, e) {
			case runtimeObservabilityEmpty:
				addMissing(observabilityMissingProcesses)
			case runtimeObservabilityPending:
				addMissing(observabilityMissingRuntime)
			case runtimeObservabilityReady:
			}
		}
	}
	if len(missing) > 0 {
		return false, missing
	}
	return true, nil
}

// serviceWarningReason returns the machine-readable cause of a service warning
// for the dashboard to phrase. It reads the stale-binary check's published
// snapshot rather than discovering processes: like serviceProcessActive below,
// the web request must not scan /proc — daemon cycles own the runtime evidence
// and its freshness boundary.
func (b *WebBackend) serviceWarningReason(name string, e *webEntry) string {
	if e == nil || e.checkTypes == nil {
		return ""
	}
	snap := b.snapshots.Get(name)
	for check, typ := range e.checkTypes {
		if typ != checks.CheckTypeStaleBinary {
			continue
		}
		if cs, ok := snap[check]; ok && b.serviceCheckSnapshotCurrent(e, check, cs) && !cs.OK {
			return warningReasonStaleBinary
		}
	}
	return ""
}

// onlyMissingProcesses reports whether the empty process tree is the sole
// indicator gap. Anything else in the list is genuinely still populating, so
// the service keeps reading as collecting until that settles and the blind
// spot is the only thing left.
func onlyMissingProcesses(missing []string) bool {
	return len(missing) == 1 && missing[0] == observabilityMissingProcesses
}

// checkView builds one check's detail row from its latest snapshot.
func (b *WebBackend) checkView(cn string, e *webEntry, snap map[string]CheckSnapshot) web.Check {
	cs, seen := snap[cn]
	current := seen && b.serviceCheckSnapshotCurrent(e, cn, cs)
	ch := web.Check{
		Name:    cn,
		Type:    e.checkTypes[cn],
		Reports: e.checkReports[cn],
		Stale:   seen && !current,
		Ran:     current && cs.Ran,
	}
	if current {
		// Availability, not the raw comparison: a condition check reports OK
		// when its threshold is crossed, so passing it through unchanged would
		// render a healthy sensor as "fail" next to a 100% SLA column. A
		// verdictless check has no availability to report, so it carries the
		// sensed state itself — that is what active/inactive renders.
		ch.OK = cs.healthy()
		if checks.VerdictlessMode(ch.Reports) {
			ch.OK = cs.OK
		}
		ch.Optional = cs.Optional
		ch.Skipped = cs.Skipped
		ch.Message = cs.Message
		ch.Readings = checkReadings(e.checkTypes[cn], cs.Data)
	}
	if seen && !cs.At.IsZero() {
		ch.At = cs.At.UTC().Format(time.RFC3339)
	}
	for _, m := range checks.DeclaredGraphMetrics(e.checkTypes[cn], e.checkUnits[cn]) {
		ch.Metrics = append(ch.Metrics, web.CheckMetric{Name: m.Key, Unit: m.Unit})
	}
	return ch
}

func (b *WebBackend) serviceCheckHealth(name string, e *webEntry, monitored bool) (int, string) {
	if e == nil {
		return 0, checkHealthUnknown
	}
	return checkHealthSummaryCurrent(b.snapshots.Get(name), e.checkNames, monitored,
		func(check string, snap CheckSnapshot) bool {
			return b.serviceCheckSnapshotCurrent(e, check, snap)
		},
		func(check string) bool { return checks.VerdictlessMode(e.checkReports[check]) })
}

// serviceCheckSnapshotCurrent accepts only a result produced by the configured
// check type and no older than two effective check intervals. A reload may keep
// a check name while changing either its type or cadence.
func (b *WebBackend) serviceCheckSnapshotCurrent(e *webEntry, name string, snap CheckSnapshot) bool {
	if e == nil || snap.At.IsZero() || snap.CheckType != e.checkTypes[name] {
		return false
	}
	interval := e.checkIntervals[name]
	if interval <= 0 {
		interval = e.interval
	}
	return b.webNow().Sub(snap.At) <= runtimePublishMaxAge(interval)
}

// runtimeObservability classifies the per-service runtime indicator. The split
// that matters is between "no answer yet" and "a complete answer of nothing":
// workers publish a sample every observed cycle, so a fresh sample counting
// zero processes is the daemon reporting that it looked and found none, not
// that the numbers are still on their way.
type runtimeObservability int

const (
	runtimeObservabilityPending runtimeObservability = iota
	runtimeObservabilityEmpty
	runtimeObservabilityReady
)

func (b *WebBackend) serviceRuntimeObservability(name string, e *webEntry) runtimeObservability {
	if e == nil || e.noResidentProcess || b.serviceMetrics == nil {
		return runtimeObservabilityReady
	}
	cur, at, ok := b.serviceMetrics.LatestWithAt(name)
	if !ok || b.webNow().Sub(at) > runtimePublishMaxAge(e.interval) {
		return runtimeObservabilityPending
	}
	if cur.Count == 0 {
		return runtimeObservabilityEmpty
	}
	// Processes are visible; the CPU and IO rates need a second sample to
	// derive, so they are genuinely still populating.
	if cur.HasCPU && cur.IOReady {
		return runtimeObservabilityReady
	}
	return runtimeObservabilityPending
}

// serviceProcessActive only reads the worker-published runtime sample. The web
// request must not scan /proc to turn an unobserved service into active: daemon
// cycles own the runtime evidence and its freshness boundary.
func (b *WebBackend) serviceProcessActive(name string, e *webEntry) bool {
	if e == nil || e.noResidentProcess || b.serviceMetrics == nil {
		return false
	}
	cur, at, ok := b.serviceMetrics.LatestWithAt(name)
	if !ok || b.webNow().Sub(at) > runtimePublishMaxAge(e.interval) {
		return false
	}
	return cur.StartedAt != ""
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
	switch {
	case rep.Allowed:
		svc.RemediationState = remediationStateEligible
	case rep.Reason != "":
		svc.RemediationState = rep.Reason
	default:
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

// checkHealthSummaryCurrent reports required-check health for the service list.
// It uses the same rule as SLA availability: a required, non-skipped check with
// OK=false counts as failing; optional failures are ignored. Paused services are
// "paused"; services with no observed checks yet are "unknown". current, when
// set, filters snapshots to the ones the running config still declares; nil
// keeps every snapshot.
// checkHealthSummaryCurrent counts the checks that make a service unhealthy.
// verdictless names the checks that assert nothing (`reports: state`/`value`);
// it comes from configuration because the snapshot carries no reporting mode.
func checkHealthSummaryCurrent(snap map[string]CheckSnapshot, checkNames []string, monitored bool, current func(string, CheckSnapshot) bool, verdictless func(string) bool) (failing int, health string) {
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
		if !seen || (current != nil && !current(name, cs)) {
			continue
		}
		observed = true
		if cs.Skipped || cs.Optional || (verdictless != nil && verdictless(name)) || cs.healthy() {
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
	operating := b.operationActiveByService()
	for _, name := range b.order {
		e := b.entries[name]
		if e == nil {
			continue
		}
		out = append(out, b.viewWithRuntime(ctx, name, e, lastEvents[name], serviceLockView{
			active:          activeLocks[name],
			operationActive: operating[name],
			ready:           true,
		}))
	}
	return out
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
	now := b.webNow()

	snap := b.snapshots.Get(name)
	for _, cn := range e.checkNames {
		d.Checks = append(d.Checks, b.checkView(cn, e, snap))
	}

	if report, err := serviceLocksReport(b.cfg, name); err == nil {
		for i := range report.Locks {
			d.Locks = append(d.Locks, lockToWebAt(report.Locks[i], name, now))
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
	if len(e.sshSessionFilters) > 0 {
		d.SSHSessionsSupported = true
		sessions, err := b.sshSessions(e.sshSessionFilters)
		if err != nil {
			d.ProcessWarnings = append(d.ProcessWarnings, sshSessionUnavailablePrefix+err.Error())
		} else {
			d.SSHSessions = sshSessionsToWeb(sessions)
		}
	}
	if terminalSessionsSupported(e) {
		d.TerminalSessionsSupported = true
		d.TerminalSessions = b.terminalSessions(e, snap)
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
