package app

import (
	"context"
	"fmt"
	"time"

	"sermo/internal/config"
	"sermo/internal/locks"
	"sermo/internal/web"
)

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
	report, err := locksScanner(cfg).Scan(service)
	if err != nil {
		return locks.Report{Service: service}, fmt.Errorf("scan locks for %s: %w", service, err)
	}
	return report, nil
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
	for i := range report.Locks {
		if report.Locks[i].State != locks.StateActive {
			continue
		}
		name := report.Locks[i].Name
		if name == "" {
			name = watchDefaultLockName
		}
		names = append(names, name)
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
		entry := b.entries[name]
		if entry == nil || entry.disabled {
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

// Locks returns the active and stale runtime locks across services.
func (b *WebBackend) Locks(_ context.Context) []web.Lock {
	var out []web.Lock
	now := b.webNow()
	reports := b.lockReportsByService()
	for _, name := range b.order {
		e := b.entries[name]
		if e == nil || e.disabled {
			continue
		}
		report := reports[name]
		for i := range report.Locks {
			out = append(out, lockToWebAt(report.Locks[i], name, now))
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

// lockToWebAt maps one runtime lock to its API view using the caller's shared
// observation time for age and remaining-TTL fields.
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
