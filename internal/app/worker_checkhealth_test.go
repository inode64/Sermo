package app

import (
	"context"
	"testing"

	"sermo/internal/checks"
	"sermo/internal/rules"
)

// runCycles drives n cycles over a worker whose check cache is produced by
// next(), collecting every emitted event.
func runCycles(t *testing.T, next func(cycle int) map[string]checks.Result, n int) []Event {
	t.Helper()
	var events []Event
	cycle := 0
	w := &Worker{
		Service: "web",
		Checks: func(context.Context, checks.Deps) map[string]checks.Result {
			cycle++
			return next(cycle)
		},
		Emit: func(e Event) { events = append(events, e) },
	}
	for range n {
		w.RunCycle(context.Background())
	}
	return events
}

func kinds(events []Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

// A required check that fails with no rule bound to it used to move the
// dashboard to "failed" and write nothing: 17 services fleet-wide sat in that
// state, including a smartd whose unit had failed hours before. The transition
// must reach the event log exactly once, and the recovery too.
func TestRequiredCheckFailureIsReportedOnceAndRecovers(t *testing.T) {
	events := runCycles(t, func(c int) map[string]checks.Result {
		if c <= 3 {
			return map[string]checks.Result{"service": {Check: "service", OK: c == 1, Message: "status failed (want active)"}}
		}
		return map[string]checks.Result{"service": {Check: "service", OK: true, Message: "status active (want active)"}}
	}, 5)

	if got := kinds(events); len(got) != 2 || got[0] != eventKindFiring || got[1] != eventKindRecovered {
		t.Fatalf("kinds = %v, want exactly [firing recovered]", got)
	}
	if events[0].Service != "web" {
		t.Fatalf("event service = %q, want web", events[0].Service)
	}
	if events[0].Message != "check service: status failed (want active)" {
		t.Fatalf("firing message = %q, want the check name and its own diagnostic", events[0].Message)
	}
}

// Persistent snapshots are the durable record of the last check-health edge.
// Restoring them on daemon startup prevents a still-failing check from opening
// a duplicate event while preserving the eventual recovery transition.
func TestRestoredCheckFailureDoesNotRepeatFiringAndRecovers(t *testing.T) {
	snapshots := NewSnapshots()
	snapshots.PublishWithCheckTypes("web", map[string]checks.Result{
		"service": {Check: "service", OK: false, Message: "status failed (want active)"},
	}, map[string]bool{"service": true}, map[string]string{"service": "service"})

	var events []Event
	cycle := 0
	w := &Worker{
		Service:      "web",
		checkFailing: checkFailingFromSnapshots(snapshots, "web", map[string]string{"service": "service"}),
		Checks: func(context.Context, checks.Deps) map[string]checks.Result {
			cycle++
			return map[string]checks.Result{
				"service": {
					Check: "service", OK: cycle > 1,
					Message: "status failed (want active)",
				},
			}
		},
		Emit: func(e Event) { events = append(events, e) },
	}
	w.RunCycle(context.Background())
	w.RunCycle(context.Background())

	if got := kinds(events); len(got) != 1 || got[0] != eventKindRecovered {
		t.Fatalf("kinds = %v, want only [recovered]", got)
	}
}

func TestCheckFailureRestoreRejectsStaleSnapshots(t *testing.T) {
	tests := []struct {
		name       string
		result     checks.Result
		storedType string
		current    map[string]string
		want       map[string]bool
	}{
		{
			name: "matching failure", result: checks.Result{Check: "service", OK: false},
			storedType: "service", current: map[string]string{"service": "service"},
			want: map[string]bool{"service": true},
		},
		{
			name: "matching health", result: checks.Result{Check: "service", OK: true},
			storedType: "service", current: map[string]string{"service": "service"},
			want: map[string]bool{"service": false},
		},
		{
			name: "changed type", result: checks.Result{Check: "service", OK: false},
			storedType: "service", current: map[string]string{"service": "process"},
		},
		{
			name: "removed check", result: checks.Result{Check: "service", OK: false},
			storedType: "service", current: map[string]string{},
		},
		{
			name: "optional check", result: checks.Result{Check: "service", OK: false, Optional: true},
			storedType: "service", current: map[string]string{"service": "service"},
		},
		{
			name: "skipped check", result: checks.Result{Check: "service", OK: false, Skipped: true},
			storedType: "service", current: map[string]string{"service": "service"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshots := NewSnapshots()
			snapshots.PublishWithCheckTypes("web", map[string]checks.Result{"service": tt.result},
				map[string]bool{"service": true}, map[string]string{"service": tt.storedType})
			got := checkFailingFromSnapshots(snapshots, "web", tt.current)
			if len(got) != len(tt.want) || got["service"] != tt.want["service"] {
				t.Fatalf("restored = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// Optional and verdictless checks assert nothing about availability, so neither
// may raise a service event. `backup` (reports: state) is failing on 67 services
// by design — a backup is idle almost always.
func TestOptionalAndVerdictlessChecksRaiseNoEvent(t *testing.T) {
	events := runCycles(t, func(int) map[string]checks.Result {
		return map[string]checks.Result{
			"backup":  {Check: "backup", OK: false, Reports: checks.ReportsState},
			"reading": {Check: "reading", OK: false, Reports: checks.ReportsValue},
			"warn":    {Check: "warn", OK: false, Optional: true},
		}
	}, 3)
	if len(events) != 0 {
		t.Fatalf("events = %+v, want none", events)
	}
}

// A skipped check produced no observation this cycle, so it is neither a
// failure nor a recovery.
func TestSkippedCheckRaisesNoEvent(t *testing.T) {
	events := runCycles(t, func(int) map[string]checks.Result {
		return map[string]checks.Result{"pidfile": {Check: "pidfile", OK: false, Skipped: true}}
	}, 3)
	if len(events) != 0 {
		t.Fatalf("events = %+v, want none for a skipped check", events)
	}
}

// A condition-style check is healthy while its condition is false, so the event
// must follow Healthy() rather than the raw OK flag.
func TestConditionCheckReportsOnTheConditionFiring(t *testing.T) {
	events := runCycles(t, func(c int) map[string]checks.Result {
		return map[string]checks.Result{
			"cert": {Check: "cert", OK: c >= 2, Condition: true, Message: "expires in 1 days"},
		}
	}, 3)
	if got := kinds(events); len(got) != 1 || got[0] != eventKindFiring {
		t.Fatalf("kinds = %v, want [firing] once the condition fires", got)
	}
}

// Flapping must produce one event per crossing, not one per cycle.
func TestFlappingReportsEachCrossingOnce(t *testing.T) {
	events := runCycles(t, func(c int) map[string]checks.Result {
		return map[string]checks.Result{"port": {Check: "port", OK: c%2 == 0, Message: "connect refused"}}
	}, 4)
	if got := kinds(events); len(got) != 4 {
		t.Fatalf("kinds = %v, want four crossings", got)
	}
}

// A result reused from the cache because of a per-check interval is not a fresh
// observation, so it must neither raise nor clear an event.
func TestCachedResultNotRunThisCycleRaisesNoEvent(t *testing.T) {
	var events []Event
	cycle := 0
	w := &Worker{Service: "web", Emit: func(e Event) { events = append(events, e) }}
	w.Checks = func(context.Context, checks.Deps) map[string]checks.Result {
		cycle++
		// "slow" only runs on the first cycle; afterwards its cached failure is
		// reused without re-running.
		w.cycleRan = map[string]bool{"slow": cycle == 1}
		return map[string]checks.Result{"slow": {Check: "slow", OK: false, Message: "still down"}}
	}
	for range 4 {
		w.RunCycle(context.Background())
	}
	if got := kinds(events); len(got) != 1 || got[0] != eventKindFiring {
		t.Fatalf("kinds = %v, want a single firing from the one cycle that ran", got)
	}
}

// A reload that drops a check must not leave a stale "failing" memory behind:
// if the name comes back later and is healthy, that is not a recovery of
// anything this configuration ever reported.
func TestDroppedCheckForgetsItsFailingMemory(t *testing.T) {
	events := runCycles(t, func(c int) map[string]checks.Result {
		switch c {
		case 1:
			return map[string]checks.Result{"port": {Check: "port", OK: false, Message: "refused"}}
		case 2:
			return map[string]checks.Result{} // reload dropped it
		default:
			return map[string]checks.Result{"port": {Check: "port", OK: true, Message: "connected"}}
		}
	}, 3)
	if got := kinds(events); len(got) != 1 || got[0] != eventKindFiring {
		t.Fatalf("kinds = %v, want only the original firing", got)
	}
}

// A check a rule already reads must not get a second pair of events: the rule
// emits its own alert and recovery with the operator's wording, and doubling
// them would turn one incident into two entries.
func TestCheckReportedByARuleGetsNoSecondEvent(t *testing.T) {
	var events []Event
	cycle := 0
	w := &Worker{
		Service: "web",
		Rules: []rules.Rule{{
			Name:    "warn-down",
			Type:    rules.RuleAlert,
			If:      map[string]any{rules.ConditionFailed: map[string]any{rules.FieldCheck: "http"}},
			Actions: []rules.Action{{Type: rules.ActionAlert, Message: "http is down"}},
		}},
		Checks: func(context.Context, checks.Deps) map[string]checks.Result {
			cycle++
			return map[string]checks.Result{
				"http": {Check: "http", OK: false, Message: "connect refused"},
				// No rule mentions `disk`, so this one is still reported.
				"disk": {Check: "disk", OK: false, Message: "0% free"},
			}
		},
		Emit: func(e Event) { events = append(events, e) },
	}
	w.RunCycle(context.Background())

	var health []Event
	for _, e := range events {
		if e.Rule == "" && (e.Kind == eventKindFiring || e.Kind == eventKindRecovered) {
			health = append(health, e)
		}
	}
	if len(health) != 1 {
		t.Fatalf("check-health events = %+v, want only the unruled `disk`", health)
	}
	if health[0].Message != "check disk: 0% free" {
		t.Fatalf("message = %q, want the unruled check", health[0].Message)
	}
}

// A rule buried inside and/or/not still counts as speaking for its checks.
func TestNestedRuleConditionSuppressesCheckHealthEvent(t *testing.T) {
	var events []Event
	w := &Worker{
		Service: "web",
		Rules: []rules.Rule{{
			Name: "deep",
			Type: rules.RuleAlert,
			If: map[string]any{rules.ConditionAnd: []any{
				map[string]any{rules.ConditionNot: map[string]any{
					rules.ConditionActive: map[string]any{rules.FieldCheck: "http"},
				}},
			}},
			Actions: []rules.Action{{Type: rules.ActionAlert, Message: "down"}},
		}},
		Checks: func(context.Context, checks.Deps) map[string]checks.Result {
			return map[string]checks.Result{"http": {Check: "http", OK: false, Message: "refused"}}
		},
		Emit: func(e Event) { events = append(events, e) },
	}
	w.RunCycle(context.Background())
	for _, e := range events {
		if e.Rule == "" && e.Kind == eventKindFiring {
			t.Fatalf("emitted a check-health event for a check a nested rule reads: %+v", e)
		}
	}
}
