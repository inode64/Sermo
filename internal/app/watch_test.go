package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/rules"
	"sermo/internal/volume"
)

// stubCheck returns a fixed result for the watch cycle tests.
type stubCheck struct {
	name string
	ok   bool
	data map[string]any
}

func (c stubCheck) Name() string { return c.name }
func (c stubCheck) Run(context.Context) checks.Result {
	return checks.Result{Check: c.name, OK: c.ok, Data: c.data}
}

func TestWatchFiresHookWhenConditionTrue(t *testing.T) {
	var calls int
	var env map[string]string
	w := &Watch{
		Name:      "disk-root",
		CheckType: "disk",
		Check:     stubCheck{name: "disk", ok: true, data: map[string]any{"path": "/", "used_pct": 92.0}},
		Hook:      HookSpec{Command: []string{"/bin/true"}},
		Runner: HookRunnerFunc(func(_ context.Context, _ []string, e map[string]string, _ time.Duration) error {
			calls++
			env = e
			return nil
		}),
	}
	w.RunCycle(context.Background())
	if calls != 1 {
		t.Fatalf("expected hook to fire once, got %d", calls)
	}
	if env["SERMO_WATCH"] != "disk-root" || env["SERMO_PATH"] != "/" || env["SERMO_CHECK_TYPE"] != "disk" {
		t.Fatalf("unexpected env: %v", env)
	}
}

// fakeExpander records expansion calls and returns a canned result.
type fakeExpander struct {
	calls []string
	res   volume.Result
	err   error
}

func (e *fakeExpander) ExpandPath(_ context.Context, path string, by int64) (volume.Result, error) {
	e.calls = append(e.calls, fmt.Sprintf("%s:%d", path, by))
	return e.res, e.err
}

func TestWatchExpandFiresOnceThenCooldown(t *testing.T) {
	exp := &fakeExpander{res: volume.Result{VG: "vg0", LV: "data", GrewBytes: 5 << 30}}
	at := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	var events []Event
	w := &Watch{
		Name:      "expand-backup",
		CheckType: "disk",
		Check:     stubCheck{name: "disk", ok: true, data: map[string]any{"path": "/mnt/backup", "free_pct": 5.0}},
		Expand:    &ExpandSpec{By: 5 << 30},
		Expander:  exp,
		Policy:    rules.Policy{Cooldown: 30 * time.Minute},
		Now:       func() time.Time { return at },
		Emit:      func(e Event) { events = append(events, e) },
	}

	w.RunCycle(context.Background()) // fires + expands
	if len(exp.calls) != 1 || exp.calls[0] != "/mnt/backup:5368709120" {
		t.Fatalf("expand calls = %v, want one /mnt/backup:5368709120", exp.calls)
	}
	if !hasEventKind(events, "expand") {
		t.Fatalf("expected an 'expand' event, got %v", events)
	}

	// Same instant, still within cooldown: must NOT expand again.
	w.RunCycle(context.Background())
	if len(exp.calls) != 1 {
		t.Fatalf("expand must be suppressed within cooldown, calls = %v", exp.calls)
	}
	if !hasEventKind(events, "expand-skipped") {
		t.Fatalf("expected an 'expand-skipped' event on cooldown, got %v", events)
	}

	// After the cooldown elapses, it expands again.
	at = at.Add(31 * time.Minute)
	w.RunCycle(context.Background())
	if len(exp.calls) != 2 {
		t.Fatalf("expand must run again after cooldown, calls = %v", exp.calls)
	}
}

func TestWatchExpandFailureEmitsEvent(t *testing.T) {
	exp := &fakeExpander{err: errExpandTest}
	var events []Event
	w := &Watch{
		Name:      "expand-backup",
		CheckType: "disk",
		Check:     stubCheck{name: "disk", ok: true, data: map[string]any{"path": "/mnt/backup"}},
		Expand:    &ExpandSpec{By: 1 << 30},
		Expander:  exp,
		Emit:      func(e Event) { events = append(events, e) },
	}
	w.RunCycle(context.Background())
	if !hasEventKind(events, "expand-failed") {
		t.Fatalf("expected an 'expand-failed' event, got %v", events)
	}
}

func hasEventKind(events []Event, kind string) bool {
	for _, e := range events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

var errExpandTest = errTest("boom")

type errTest string

func (e errTest) Error() string { return string(e) }

func TestHookEnvMapsAllDataKeys(t *testing.T) {
	res := checks.Result{
		Check:   "net-eth0",
		Message: "eth0 state up->down",
		Data: map[string]any{
			"interface": "eth0",
			"metric":    "state",
			"old":       "up",
			"new":       "down",
			"value":     "down",
		},
	}
	env := hookEnv("net-eth0", "net", res)
	if env["SERMO_WATCH"] != "net-eth0" || env["SERMO_CHECK_TYPE"] != "net" || env["SERMO_MESSAGE"] != "eth0 state up->down" {
		t.Fatalf("base env wrong: %v", env)
	}
	for k, want := range map[string]string{
		"SERMO_INTERFACE": "eth0",
		"SERMO_METRIC":    "state",
		"SERMO_OLD":       "up",
		"SERMO_NEW":       "down",
		"SERMO_VALUE":     "down",
	} {
		if env[k] != want {
			t.Fatalf("env[%s] = %q, want %q (full: %v)", k, env[k], want, env)
		}
	}
}

func TestHookEnvDiskKeysStillWork(t *testing.T) {
	// Disk Data with a `value` key (set by the disk check) yields SERMO_PATH + SERMO_VALUE.
	res := checks.Result{Data: map[string]any{"path": "/", "value": 92.0, "used_pct": 92.0}}
	env := hookEnv("disk-root", "disk", res)
	if env["SERMO_PATH"] != "/" || env["SERMO_VALUE"] != "92" {
		t.Fatalf("disk env wrong: %v", env)
	}
}

func TestWatchDoesNotFireWhenConditionFalse(t *testing.T) {
	var calls int
	w := &Watch{
		Name:   "disk-root",
		Check:  stubCheck{name: "disk", ok: false},
		Hook:   HookSpec{Command: []string{"/bin/true"}},
		Runner: HookRunnerFunc(func(context.Context, []string, map[string]string, time.Duration) error { calls++; return nil }),
	}
	w.RunCycle(context.Background())
	if calls != 0 {
		t.Fatalf("expected no hook, got %d", calls)
	}
}

func TestWatchForWindowRequiresConsecutive(t *testing.T) {
	var calls int
	w := &Watch{
		Name:   "disk-root",
		Check:  stubCheck{name: "disk", ok: true},
		Window: rules.Rule{For: &rules.ForWindow{Cycles: 3}},
		Hook:   HookSpec{Command: []string{"/bin/true"}},
		Runner: HookRunnerFunc(func(context.Context, []string, map[string]string, time.Duration) error { calls++; return nil }),
	}
	w.RunCycle(context.Background()) // 1
	w.RunCycle(context.Background()) // 2
	if calls != 0 {
		t.Fatalf("fired before 3 consecutive cycles: %d", calls)
	}
	w.RunCycle(context.Background()) // 3 -> fire
	if calls != 1 {
		t.Fatalf("expected fire on 3rd cycle, got %d", calls)
	}
}

func TestWatchWithRealOSHookRunner(t *testing.T) {
	var hookEvents []Event
	w := &Watch{
		Name:      "disk-root",
		CheckType: "disk",
		Check:     stubCheck{name: "disk", ok: true, data: map[string]any{"path": "/"}},
		Hook:      HookSpec{Command: []string{"/bin/true"}, Timeout: time.Second},
		// Use the real OSHookRunner (which now goes through execx) instead of mock Func.
		// This exercises defaultHookRunner path + real execution in a Watch context.
		Runner: OSHookRunner{},
		Emit: func(e Event) {
			if e.Kind == "hook" || e.Kind == "hook-failed" {
				hookEvents = append(hookEvents, e)
			}
		},
	}
	w.RunCycle(context.Background())
	if len(hookEvents) != 1 {
		t.Fatalf("expected 1 hook event, got %d: %v", len(hookEvents), hookEvents)
	}
	if hookEvents[0].Kind != "hook" {
		t.Fatalf("expected hook success event, got %s", hookEvents[0].Kind)
	}
}
