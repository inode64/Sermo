package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/execx"
	"sermo/internal/notify"
	"sermo/internal/web"
)

// severityOf runs a built watch's check and reports the severity it stamps on
// every result, which is what the whole feature reduces to at runtime.
func severityOf(t *testing.T, w *Watch) string {
	t.Helper()
	if w.Check == nil {
		return w.Severity
	}
	return w.Check.Run(context.Background()).Severity
}

func watchesByStateSlot(watches []*Watch) map[string]*Watch {
	out := map[string]*Watch{}
	for _, w := range watches {
		out[w.StateSlot] = w
	}
	return out
}

// The narrowest declaration wins. This is the case the whole per-metric feature
// exists for: a link's error counter is an advisory while the link going down
// stays an outage.
func TestBuildWatchesSeverityPrecedence(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"net-enp1s0": map[string]any{
			"check": map[string]any{"type": "net", "interface": "enp1s0"},
			"metrics": map[string]any{
				"state": map[string]any{"expect": "down"},
				"errors": map[string]any{
					"severity": checks.SeverityWarning,
					"delta":    map[string]any{"op": ">", "value": 100},
				},
			},
		},
		"hdparm-sdd": map[string]any{
			"severity": checks.SeverityWarning,
			"check": map[string]any{
				"type": "hdparm", "device": "/dev/sdd",
				"read": map[string]any{"op": "<", "value": 20},
			},
		},
		"storage-root": map[string]any{
			"check": map[string]any{
				"type": "storage", "path": "/",
				"used_pct": map[string]any{"op": ">", "value": 90},
			},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{DefaultTimeout: time.Second, ExecxRunner: execx.CommandRunner{}}, 30*time.Second)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	slots := watchesByStateSlot(watches)

	errors, ok := slots[checks.DataKeyMetric+":errors"]
	if !ok {
		t.Fatalf("no errors metric watch built, got slots %v", slots)
	}
	if got := severityOf(t, errors); got != checks.SeverityWarning {
		t.Errorf("errors metric severity = %q, want warning", got)
	}
	state, ok := slots[checks.DataKeyMetric+":state"]
	if !ok {
		t.Fatalf("no state metric watch built, got slots %v", slots)
	}
	// The base check block is copied over the metric block, so a metric-level
	// severity is only safe if it is applied after that copy.
	if got := severityOf(t, state); got != checks.SeverityError {
		t.Errorf("state metric severity = %q, want the undeclared default error", got)
	}

	var hdparm, storage *Watch
	for _, w := range watches {
		switch w.Name {
		case "hdparm-sdd":
			hdparm = w
		case "storage-root":
			storage = w
		}
	}
	if hdparm == nil || storage == nil {
		t.Fatalf("missing single-check watches, got %d watches", len(watches))
	}
	// A watch-level declaration reaches the check, which is what makes the
	// inline build path carry it at all.
	if got := severityOf(t, hdparm); got != checks.SeverityWarning {
		t.Errorf("hdparm-sdd severity = %q, want the watch-level warning", got)
	}
	if !hdparm.IsWarning() {
		t.Error("hdparm-sdd Watch.IsWarning() = false, want true")
	}
	if got := severityOf(t, storage); got != checks.SeverityError {
		t.Errorf("storage-root severity = %q, want error", got)
	}
	if storage.IsWarning() {
		t.Error("storage-root Watch.IsWarning() = true, want false for an undeclared watch")
	}
}

// A check-level declaration is the per-watch default for every metric, and a
// metric may still overrule it in either direction.
func TestBuildWatchesSeverityMetricOverridesCheck(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"icmp-gw": map[string]any{
			"check": map[string]any{
				"type": "icmp", "host": "192.0.2.1", "count": 3,
				"severity": checks.SeverityWarning,
			},
			"metrics": map[string]any{
				"state":   map[string]any{"severity": checks.SeverityError, "expect": "down"},
				"latency": map[string]any{"threshold": map[string]any{"op": ">", "value": 100}},
			},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{DefaultTimeout: time.Second, ExecxRunner: execx.CommandRunner{}}, 30*time.Second)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	slots := watchesByStateSlot(watches)
	if got := severityOf(t, slots[checks.DataKeyMetric+":latency"]); got != checks.SeverityWarning {
		t.Errorf("latency severity = %q, want the check-level warning", got)
	}
	if got := severityOf(t, slots[checks.DataKeyMetric+":state"]); got != checks.SeverityError {
		t.Errorf("state severity = %q, want the metric-level error", got)
	}
}

// An advisory reports through its own event kind. The kind is the one severity
// channel the event log stores, so this is what keeps a watch amber per metric
// and across a daemon restart.
func TestWatchWarningRaisesWarningKindAndStillActs(t *testing.T) {
	check := &scriptedCheck{results: []checks.Result{
		{Check: "hdparm", Unavailable: true, Message: "no timing in output", Severity: checks.SeverityWarning},
		{Check: "hdparm", OK: true, Message: "read=0.4 MB/s", Severity: checks.SeverityWarning},
	}}
	var events []Event
	var hookEnvSeen map[string]string
	sent := make(chan notify.Message, 1)
	w := &Watch{
		Name: "hdparm-sdd", CheckType: checks.CheckTypeHdparm, Check: check,
		Severity: checks.SeverityWarning,
		Hook:     HookSpec{Command: []string{"/bin/true"}},
		Runner: HookRunnerFunc(func(_ context.Context, _ []string, env map[string]string, _ time.Duration) error {
			hookEnvSeen = env
			return nil
		}),
		Notifiers: []notify.Notifier{captureNotifier{sent: sent}},
		Emit:      func(e Event) { events = append(events, e) },
	}

	w.RunCycle(context.Background()) // unavailable
	w.RunCycle(context.Background()) // condition fires

	kinds := make([]string, 0, len(events))
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	if len(events) == 0 || events[0].Kind != eventKindWarning {
		t.Fatalf("kinds = %v, want an unavailable advisory to raise %q, not %q", kinds, eventKindWarning, eventKindError)
	}
	for _, e := range events {
		if e.Kind == eventKindError || e.Kind == eventKindFiring {
			t.Errorf("kinds = %v, want no %q or %q from an advisory watch", kinds, eventKindError, eventKindFiring)
		}
	}
	// An advisory is still a condition: it must keep running its actions.
	if hookEnvSeen == nil {
		t.Fatal("advisory watch ran no hook, want the configured hook to still run")
	}
	if got := hookEnvSeen[sermoEnvSeverity]; got != checks.SeverityWarning {
		t.Errorf("%s = %q, want warning", sermoEnvSeverity, got)
	}
	select {
	case msg := <-sent:
		if !strings.Contains(msg.Subject, checks.SeverityWarning) {
			t.Errorf("subject = %q, want it to mark the advisory", msg.Subject)
		}
	default:
		t.Error("advisory watch sent no notification, want the configured notifier to still fire")
	}
}

// An undeclared watch keeps every byte of today's reporting, including the
// notification subject operators already filter on.
func TestWatchErrorSeverityKeepsExistingReporting(t *testing.T) {
	check := &scriptedCheck{results: []checks.Result{
		{Check: "http", Unavailable: true, Message: "request timed out"},
	}}
	var events []Event
	w := &Watch{
		Name: "http", CheckType: checks.CheckTypeHTTP, Check: check,
		Emit: func(e Event) { events = append(events, e) },
	}
	w.RunCycle(context.Background())
	if len(events) != 1 || events[0].Kind != eventKindError {
		t.Fatalf("events = %+v, want one error", events)
	}
	if got := watchSubject("http", "request timed out", checks.SeverityError); got != "[sermo] http: request timed out" {
		t.Errorf("subject = %q, want the unmarked error form", got)
	}
}

type captureNotifier struct {
	sent chan notify.Message
}

func (captureNotifier) Name() string { return "capture" }
func (captureNotifier) Type() string { return "capture" }
func (n captureNotifier) Send(_ context.Context, msg notify.Message) error {
	select {
	case n.sent <- msg:
	default:
	}
	return nil
}

// The dashboard resolves the same chain the daemon builder does, per metric, so
// a net watch's error counter can read amber while its link state reads red.
func TestWebWatchSeverityFor(t *testing.T) {
	w := &webWatch{
		severity: checks.SeverityError,
		metrics: map[string]any{
			"errors": map[string]any{"severity": checks.SeverityWarning},
			"state":  map[string]any{"expect": "down"},
		},
	}
	if got := w.severityFor("errors"); got != checks.SeverityWarning {
		t.Errorf("errors severity = %q, want warning", got)
	}
	if got := w.severityFor("state"); got != checks.SeverityError {
		t.Errorf("state severity = %q, want the inherited error", got)
	}
	if got := w.severityFor(""); got != checks.SeverityError {
		t.Errorf("single-check severity = %q, want error", got)
	}

	advisory := &webWatch{severity: checks.SeverityWarning}
	if got := advisory.severityFor(""); got != checks.SeverityWarning {
		t.Errorf("watch-level severity = %q, want warning", got)
	}
}

// Error is what paints a row red, so an advisory must report through Warning
// instead — and the row must grade as warning rather than failed.
func TestWatchAdvisoryReadingsAndRowState(t *testing.T) {
	snap := CheckSnapshot{
		Observation: checks.ObservationUnavailable, Unavailable: true,
		Message: "hdparm /dev/sdd: no timing in output",
	}

	grave := watchSnapshotReadings(checks.CheckTypeHdparm, checks.SeverityError, snap)
	if !watchReadingsFailed(grave) || watchReadingsWarning(grave) {
		t.Fatalf("error-severity readings = %+v, want an Error entry", grave)
	}

	advisory := watchSnapshotReadings(checks.CheckTypeHdparm, checks.SeverityWarning, snap)
	if watchReadingsFailed(advisory) {
		t.Fatalf("advisory readings = %+v, want no Error entry: Error is what turns the row red", advisory)
	}
	if !watchReadingsWarning(advisory) {
		t.Fatalf("advisory readings = %+v, want a Warning entry", advisory)
	}
	if got := watchSnapshotSummary(snap, advisory); got != snap.Message {
		t.Errorf("summary = %q, want the advisory message %q", got, snap.Message)
	}

	failed, warning := watchViewState(web.Watch{Readings: advisory})
	if failed || !warning {
		t.Errorf("watchViewState() = (%v, %v), want (false, true)", failed, warning)
	}
	if got := WatchState(true, true, failed, warning, true); got != TargetStateWarning {
		t.Errorf("WatchState() = %q, want %q", got, TargetStateWarning)
	}

	// One grave reading beside the advisory outranks it.
	mixed := append(append([]web.WatchReading{}, advisory...), grave...)
	if failed, _ := watchViewState(web.Watch{Readings: mixed}); !failed {
		t.Error("a mixed watch graded warning, want failed: an outage outranks an advisory")
	}
}

// The advisory event kind is the signal that survives a daemon restart, because
// it is what the event log stores.
func TestWatchViewStateFromAdvisoryActivity(t *testing.T) {
	const at = "2026-06-17T14:20:43Z"
	failed, warning := watchViewState(web.Watch{LastActivityKind: eventKindWarning, LastActivity: at})
	if failed || !warning {
		t.Errorf("advisory activity = (%v, %v), want (false, true)", failed, warning)
	}
	failed, warning = watchViewState(web.Watch{LastActivityKind: eventKindFiring, LastActivity: at})
	if !failed || warning {
		t.Errorf("firing activity = (%v, %v), want (true, false)", failed, warning)
	}
}
