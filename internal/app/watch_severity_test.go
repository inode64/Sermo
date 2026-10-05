package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/execx"
	"sermo/internal/notify"
	"sermo/internal/severity"
	"sermo/internal/web"
)

// severityOf runs a built watch's check and reports the resolved severity it
// stamps on every result, which is what the whole feature reduces to at
// runtime. The check itself receives only the declaration — none when nothing
// is declared, so it may grade its own finding — which is why the result is
// resolved here the way the watch runtime resolves it.
func severityOf(t *testing.T, w *Watch) severity.Level {
	t.Helper()
	if w.Check == nil {
		return w.Severity
	}
	return w.Check.Run(context.Background()).Severity.Resolved()
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
					"severity": string(severity.Warning),
					"delta":    map[string]any{"op": ">", "value": 100},
				},
			},
		},
		"hdparm-sdd": map[string]any{
			"severity": string(severity.Warning),
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
	if got := severityOf(t, errors); got != severity.Warning {
		t.Errorf("errors metric severity = %q, want warning", got)
	}
	state, ok := slots[checks.DataKeyMetric+":state"]
	if !ok {
		t.Fatalf("no state metric watch built, got slots %v", slots)
	}
	// The base check block is copied over the metric block, so a metric-level
	// severity is only safe if it is applied after that copy.
	if got := severityOf(t, state); got != severity.Error {
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
	if got := severityOf(t, hdparm); got != severity.Warning {
		t.Errorf("hdparm-sdd severity = %q, want the watch-level warning", got)
	}
	if hdparm.Severity != severity.Warning {
		t.Error("hdparm-sdd severity is not warning")
	}
	if got := severityOf(t, storage); got != severity.Error {
		t.Errorf("storage-root severity = %q, want error", got)
	}
	if storage.Severity == severity.Warning {
		t.Error("storage-root severity is warning, want undeclared error")
	}
}

// A check-level declaration is the per-watch default for every metric, and a
// metric may still overrule it in either direction.
func TestBuildWatchesSeverityMetricOverridesCheck(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"icmp-gw": map[string]any{
			"check": map[string]any{
				"type": "icmp", "host": "192.0.2.1", "count": 3,
				"severity": string(severity.Warning),
			},
			"metrics": map[string]any{
				"state":   map[string]any{"severity": string(severity.Error), "expect": "down"},
				"latency": map[string]any{"threshold": map[string]any{"op": ">", "value": 100}},
			},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{DefaultTimeout: time.Second, ExecxRunner: execx.CommandRunner{}}, 30*time.Second)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	slots := watchesByStateSlot(watches)
	if got := severityOf(t, slots[checks.DataKeyMetric+":latency"]); got != severity.Warning {
		t.Errorf("latency severity = %q, want the check-level warning", got)
	}
	if got := severityOf(t, slots[checks.DataKeyMetric+":state"]); got != severity.Error {
		t.Errorf("state severity = %q, want the metric-level error", got)
	}
}

// An advisory reports through its events' severity, which the event log
// stores beside the kind, so the watch stays amber per metric and across a
// daemon restart.
func TestWatchAdvisoryCarriesItsSeverityAndStillActs(t *testing.T) {
	check := &scriptedCheck{results: []checks.Result{
		{Check: "hdparm", Unavailable: true, Message: "no timing in output", Severity: severity.Warning},
		{Check: "hdparm", OK: true, Message: "read=0.4 MB/s", Severity: severity.Warning},
	}}
	var events []Event
	var hookEnvSeen map[string]string
	sent := make(chan notify.Message, 1)
	w := &Watch{
		Name: "hdparm-sdd", CheckType: checks.CheckTypeHdparm, Check: check,
		Severity: severity.Warning,
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
	if len(events) < 2 || events[0].Kind != eventKindError || events[0].Severity != severity.Warning {
		t.Fatalf("events = %+v, want an unavailable advisory to raise an error graded warning", events)
	}
	for _, e := range events {
		if e.Kind == eventKindFiring && e.Severity != severity.Warning {
			t.Errorf("kinds = %v: a firing from an advisory watch graded %q, want warning", kinds, e.Severity)
		}
	}
	// An advisory is still a condition: it must keep running its actions.
	if hookEnvSeen == nil {
		t.Fatal("advisory watch ran no hook, want the configured hook to still run")
	}
	if got := hookEnvSeen[sermoEnvSeverity]; got != string(severity.Warning) {
		t.Errorf("%s = %q, want warning", sermoEnvSeverity, got)
	}
	select {
	case msg := <-sent:
		if !strings.Contains(msg.Subject, string(severity.Warning)) {
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
	if got := watchSubject("http", "request timed out", severity.Error); got != "[sermo] http: request timed out" {
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
		severity: severity.Error,
		metrics: map[string]any{
			"errors": map[string]any{"severity": string(severity.Warning)},
			"state":  map[string]any{"expect": "down"},
		},
	}
	if got := w.severityFor("errors"); got != severity.Warning {
		t.Errorf("errors severity = %q, want warning", got)
	}
	if got := w.severityFor("state"); got != severity.Error {
		t.Errorf("state severity = %q, want the inherited error", got)
	}
	if got := w.severityFor(""); got != severity.Error {
		t.Errorf("single-check severity = %q, want error", got)
	}

	advisory := &webWatch{severity: severity.Warning}
	if got := advisory.severityFor(""); got != severity.Warning {
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

	grave := watchSnapshotReadings(checks.CheckTypeHdparm, severity.Error, snap, false)
	if !watchReadingsFailed(grave) || watchReadingsWarning(grave) {
		t.Fatalf("error-severity readings = %+v, want an Error entry", grave)
	}

	advisory := watchSnapshotReadings(checks.CheckTypeHdparm, severity.Warning, snap, false)
	if watchReadingsFailed(advisory) {
		t.Fatalf("advisory readings = %+v, want no Error entry: Error is what turns the row red", advisory)
	}
	if !watchReadingsWarning(advisory) {
		t.Fatalf("advisory readings = %+v, want a Warning entry", advisory)
	}
	if got := watchSnapshotSummary(snap, advisory); got != snap.Message {
		t.Errorf("summary = %q, want the advisory message %q", got, snap.Message)
	}

	failed, warning := watchViewState(&webWatch{}, web.Watch{Readings: advisory}, time.Time{}, time.Time{})
	if failed || !warning {
		t.Errorf("watchViewState() = (%v, %v), want (false, true)", failed, warning)
	}
	if got := WatchState(true, true, failed, warning, true); got != TargetStateWarning {
		t.Errorf("WatchState() = %q, want %q", got, TargetStateWarning)
	}

	// One grave reading beside the advisory outranks it.
	mixed := append(append([]web.WatchReading{}, advisory...), grave...)
	if failed, _ := watchViewState(&webWatch{}, web.Watch{Readings: mixed}, time.Time{}, time.Time{}); !failed {
		t.Error("a mixed watch graded warning, want failed: an outage outranks an advisory")
	}
}

// The last activity's severity is the signal that survives a daemon restart,
// because the event log stores it.
func TestWatchViewStateFromAdvisoryActivity(t *testing.T) {
	at := time.Date(2026, time.June, 17, 14, 20, 43, 0, time.UTC)
	failed, warning := watchViewState(&webWatch{}, web.Watch{LastActivityKind: eventKindFiring, LastActivitySeverity: string(severity.Warning)}, at, time.Time{})
	if failed || !warning {
		t.Errorf("advisory activity = (%v, %v), want (false, true)", failed, warning)
	}
	failed, warning = watchViewState(&webWatch{}, web.Watch{LastActivityKind: eventKindFiring}, at, time.Time{})
	if !failed || warning {
		t.Errorf("firing activity = (%v, %v), want (true, false)", failed, warning)
	}

	// An episode announced as firing may since have been regraded an advisory
	// by the check itself: the newest snapshot then carries a warning row and no
	// error row, and that outranks the kind that opened the episode.
	advisory := []web.WatchReading{{Field: watchReadingFieldWarning, Warning: "smart /dev/sda health=PASSED; reallocated 4 > 0"}}
	failed, warning = watchViewState(&webWatch{}, web.Watch{LastActivityKind: eventKindFiring, Readings: advisory}, at, time.Time{})
	if failed || !warning {
		t.Errorf("firing activity + advisory readings = (%v, %v), want (false, true)", failed, warning)
	}
	mixed := append([]web.WatchReading{{Field: watchReadingFieldError, Error: "link down"}}, advisory...)
	if failed, _ = watchViewState(&webWatch{}, web.Watch{LastActivityKind: eventKindFiring, Readings: mixed}, at, time.Time{}); !failed {
		t.Error("firing activity + an error reading graded warning, want failed")
	}
	if failed, _ = watchViewState(&webWatch{}, web.Watch{LastActivityKind: eventKindHookFail, Readings: advisory}, at, time.Time{}); !failed {
		t.Error("a failed hook beside advisory readings graded warning, want failed: the hook failure is an outage of its own")
	}
}

// A check that received no declaration grades its own result, and the watch
// announces that grade: the result's severity decides the event kind, not the
// watch's static declaration.
func TestWatchGradesEventKindFromResultSeverity(t *testing.T) {
	check := &scriptedCheck{results: []checks.Result{
		{Check: "smart", OK: true, Condition: true, Message: "health=PASSED; reallocated 4 > 0", Severity: severity.Warning},
	}}
	var events []Event
	w := &Watch{
		Name: "smart-sda", CheckType: checks.CheckTypeSmart, Check: check,
		Emit: func(e Event) { events = append(events, e) },
	}
	w.RunCycle(context.Background())
	if len(events) != 1 || events[0].Kind != eventKindFiring || events[0].Severity != severity.Warning {
		t.Fatalf("events = %+v, want one firing graded warning from a result the check graded warning", events)
	}
	if w.Severity == severity.Warning {
		t.Error("watch severity is warning, want undeclared error")
	}
}

// An open episode that grows graver — the drive's verdict flips to FAILED under
// the same reallocated sectors — is announced again, once; a grade that eases
// off — a RAID member's state recovers while its counters remain — is held
// until the episode recovers, so an oscillating grade stays quiet.
func TestWatchEscalatesAndHoldsWithinEpisode(t *testing.T) {
	advisory := checks.Result{Check: "smart", OK: true, Condition: true, Message: "health=PASSED; reallocated 4 > 0", Severity: severity.Warning}
	outage := checks.Result{Check: "smart", OK: true, Condition: true, Message: "health=FAILED; reallocated 4 > 0"}
	check := &scriptedCheck{results: []checks.Result{advisory, advisory, outage, outage, advisory}}
	var events []Event
	w := &Watch{
		Name: "smart-sda", CheckType: checks.CheckTypeSmart, Check: check,
		Emit: func(e Event) { events = append(events, e) },
	}
	for range 5 {
		w.RunCycle(context.Background())
	}
	got := make([]string, 0, len(events))
	for _, e := range events {
		got = append(got, e.Kind+"/"+e.Severity.String())
	}
	want := []string{eventKindFiring + "/warning", eventKindFiring + "/error"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v: one announcement per escalation, none when the grade eases", got, want)
	}
}

// A watch that worked and fails once (a DNS server slow for a moment) is
// re-run before anything is announced; a fault that lasts is announced.
func TestWatchConfirmsAnUnavailabilityBeforeAnnouncingIt(t *testing.T) {
	ok := checks.Result{Check: "clock", OK: true, Message: "offset 2ms"}
	gone := checks.Result{Check: "clock", Unavailable: true, Message: "lookup time.cloudflare.com: i/o timeout"}
	check := &scriptedCheck{results: []checks.Result{ok, gone, ok, gone, gone}}
	var events []Event
	slept := 0
	w := &Watch{
		Name: "watch-clock-drift", CheckType: checks.CheckTypeClock, Check: check, FireOnFail: true,
		Sleep: func(time.Duration) { slept++ },
		Emit:  func(e Event) { events = append(events, e) },
	}
	w.RunCycle(context.Background()) // ok
	w.RunCycle(context.Background()) // blip, re-run answers ok
	if len(events) != 0 || slept != 1 {
		t.Fatalf("a transient unavailability must stay silent: events=%+v slept=%d", events, slept)
	}
	w.RunCycle(context.Background()) // fails twice: announced
	if len(events) != 1 || events[0].Kind != eventKindError || events[0].Check != watchAvailabilityCheck {
		t.Fatalf("a lasting unavailability must be announced: %+v", events)
	}
}
