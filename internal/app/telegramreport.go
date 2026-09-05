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
// NewTelegramReporter adapts the daemon's web backend to the bot's read-only
// port. It returns the interface on purpose: the concrete adapter is an
// implementation detail, and exporting it would let a caller reach past the
// read-only surface the bot is deliberately confined to.
//
//nolint:ireturn // returns the narrow read-only port by design; see above.
func NewTelegramReporter(webHolder *WebBackendHolder, sla telegramSLAReader, now func() time.Time) telegrambot.Reporter {
	now = clockOrNow(now)
	return &telegramReporter{web: webHolder, sla: sla, now: now}
}

// backend pins the active web backend generation for one report. A holder
// without a backend yields an empty *WebBackend, whose readers answer with
// empty listings, so the bot degrades to "nothing to report" instead of failing.
func (r *telegramReporter) backend() *WebBackend {
	b, _ := r.web.backendAndGeneration()
	if b == nil {
		return &WebBackend{}
	}
	return b
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
	for i := range snap.Services {
		switch snap.Services[i].CheckHealth {
		case webCheckHealthOK:
			rep.OK++
		case webCheckHealthFailing:
			rep.Failing++
		}
	}
	return rep, nil
}

//nolint:dupl // parallel projections of two unrelated web types; merging them would need reflection.
func (r *telegramReporter) Services(ctx context.Context) ([]telegrambot.ServiceLine, error) {
	return mapSlice(r.backend().Services(ctx), func(s web.Service) telegrambot.ServiceLine {
		return telegrambot.ServiceLine{
			Name:      s.Name,
			State:     s.State,
			Health:    s.CheckHealth,
			Monitored: s.Monitored,
		}
	}), nil
}

//nolint:dupl // parallel projections of two unrelated web types; merging them would need reflection.
func (r *telegramReporter) Watches(ctx context.Context) ([]telegrambot.WatchLine, error) {
	return mapSlice(r.backend().Watches(ctx), func(w web.Watch) telegrambot.WatchLine {
		return telegrambot.WatchLine{
			Name:      w.Name,
			Scope:     w.Scope,
			State:     w.State,
			Monitored: w.Monitored,
		}
	}), nil
}

func (r *telegramReporter) SLA(ctx context.Context, service string) ([]telegrambot.SLAWindow, bool, error) {
	if !r.serviceExists(ctx, service) {
		return nil, false, nil
	}
	values, err := r.sla.SLAReport(service, r.now())
	if err != nil {
		return nil, true, fmt.Errorf("sla report for %s: %w", service, err)
	}
	windows := make([]telegrambot.SLAWindow, 0, len(values))
	for _, v := range values {
		windows = append(windows, telegrambot.SLAWindow{Window: v.Window, Ratio: formatSLARatio(v)})
	}
	return windows, true, nil
}

func (r *telegramReporter) Events(ctx context.Context, limit int) ([]telegrambot.EventLine, error) {
	events := r.backend().Events(ctx, limit)
	lines := make([]telegrambot.EventLine, 0, len(events))
	for _, e := range events {
		lines = append(lines, telegrambot.EventLine{
			Time:    e.Time,
			Target:  e.Target(),
			Kind:    e.Kind,
			Message: e.Message,
		})
	}
	return lines, nil
}

func (r *telegramReporter) serviceExists(ctx context.Context, name string) bool {
	services := r.backend().Services(ctx)
	for i := range services {
		if strings.EqualFold(services[i].Name, name) {
			return true
		}
	}
	return false
}

// formatSLARatio renders one window's availability, naming the affected minutes
// when there were any. A window can round to 100.00% and still have had an
// incident, so the percentage alone would hide it.
func formatSLARatio(v state.SLAValue) string {
	pct := v.PercentText()
	if v.DownBuckets <= 0 {
		return pct
	}
	return fmt.Sprintf("%s (%d min affected)", pct, v.DownBuckets)
}
