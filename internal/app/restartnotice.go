package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"sermo/internal/config"
	"sermo/internal/metrics"
	"sermo/internal/notify"
	"sermo/internal/process"
	"sermo/internal/state"
)

const (
	restartNoticeRule              = "service-restart"
	restartNoticeRuntimeService    = "${restart.service}"
	restartNoticeRuntimeUnit       = "${restart.unit}"
	restartNoticeRuntimeProcess    = "${restart.process}"
	restartNoticeRuntimePID        = "${restart.pid}"
	restartNoticeRuntimeUptime     = "${restart.uptime}"
	restartNoticeRuntimeUptimeSecs = "${restart.uptime_seconds}"
	restartNoticeRuntimeStartedAt  = "${restart.started_at}"
	restartNoticeRuntimeThreshold  = "${restart.threshold}"

	sermoEnvEvent              = sermoEnvPrefix + "EVENT"
	sermoEnvRestartService     = sermoEnvPrefix + "RESTART_SERVICE"
	sermoEnvRestartUnit        = sermoEnvPrefix + "RESTART_UNIT"
	sermoEnvRestartProcess     = sermoEnvPrefix + "RESTART_PROCESS"
	sermoEnvRestartPID         = sermoEnvPrefix + "RESTART_PID"
	sermoEnvRestartUptime      = sermoEnvPrefix + "RESTART_UPTIME"
	sermoEnvRestartUptimeSecs  = sermoEnvPrefix + "RESTART_UPTIME_SECONDS"
	sermoEnvRestartStartedAt   = sermoEnvPrefix + "RESTART_STARTED_AT"
	sermoEnvRestartUptimeBelow = sermoEnvPrefix + "RESTART_UPTIME_BELOW"
)

// servicePrimaryProcess is the trusted process whose uptime represents one
// service restart. It is deliberately not the oldest member of a service tree:
// a long-lived worker must not hide a restarted systemd MainPID.
type servicePrimaryProcess struct {
	process   process.Process
	startedAt time.Time
}

// primaryProcessForCycle combines shared per-cycle discovery with one precise
// principal-process start-time read. It never broadens process attribution: a
// service without a backend MainPID, pidfile, or explicit main selector simply
// has no restart-notice candidate.
func primaryProcessForCycle(procs func() []process.Process, starts processStartReader, now func() time.Time) func() (servicePrimaryProcess, bool) {
	now = clockOrNow(now)
	return func() (servicePrimaryProcess, bool) {
		if procs == nil || starts == nil {
			return servicePrimaryProcess{}, false
		}
		principal, ok := selectPrimaryProcess(procs())
		if !ok {
			return servicePrimaryProcess{}, false
		}
		started, ok := starts.ProcessStartTime(principal.PID)
		if !ok || started.IsZero() || started.After(now()) {
			return servicePrimaryProcess{}, false
		}
		return servicePrimaryProcess{process: principal, startedAt: started}, true
	}
}

// selectPrimaryProcess chooses only an unambiguous principal identity. Systemd
// and container backends seed their MainPID first; a top-level pidfile or a
// pidfiles.main entry is the next strongest evidence; explicit process
// selectors may opt in with the name "main". A generic process tree root, or a
// pidfile explicitly assigned to a non-main role, is intentionally not guessed.
func selectPrimaryProcess(procs []process.Process) (process.Process, bool) {
	for _, p := range procs {
		if p.Source == process.SourceBackend && p.Role == process.RoleMain {
			return p, true
		}
	}
	for _, p := range procs {
		if p.Source == process.SelectorPidfile && (p.Role == process.RoleMain || p.Role == process.SelectorPidfile) {
			return p, true
		}
	}
	for _, p := range procs {
		if p.Source == process.SelectorCommandMatch && p.Role == process.RoleMain {
			return p, true
		}
	}
	return process.Process{}, false
}

//nolint:ireturn // The reader is intentionally injectable so tests can control process start times.
func primaryStartReader(collector *metrics.Collector) processStartReader {
	if collector != nil {
		if reader, ok := collector.Reader.(processStartReader); ok {
			return reader
		}
	}
	return metrics.OSReader{}
}

// observeServiceRestart records a young main process before delivering an
// external-restart notice. Persisting first makes this one-shot even when a
// notifier or sermod itself is restarted. During a Sermo operation the same
// identity is recorded but delivery is suppressed, preventing a later false
// external-restart alert after the operation-settling cycle completes.
func (w *Worker) observeServiceRestart(ctx context.Context, mode workerCycleMode) {
	notice := w.RestartNotice
	if notice == nil || notice.UptimeBelow <= 0 || w.PrimaryProcess == nil {
		return
	}
	principal, ok := w.PrimaryProcess()
	if !ok {
		return
	}
	now := clockOrNow(w.Now)
	at := now()
	uptime := at.Sub(principal.startedAt)
	if uptime < 0 || uptime >= notice.UptimeBelow {
		return
	}

	store := w.ServiceRestartNotice
	if store == nil {
		w.emitStoreError(restartNoticeRule+"-state", "service restart notice requires persistent state")
		return
	}
	record := state.ServiceRestartNoticeRecord{PID: principal.process.PID, StartedAt: principal.startedAt}
	previous, found, err := store.ServiceRestartNotice(w.Service)
	if err != nil {
		w.emitStoreError(restartNoticeRule+"-load", err.Error())
		return
	}
	if found && sameServiceRestartNotice(previous, record) {
		return
	}
	if err := store.SetServiceRestartNotice(w.Service, record); err != nil {
		w.emitStoreError(restartNoticeRule+"-save", err.Error())
		return
	}
	if mode.operation {
		return
	}
	w.emitServiceRestartNotice(ctx, *notice, principal, uptime)
}

func sameServiceRestartNotice(a, b state.ServiceRestartNoticeRecord) bool {
	return a.PID == b.PID && a.StartedAt.Equal(b.StartedAt)
}

func (w *Worker) emitServiceRestartNotice(ctx context.Context, notice config.ServiceRestartNotice, principal servicePrimaryProcess, uptime time.Duration) {
	message := w.expandServiceRestartNotice(notice.Message, notice, principal, uptime)
	subject := w.expandServiceRestartNotice(notice.Subject, notice, principal, uptime)
	w.emit(Event{Kind: eventKindAlert, Rule: restartNoticeRule, Message: message})
	if w.InPanic != nil && w.InPanic() {
		w.emit(Event{Kind: eventKindNotifySuppressed, Rule: restartNoticeRule, Message: "panic mode: service restart notification suppressed"})
		return
	}
	allow := func(notify.Notifier) bool { return true }
	if w.DryRun {
		allow = dryRunConsoleNotifier
	}
	for _, n := range resolveNotifiers(notice.Notify, w.Notifiers) {
		if !allow(n) {
			continue
		}
		if err := n.Send(ctx, serviceRestartMessage(w.Service, w.Unit, subject, message, notice, principal, uptime)); err != nil {
			w.emit(Event{Kind: eventKindNotifyFail, Rule: restartNoticeRule, Message: n.Name() + ": " + err.Error()})
		} else {
			w.emit(Event{Kind: eventKindNotify, Rule: restartNoticeRule, Message: "notified " + n.Name()})
		}
	}
}

func (w *Worker) expandServiceRestartNotice(text string, notice config.ServiceRestartNotice, principal servicePrimaryProcess, uptime time.Duration) string {
	startedAt, uptimeText, uptimeSeconds := serviceRuntimeUptime(principal.startedAt, principal.startedAt.Add(uptime))
	processName := primaryProcessName(principal.process)
	text = strings.NewReplacer(
		restartNoticeRuntimeService, w.Service,
		restartNoticeRuntimeUnit, w.Unit,
		restartNoticeRuntimeProcess, processName,
		restartNoticeRuntimePID, strconv.Itoa(principal.process.PID),
		restartNoticeRuntimeUptime, uptimeText,
		restartNoticeRuntimeUptimeSecs, strconv.FormatInt(uptimeSeconds, 10),
		restartNoticeRuntimeStartedAt, startedAt,
		restartNoticeRuntimeThreshold, notice.UptimeBelow.String(),
	).Replace(text)
	return w.expandRuntime(text, Event{Service: w.Service, Rule: restartNoticeRule})
}

func primaryProcessName(p process.Process) string {
	if p.Exe != "" {
		return p.Exe
	}
	if len(p.Cmdline) > 0 {
		return p.Cmdline[0]
	}
	if p.Role != "" {
		return p.Role
	}
	return fmt.Sprintf("pid %d", p.PID)
}

func serviceRestartMessage(service, unit, subject, body string, notice config.ServiceRestartNotice, principal servicePrimaryProcess, uptime time.Duration) notify.Message {
	startedAt, uptimeText, uptimeSeconds := serviceRuntimeUptime(principal.startedAt, principal.startedAt.Add(uptime))
	processName := primaryProcessName(principal.process)
	return notify.Message{
		Subject: subject,
		Body:    body,
		Fields: map[string]string{
			sermoEnvService:            service,
			sermoEnvRule:               restartNoticeRule,
			sermoEnvEvent:              restartNoticeRule,
			sermoEnvRestartService:     service,
			sermoEnvRestartUnit:        unit,
			sermoEnvRestartProcess:     processName,
			sermoEnvRestartPID:         strconv.Itoa(principal.process.PID),
			sermoEnvRestartUptime:      uptimeText,
			sermoEnvRestartUptimeSecs:  strconv.FormatInt(uptimeSeconds, 10),
			sermoEnvRestartStartedAt:   startedAt,
			sermoEnvRestartUptimeBelow: notice.UptimeBelow.String(),
		},
	}
}
