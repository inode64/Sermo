package telegrambot

import (
	"context"
	"fmt"
	"strings"
)

// Reporter is the read-only view of Sermo state the bot renders into replies.
// It is deliberately narrow and free of daemon/web types so this package builds
// and tests without the daemon's platform-specific dependencies; the daemon
// supplies an adapter over its web backend and state store.
type Reporter interface {
	Status(ctx context.Context) (StatusReport, error)
	Services(ctx context.Context) ([]ServiceLine, error)
	Watches(ctx context.Context) ([]WatchLine, error)
	// SLA returns availability windows for a service; ok is false when no such
	// service is configured.
	SLA(ctx context.Context, service string) (windows []SLAWindow, ok bool, err error)
	Events(ctx context.Context, limit int) ([]EventLine, error)
}

// StatusReport is the /status rollup.
type StatusReport struct {
	Host       string
	Services   int
	OK         int
	Failing    int
	Monitored  int
	Paused     int
	Errors     int
	LastEvent  string
	HostUptime string
}

// ServiceLine is one service's summary for /services.
type ServiceLine struct {
	Name      string
	State     string
	Health    string
	Monitored bool
}

// WatchLine is one watch's summary for /watches.
type WatchLine struct {
	Name      string
	Scope     string
	State     string
	Monitored bool
}

// EventLine is one entry of the /events feed.
type EventLine struct {
	Time    string
	Target  string // the service/watch/app the event concerns, if any
	Kind    string
	Message string
}

// SLAWindow is one availability window for /sla.
type SLAWindow struct {
	Window string
	Ratio  string // formatted percentage, or "n/a"
}

const (
	// EventsDefaultLimit and EventsMaxLimit bound the /events reply size.
	EventsDefaultLimit = 10
	EventsMaxLimit     = 50
)

func formatStatus(r StatusReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Sermo status — %s\n", nonEmpty(r.Host, "host"))
	fmt.Fprintf(&b, "Services: %d (ok %d, failing %d)\n", r.Services, r.OK, r.Failing)
	fmt.Fprintf(&b, "Monitoring: %d monitored, %d paused\n", r.Monitored, r.Paused)
	fmt.Fprintf(&b, "Recent errors: %d\n", r.Errors)
	if r.LastEvent != "" {
		fmt.Fprintf(&b, "Last event: %s\n", r.LastEvent)
	}
	if r.HostUptime != "" {
		fmt.Fprintf(&b, "Host uptime: %s\n", r.HostUptime)
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatServices(lines []ServiceLine) string {
	if len(lines) == 0 {
		return "No services configured."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Services (%d):\n", len(lines))
	for _, s := range lines {
		fmt.Fprintf(&b, "- %s: %s / %s%s\n", s.Name, nonEmpty(s.State, "?"), nonEmpty(s.Health, "unknown"), monitorSuffix(s.Monitored))
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatServiceDetail(s ServiceLine) string {
	mon := "monitored"
	if !s.Monitored {
		mon = "not monitored"
	}
	return fmt.Sprintf("%s\nState: %s\nHealth: %s\nMonitoring: %s",
		s.Name, nonEmpty(s.State, "?"), nonEmpty(s.Health, "unknown"), mon)
}

func formatWatches(lines []WatchLine) string {
	if len(lines) == 0 {
		return "No watches configured."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Watches (%d):\n", len(lines))
	for _, w := range lines {
		fmt.Fprintf(&b, "- %s (%s): %s%s\n", w.Name, nonEmpty(w.Scope, "?"), nonEmpty(w.State, "?"), monitorSuffix(w.Monitored))
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatSLA(service string, windows []SLAWindow) string {
	if len(windows) == 0 {
		return fmt.Sprintf("No SLA data for %s.", service)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "SLA — %s\n", service)
	for _, w := range windows {
		fmt.Fprintf(&b, "- %s: %s\n", w.Window, w.Ratio)
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatEvents(lines []EventLine) string {
	if len(lines) == 0 {
		return "No recent events."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Recent events (%d):\n", len(lines))
	for _, e := range lines {
		target := ""
		if e.Target != "" {
			target = " " + e.Target
		}
		fmt.Fprintf(&b, "- %s [%s]%s: %s\n", e.Time, nonEmpty(e.Kind, "?"), target, e.Message)
	}
	return strings.TrimRight(b.String(), "\n")
}

func monitorSuffix(monitored bool) string {
	if monitored {
		return ""
	}
	return " [not monitored]"
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
