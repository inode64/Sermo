package app

import (
	"context"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
	"sermo/internal/web"
	"slices"
	"strings"
	"time"
)

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
	status, statusAt := e.backendStatusSnapshot(ctx, b.webNow())
	svc.Status = status
	if !statusAt.IsZero() {
		svc.StatusObservedAt = statusAt.UTC().Format(time.RFC3339)
	}
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
		for i := range report.Locks {
			d.Locks = append(d.Locks, lockToWeb(report.Locks[i], name))
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
