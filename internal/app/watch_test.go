package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/notify"
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

type countingCheck struct {
	calls int
}

func (c *countingCheck) Name() string { return "counting" }
func (c *countingCheck) Run(context.Context) checks.Result {
	c.calls++
	return checks.Result{Check: "counting", OK: true}
}

type scriptedCheck struct {
	results []checks.Result
	calls   int
}

func (c *scriptedCheck) Name() string { return "scripted" }
func (c *scriptedCheck) Run(context.Context) checks.Result {
	if c.calls >= len(c.results) {
		c.calls++
		return c.results[len(c.results)-1]
	}
	res := c.results[c.calls]
	c.calls++
	return res
}

func TestWatchPausedSkipsCheck(t *testing.T) {
	check := &countingCheck{}
	w := &Watch{
		Name:     "storage-root",
		Check:    check,
		IsPaused: func() bool { return true },
	}
	w.RunCycle(context.Background())
	if check.calls != 0 {
		t.Fatalf("paused watch ran check %d times", check.calls)
	}
}

func TestWatchPublishesResultSnapshot(t *testing.T) {
	snapshots := NewWatchSnapshots()
	w := &Watch{
		Name:      "disk",
		CheckType: checks.CheckTypeHdparm,
		Check: stubCheck{name: "disk", ok: false, data: map[string]any{
			checks.DataKeyDevice:   "/dev/sda",
			checks.HdparmFieldRead: 500.0,
		}},
		Publish: publishWatchSnapshots(snapshots),
	}

	w.RunCycle(context.Background())

	got := snapshots.Get("disk", checks.CheckTypeHdparm)
	if len(got) != 1 || got[0].Data[checks.HdparmFieldRead] != 500.0 {
		t.Fatalf("published snapshot = %+v, want hdparm reading", got)
	}
}

func TestWatchFiresHookWhenConditionTrue(t *testing.T) {
	var calls int
	var env map[string]string
	w := &Watch{
		Name:      "storage-root",
		CheckType: "storage",
		Check:     stubCheck{name: "storage", ok: true, data: map[string]any{"path": "/", "used_pct": 92.0}},
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
	if env["SERMO_WATCH"] != "storage-root" || env["SERMO_PATH"] != "/" || env["SERMO_CHECK_TYPE"] != "storage" {
		t.Fatalf("unexpected env: %v", env)
	}
}

func TestWatchPanicSuppressesHookButStillFires(t *testing.T) {
	var calls int
	var events []Event
	w := &Watch{
		Name:      "storage-root",
		CheckType: "storage",
		Check:     stubCheck{name: "storage", ok: true, data: map[string]any{"path": "/", "used_pct": 92.0}},
		Hook:      HookSpec{Command: []string{"/bin/true"}},
		Runner: HookRunnerFunc(func(_ context.Context, _ []string, _ map[string]string, _ time.Duration) error {
			calls++
			return nil
		}),
		InPanic: func() bool { return true },
		Emit:    func(e Event) { events = append(events, e) },
	}
	w.RunCycle(context.Background())

	if calls != 0 {
		t.Fatalf("panic mode must suppress the hook, ran %d times", calls)
	}
	kinds := eventKinds(events)
	if strings.Join(kinds, ",") != "firing,panic-suppressed" {
		t.Fatalf("event kinds = %v, want firing,panic-suppressed", kinds)
	}
}

func TestWatchEmitsRecoveredAfterFiringClears(t *testing.T) {
	check := &scriptedCheck{results: []checks.Result{
		{Check: "dns", OK: false, Message: "dns timeout"},
		{Check: "dns", OK: true, Message: "dns ok"},
		{Check: "dns", OK: true, Message: "dns ok"},
	}}
	var events []Event
	w := &Watch{
		Name:       "uplink-dns",
		CheckType:  "dns",
		Check:      check,
		FireOnFail: true,
		Emit:       func(e Event) { events = append(events, e) },
	}

	w.RunCycle(context.Background())
	w.RunCycle(context.Background())
	w.RunCycle(context.Background())

	if got := eventKinds(events); strings.Join(got, ",") != "firing,recovered" {
		t.Fatalf("event kinds = %v, want firing,recovered", got)
	}
	if events[1].Message != "dns ok" {
		t.Fatalf("recovered message = %q, want dns ok", events[1].Message)
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
		CheckType: "storage",
		Check:     stubCheck{name: "storage", ok: true, data: map[string]any{"path": "/mnt/backup", "free_pct": 5.0}},
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
	if !hasEventKind(events, eventKindExpand) {
		t.Fatalf("expected an 'expand' event, got %v", events)
	}

	// Same instant, still within cooldown: must NOT expand again.
	w.RunCycle(context.Background())
	if len(exp.calls) != 1 {
		t.Fatalf("expand must be suppressed within cooldown, calls = %v", exp.calls)
	}
	if !hasEventKind(events, eventKindExpandSkipped) {
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
		CheckType: "storage",
		Check:     stubCheck{name: "storage", ok: true, data: map[string]any{"path": "/mnt/backup"}},
		Expand:    &ExpandSpec{By: 1 << 30},
		Expander:  exp,
		Emit:      func(e Event) { events = append(events, e) },
	}
	w.RunCycle(context.Background())
	if !hasEventKind(events, eventKindExpandFailed) {
		t.Fatalf("expected an 'expand-failed' event, got %v", events)
	}
}

func TestWatchDryRunSkipsHookNotifyAndExpand(t *testing.T) {
	exp := &fakeExpander{res: volume.Result{VG: "vg0", LV: "data", GrewBytes: 1 << 30}}
	n := &fakeNotifier{name: "ops"}
	var calls int
	var events []Event
	at := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	w := &Watch{
		Name:      "dry-storage",
		CheckType: "storage",
		Check:     stubCheck{name: "storage", ok: true, data: map[string]any{"path": "/data"}},
		Hook:      HookSpec{Command: []string{"/usr/local/bin/alert"}},
		Runner: HookRunnerFunc(func(context.Context, []string, map[string]string, time.Duration) error {
			calls++
			return nil
		}),
		Notifiers: []notify.Notifier{n},
		DryRun:    true,
		Expand:    &ExpandSpec{By: 1 << 30},
		Expander:  exp,
		Policy:    rules.Policy{Cooldown: time.Hour},
		Now:       func() time.Time { return at },
		Emit:      func(e Event) { events = append(events, e) },
	}
	w.policyState.LastActionAt = at.Add(-time.Minute)
	w.RunCycle(context.Background())
	if calls != 0 {
		t.Fatalf("dry-run must not execute hook, got %d calls", calls)
	}
	if len(n.msgs) != 0 {
		t.Fatalf("dry-run must not notify, got %d messages", len(n.msgs))
	}
	if len(exp.calls) != 0 {
		t.Fatalf("dry-run must not expand, calls = %v", exp.calls)
	}
	if !hasEventKind(events, eventKindFiring) || !hasEventKind(events, eventKindDryRun) {
		t.Fatalf("dry-run should emit firing and dry-run events, got %v", events)
	}
	if !hasEventMessage(events, eventKindDryRun, "suppressed: cooldown") {
		t.Fatalf("dry-run should report expand policy suppression, got %v", events)
	}
	if hasEventKind(events, eventKindHook) || hasEventKind(events, eventKindNotify) || hasEventKind(events, eventKindExpand) {
		t.Fatalf("dry-run emitted side-effect event: %v", events)
	}
}

func TestWatchDryRunSendsOnlyWallNotify(t *testing.T) {
	email := &fakeNotifier{name: "ops-email", typ: "email"}
	wall := &fakeNotifier{name: "wall", typ: "wall"}
	var events []Event
	w := &Watch{
		Name:      "dry-storage",
		CheckType: "storage",
		Check:     stubCheck{name: "storage", ok: true, data: map[string]any{"path": "/data"}},
		Notifiers: []notify.Notifier{email, wall},
		DryRun:    true,
		Emit:      func(e Event) { events = append(events, e) },
	}

	w.RunCycle(context.Background())

	if len(email.msgs) != 0 {
		t.Fatalf("dry-run must suppress non-console notifications, got %d", len(email.msgs))
	}
	if len(wall.msgs) != 1 {
		t.Fatalf("dry-run must still send wall notification, got %d", len(wall.msgs))
	}
	if !hasEventKind(events, eventKindNotify) {
		t.Fatalf("wall notification should emit notify event, got %v", events)
	}
}

func TestWatchStartupObserveOnlySkipsFiring(t *testing.T) {
	n := &fakeNotifier{name: "ops"}
	var events []Event
	settling := NewSettling(nil)
	settling.Reset([]string{SettlingWatchKey("storage-root")})
	w := &Watch{
		Name:      "storage-root",
		CheckType: "storage",
		Check:     stubCheck{name: "storage", ok: true, data: map[string]any{"path": "/"}},
		Notifiers: []notify.Notifier{n},
		Settling:  settling,
		Emit:      func(e Event) { events = append(events, e) },
	}

	w.RunCycle(context.Background())
	if len(n.msgs) != 0 || hasEventKind(events, eventKindFiring) {
		t.Fatalf("observe-only watch must not fire or notify, events=%v msgs=%d", events, len(n.msgs))
	}
	if !settling.Observed(SettlingWatchKey("storage-root")) {
		t.Fatal("observe-only watch must mark the watch observed")
	}

	w.RunCycle(context.Background())
	if !hasEventKind(events, eventKindFiring) {
		t.Fatalf("second cycle must emit firing, events=%v", events)
	}
}

func TestWatchNotifiesOnceByDefault(t *testing.T) {
	n := &fakeNotifier{name: "ops"}
	at := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	w := &Watch{
		Name:      "storage-root",
		CheckType: "storage",
		Check:     stubCheck{name: "storage", ok: true, data: map[string]any{"path": "/"}},
		Notifiers: []notify.Notifier{n},
		Now:       func() time.Time { return at },
		Emit:      func(Event) {},
	}
	for i := 0; i < 3; i++ {
		w.RunCycle(context.Background())
	}
	if len(n.msgs) != 1 {
		t.Fatalf("default watch must notify once per firing episode, got %d", len(n.msgs))
	}
}

func TestWatchReNotifiesAfterInterval(t *testing.T) {
	n := &fakeNotifier{name: "ops"}
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	w := &Watch{
		Name:           "storage-root",
		CheckType:      "storage",
		Check:          stubCheck{name: "storage", ok: true, data: map[string]any{"path": "/"}},
		Notifiers:      []notify.Notifier{n},
		NotifyInterval: 10 * time.Minute,
		Now:            func() time.Time { return now },
		Emit:           func(Event) {},
	}
	w.RunCycle(context.Background()) // rising edge → notify (1)
	now = now.Add(5 * time.Minute)
	w.RunCycle(context.Background()) // within interval → no notify
	now = now.Add(5 * time.Minute)
	w.RunCycle(context.Background()) // interval elapsed → notify (2)
	if len(n.msgs) != 2 {
		t.Fatalf("expected re-notify after the interval, got %d notifications", len(n.msgs))
	}
}

func TestWatchReNotifiesOnNewEpisode(t *testing.T) {
	n := &fakeNotifier{name: "ops"}
	at := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	w := &Watch{
		Name:      "storage-root",
		CheckType: "storage",
		Check:     stubCheck{name: "storage", ok: true, data: map[string]any{"path": "/"}},
		Notifiers: []notify.Notifier{n},
		Now:       func() time.Time { return at },
		Emit:      func(Event) {},
	}
	w.RunCycle(context.Background()) // firing → notify (1)
	w.RunCycle(context.Background()) // still firing → no notify
	w.Check = stubCheck{name: "storage", ok: false}
	w.RunCycle(context.Background()) // recovered → reset
	w.Check = stubCheck{name: "storage", ok: true, data: map[string]any{"path": "/"}}
	w.RunCycle(context.Background()) // new episode → notify (2)
	if len(n.msgs) != 2 {
		t.Fatalf("a new firing episode must notify again, got %d notifications", len(n.msgs))
	}
}

func hasEventMessage(events []Event, kind, substr string) bool {
	for _, e := range events {
		if e.Kind == kind && strings.Contains(e.Message, substr) {
			return true
		}
	}
	return false
}

func hasEventKind(events []Event, kind string) bool {
	for _, e := range events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

func eventKinds(events []Event) []string {
	kinds := make([]string, 0, len(events))
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	return kinds
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

func TestHookEnvStorageKeysStillWork(t *testing.T) {
	// Storage check data with a `value` key yields SERMO_PATH + SERMO_VALUE.
	res := checks.Result{Data: map[string]any{"path": "/", "value": 92.0, "used_pct": 92.0}}
	env := hookEnv("storage-root", "storage", res)
	if env["SERMO_PATH"] != "/" || env["SERMO_VALUE"] != "92" {
		t.Fatalf("storage env wrong: %v", env)
	}
}

func TestHookEnvTrimsCapturedText(t *testing.T) {
	res := checks.Result{
		Message: "\nSQL threshold fired\n",
		Data: map[string]any{
			"result": "\nready\n",
		},
	}
	env := hookEnv("sql-health", "sql", res)
	if env["SERMO_MESSAGE"] != "SQL threshold fired" || env["SERMO_RESULT"] != "ready" {
		t.Fatalf("env should carry trimmed captured text: %v", env)
	}
}

func TestWatchDoesNotFireWhenConditionFalse(t *testing.T) {
	var calls int
	w := &Watch{
		Name:   "storage-root",
		Check:  stubCheck{name: "storage", ok: false},
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
		Name:   "storage-root",
		Check:  stubCheck{name: "storage", ok: true},
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

func TestWatchForDurationRequiresElapsedTime(t *testing.T) {
	var calls int
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	w := &Watch{
		Name:   "storage-root",
		Check:  stubCheck{name: "storage", ok: true},
		Window: rules.Rule{For: &rules.ForWindow{Duration: 6 * time.Minute}},
		Hook:   HookSpec{Command: []string{"/bin/true"}},
		Runner: HookRunnerFunc(func(context.Context, []string, map[string]string, time.Duration) error { calls++; return nil }),
		Now:    func() time.Time { return now },
	}
	w.RunCycle(context.Background())
	now = now.Add(5 * time.Minute)
	w.RunCycle(context.Background())
	if calls != 0 {
		t.Fatalf("fired before duration elapsed: %d", calls)
	}
	now = now.Add(time.Minute)
	w.RunCycle(context.Background())
	if calls != 1 {
		t.Fatalf("expected fire after duration elapsed, got %d", calls)
	}
}

func TestWatchWithRealOSHookRunner(t *testing.T) {
	var hookEvents []Event
	w := &Watch{
		Name:      "storage-root",
		CheckType: "storage",
		Check:     stubCheck{name: "storage", ok: true, data: map[string]any{"path": "/"}},
		Hook:      HookSpec{Command: []string{"/bin/true"}, Timeout: time.Second},
		// Use the real OSHookRunner (which now goes through execx) instead of mock Func.
		// This exercises defaultHookRunner path + real execution in a Watch context.
		Runner: OSHookRunner{},
		Emit: func(e Event) {
			if e.Kind == eventKindHook || e.Kind == eventKindHookFail {
				hookEvents = append(hookEvents, e)
			}
		},
	}
	w.RunCycle(context.Background())
	if len(hookEvents) != 1 {
		t.Fatalf("expected 1 hook event, got %d: %v", len(hookEvents), hookEvents)
	}
	if hookEvents[0].Kind != eventKindHook {
		t.Fatalf("expected hook success event, got %s", hookEvents[0].Kind)
	}
}
