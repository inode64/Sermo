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
	// EventsDefaultLimit is the number of events returned when no limit is requested.
	EventsDefaultLimit = 10
	// EventsMaxLimit is the largest number of events returned in one reply.
	EventsMaxLimit = 50
)

// Fallbacks for fields a report may not carry. Named so the same field reads
// the same way in every reply: the service list and the service detail render
// one service, and must not disagree about how an unknown state looks.
const (
	fallbackUnknownField = "?"
	fallbackHealth       = "unknown"
	fallbackHost         = "host"
)

// listReply renders the shared shape of every list command: an empty-state
// line, a "Header (n):" lead and one bullet per item.
func listReply[T any](header, empty string, items []T, line func(T) string) string {
	if len(items) == 0 {
		return empty
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d):\n", header, len(items))
	for _, item := range items {
		fmt.Fprintf(&b, "- %s\n", line(item))
	}
	return trimReply(&b)
}

// trimReply drops the trailing newline every builder-based reply accumulates.
func trimReply(b *strings.Builder) string { return strings.TrimRight(b.String(), "\n") }

func formatStatus(r StatusReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Sermo status — %s\n", nonEmpty(r.Host, fallbackHost))
	fmt.Fprintf(&b, "Services: %d (ok %d, failing %d)\n", r.Services, r.OK, r.Failing)
	fmt.Fprintf(&b, "Monitoring: %d monitored, %d paused\n", r.Monitored, r.Paused)
	fmt.Fprintf(&b, "Recent errors: %d\n", r.Errors)
	if r.LastEvent != "" {
		fmt.Fprintf(&b, "Last event: %s\n", r.LastEvent)
	}
	if r.HostUptime != "" {
		fmt.Fprintf(&b, "Host uptime: %s\n", r.HostUptime)
	}
	return trimReply(&b)
}

func formatServices(lines []ServiceLine) string {
	return listReply("Services", "No services configured.", lines, func(s ServiceLine) string {
		return fmt.Sprintf("%s: %s / %s%s", s.Name, serviceState(s), serviceHealth(s), monitorSuffix(s.Monitored))
	})
}

func serviceState(s ServiceLine) string  { return nonEmpty(s.State, fallbackUnknownField) }
func serviceHealth(s ServiceLine) string { return nonEmpty(s.Health, fallbackHealth) }

func formatServiceDetail(s ServiceLine) string {
	mon := "monitored"
	if !s.Monitored {
		mon = "not monitored"
	}
	return fmt.Sprintf("%s\nState: %s\nHealth: %s\nMonitoring: %s",
		s.Name, serviceState(s), serviceHealth(s), mon)
}

func formatWatches(lines []WatchLine) string {
	return listReply("Watches", "No watches configured.", lines, func(w WatchLine) string {
		return fmt.Sprintf("%s (%s): %s%s", w.Name,
			nonEmpty(w.Scope, fallbackUnknownField), nonEmpty(w.State, fallbackUnknownField), monitorSuffix(w.Monitored))
	})
}

// formatSLA keeps its own shape: the lead names the service instead of counting
// the windows, so listReply's "Header (n):" would read wrong.
func formatSLA(service string, windows []SLAWindow) string {
	if len(windows) == 0 {
		return fmt.Sprintf("No SLA data for %s.", service)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "SLA — %s\n", service)
	for _, w := range windows {
		fmt.Fprintf(&b, "- %s: %s\n", w.Window, w.Ratio)
	}
	return trimReply(&b)
}

func formatEvents(lines []EventLine) string {
	return listReply("Recent events", "No recent events.", lines, func(e EventLine) string {
		target := ""
		if e.Target != "" {
			target = " " + e.Target
		}
		return fmt.Sprintf("%s [%s]%s: %s", e.Time, nonEmpty(e.Kind, fallbackUnknownField), target, e.Message)
	})
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
