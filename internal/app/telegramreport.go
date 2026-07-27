package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"sermo/internal/state"
	"sermo/internal/telegrambot"
	"sermo/internal/web"
)

// Service check-health values surfaced by the web backend that the status
// rollup counts.
const (
	webCheckHealthOK      = "ok"
	webCheckHealthFailing = "failing"
)

// telegramSLAReader is the slice of the state store the bot needs for /sla.
type telegramSLAReader interface {
	SLAReport(service string, now time.Time) ([]state.SLAValue, error)
}

// telegramReporter adapts the reload-safe web backend and the state store into
// the read-only view the Telegram bot renders. It reuses exactly the data the
// web dashboard serves, so bot reports never diverge from the UI.
type telegramReporter struct {
	web *WebBackendHolder
	sla telegramSLAReader
	now func() time.Time
}

// NewTelegramReporter builds a telegrambot.Reporter over the reload-safe web
// backend holder and an SLA source (the state store). now defaults to time.Now.
func NewTelegramReporter(webHolder *WebBackendHolder, sla telegramSLAReader, now func() time.Time) telegrambot.Reporter {
	if now == nil {
		now = time.Now
	}
	return &telegramReporter{web: webHolder, sla: sla, now: now}
}

func (r *telegramReporter) Status(ctx context.Context) (telegrambot.StatusReport, error) {
	snap := r.web.DashboardSnapshot(ctx, 0)
	rep := telegrambot.StatusReport{
		Host:       snap.Daemon.Hostname,
		Services:   len(snap.Services),
		Monitored:  snap.Monitoring.Monitored,
		Paused:     snap.Monitoring.Paused,
		Errors:     snap.Activity.Errors,
		LastEvent:  snap.Activity.LastEventKind,
		HostUptime: snap.Daemon.HostUptime,
	}
	for _, s := range snap.Services {
		switch s.CheckHealth {
		case webCheckHealthOK:
			rep.OK++
		case webCheckHealthFailing:
			rep.Failing++
		}
	}
	return rep, nil
}

func (r *telegramReporter) Services(ctx context.Context) ([]telegrambot.ServiceLine, error) {
	services := r.web.Services(ctx)
	lines := make([]telegrambot.ServiceLine, 0, len(services))
	for _, s := range services {
		lines = append(lines, telegrambot.ServiceLine{
			Name:      s.Name,
			State:     s.State,
			Health:    s.CheckHealth,
			Monitored: s.Monitored,
		})
	}
	return lines, nil
}

func (r *telegramReporter) Watches(ctx context.Context) ([]telegrambot.WatchLine, error) {
	watches := r.web.Watches(ctx)
	lines := make([]telegrambot.WatchLine, 0, len(watches))
	for _, w := range watches {
		lines = append(lines, telegrambot.WatchLine{
			Name:      w.Name,
			Scope:     w.Scope,
			State:     w.State,
			Monitored: w.Monitored,
		})
	}
	return lines, nil
}

func (r *telegramReporter) SLA(ctx context.Context, service string) ([]telegrambot.SLAWindow, bool, error) {
	if !r.serviceExists(ctx, service) {
		return nil, false, nil
	}
	values, err := r.sla.SLAReport(service, r.now())
	if err != nil {
		return nil, true, err
	}
	windows := make([]telegrambot.SLAWindow, 0, len(values))
	for _, v := range values {
		windows = append(windows, telegrambot.SLAWindow{Window: v.Window, Ratio: formatSLARatio(v)})
	}
	return windows, true, nil
}

func (r *telegramReporter) Events(ctx context.Context, limit int) ([]telegrambot.EventLine, error) {
	events := r.web.Events(ctx, limit)
	lines := make([]telegrambot.EventLine, 0, len(events))
	for _, e := range events {
		lines = append(lines, telegrambot.EventLine{
			Time:    e.Time,
			Target:  eventTarget(e),
			Kind:    e.Kind,
			Message: e.Message,
		})
	}
	return lines, nil
}

func (r *telegramReporter) serviceExists(ctx context.Context, name string) bool {
	for _, s := range r.web.Services(ctx) {
		if strings.EqualFold(s.Name, name) {
			return true
		}
	}
	return false
}

func formatSLARatio(v state.SLAValue) string {
	ratio, ok := v.Ratio()
	if !ok {
		return "n/a"
	}
	return fmt.Sprintf("%.2f%%", ratio*100)
}

func eventTarget(e web.Event) string {
	switch {
	case e.Service != "":
		return e.Service
	case e.Watch != "":
		return e.Watch
	case e.App != "":
		return e.App
	default:
		return ""
	}
}
