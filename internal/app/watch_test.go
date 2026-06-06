package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/rules"
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
