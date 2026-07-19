package app

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/emission"
	"sermo/internal/execx"
	"sermo/internal/notify"
	"sermo/internal/rules"
)

func cfgWithWatches(raw map[string]any) *config.Config {
	return &config.Config{Global: config.Global{Raw: map[string]any{"watches": raw}}}
}

func cfgWithWatchDefaults(raw, defaults map[string]any) *config.Config {
	return &config.Config{Global: config.Global{Raw: map[string]any{"watches": raw}, Defaults: defaults}}
}

type typedNotifier struct {
	name string
	typ  string
}

func (n typedNotifier) Name() string { return n.name }
func (n typedNotifier) Type() string { return n.typ }
func (n typedNotifier) Send(context.Context, notify.Message) error {
	return nil
}

func TestBuildWatchesProcessKillAction(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"kill-stale-sudo": map[string]any{
			"check": map[string]any{"type": "process", "name": "/usr/bin/sudo", "user": "root", "for": "120m"},
			"then":  map[string]any{"kill": map[string]any{"signal": "TERM"}},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{DefaultTimeout: time.Second, ExecxRunner: execx.CommandRunner{}}, 30*time.Second)
	if len(warns) != 0 {
		t.Fatalf("a kill-only process watch should build cleanly, got warnings: %v", warns)
	}
	if len(watches) != 1 {
		t.Fatalf("expected 1 watch, got %d", len(watches))
	}
	if watches[0].CheckType != "process" {
		t.Fatalf("check type = %q, want process", watches[0].CheckType)
	}
}

func TestBuildWatchesProcessKillActionRejectsUnsafeSelector(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"kill-stale-sudo": map[string]any{
			"check": map[string]any{"type": "process", "name": "sudo", "for": "120m"},
			"then":  map[string]any{"kill": map[string]any{"signal": "TERM"}},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{DefaultTimeout: time.Second, ExecxRunner: execx.CommandRunner{}}, 30*time.Second)
	if len(watches) != 0 {
		t.Fatalf("unsafe kill watch should not build, got %d watches", len(watches))
	}
	if len(warns) != 1 || warns[0] != "watch kill-stale-sudo: then.kill requires check.name to be an absolute resolved exe path" {
		t.Fatalf("warnings = %v", warns)
	}
}

func TestBuildWatchesStorageExpandAction(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"expand-backup": map[string]any{
			"dry_run": true,
			"check": map[string]any{
				"type":     "storage",
				"path":     "/mnt/backup",
				"free_pct": map[string]any{"op": "<", "value": 10},
			},
			"policy": map[string]any{"cooldown": "30m"},
			"then":   map[string]any{"expand": map[string]any{"by": "5G"}},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{DefaultTimeout: time.Second, ExecxRunner: execx.CommandRunner{}}, 30*time.Second)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(watches) != 1 {
		t.Fatalf("expected 1 watch, got %d", len(watches))
	}
	w := watches[0]
	if w.Expand == nil || w.Expand.By != 5<<30 {
		t.Fatalf("expand not parsed: %+v", w.Expand)
	}
	if !w.DryRun {
		t.Fatal("dry_run not parsed")
	}
	if w.Expander == nil {
		t.Fatal("expander must be injected")
	}
	if w.Policy.Cooldown != 30*time.Minute {
		t.Fatalf("policy cooldown = %v, want 30m", w.Policy.Cooldown)
	}
	if w.CheckType != "storage" {
		t.Fatalf("check type = %q, want storage", w.CheckType)
	}
}

func TestBuildWatchesEmissionOverridesGlobal(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"storage-root": map[string]any{
			"check": map[string]any{
				"type":     "storage",
				"path":     "/",
				"used_pct": map[string]any{"op": ">", "value": 90},
			},
			"emission": map[string]any{"events": "every_cycle"},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{
		DefaultTimeout: time.Second,
		GlobalEmission: emission.Policy{
			Events: emission.ModeOnChange,
			Notify: emission.ModeEveryCycle,
		},
	}, 30*time.Second)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(watches) != 1 {
		t.Fatalf("expected 1 watch, got %d", len(watches))
	}
	if watches[0].Emission.Events != emission.ModeEveryCycle {
		t.Fatalf("watch events emission = %q, want every_cycle", watches[0].Emission.Events)
	}
	if watches[0].Emission.Notify != emission.ModeEveryCycle {
		t.Fatalf("watch notify emission = %q, want inherited every_cycle", watches[0].Emission.Notify)
	}
}

func TestBuildWatchesClearWindowFallback(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"inherits-default": map[string]any{
			"check": map[string]any{
				"type":     "storage",
				"path":     "/",
				"used_pct": map[string]any{"op": ">", "value": 90},
			},
		},
		"own-clear": map[string]any{
			"check": map[string]any{
				"type":     "storage",
				"path":     "/var",
				"used_pct": map[string]any{"op": ">", "value": 90},
			},
			"clear": map[string]any{"cycles": 1},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{
		DefaultTimeout: time.Second,
		GlobalClear:    &rules.ForWindow{Duration: rules.DefaultClearWindow},
	}, 30*time.Second)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	byName := map[string]*Watch{}
	for _, w := range watches {
		byName[w.Name] = w
	}
	if w := byName["inherits-default"]; w == nil || w.Window.Clear == nil || w.Window.Clear.Duration != rules.DefaultClearWindow {
		t.Fatalf("watch without clear must inherit the global fallback, got %+v", w.Window.Clear)
	}
	if w := byName["own-clear"]; w == nil || w.Window.Clear == nil || w.Window.Clear.Cycles != 1 {
		t.Fatalf("watch with its own clear must keep it, got %+v", w.Window.Clear)
	}
}

func TestBuildWatchesDryRunFromDefaultsCanBeOverridden(t *testing.T) {
	cfg := cfgWithWatchDefaults(map[string]any{
		"inherited": map[string]any{
			"check": map[string]any{"type": "load", "load1": map[string]any{"op": ">", "value": 10}},
			"then":  map[string]any{"notify": []any{"ops"}},
		},
		"live": map[string]any{
			"dry_run": false,
			"check":   map[string]any{"type": "load", "load1": map[string]any{"op": ">", "value": 20}},
			"then":    map[string]any{"notify": []any{"ops"}},
		},
	}, map[string]any{"dry_run": true})
	watches, warns := BuildWatches(cfg, Deps{
		Notifiers: map[string]notify.Notifier{"ops": &fakeNotifier{name: "ops"}},
	}, 30*time.Second)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	byName := map[string]*Watch{}
	for _, w := range watches {
		byName[w.Name] = w
	}
	if !byName["inherited"].DryRun {
		t.Fatal("defaults.dry_run should enable dry-run for watches")
	}
	if byName["live"].DryRun {
		t.Fatal("watch dry_run false should override defaults.dry_run")
	}
}

func TestBuildWatchesStorageExpandNotifyNoneSuppressesDefault(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"expand-backup": map[string]any{
			"check": map[string]any{
				"type":     "storage",
				"path":     "/mnt/backup",
				"free_pct": map[string]any{"op": "<", "value": 10},
			},
			"then": map[string]any{
				"notify": "none",
				"expand": map[string]any{"by": "5G"},
			},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{
		DefaultTimeout: time.Second,
		ExecxRunner:    execx.CommandRunner{},
		GlobalNotify:   []string{"ops"},
		Notifiers:      map[string]notify.Notifier{"ops": &fakeNotifier{name: "ops"}},
	}, 30*time.Second)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(watches) != 1 {
		t.Fatalf("expected 1 watch, got %d", len(watches))
	}
	if watches[0].Expand == nil {
		t.Fatalf("expand not parsed: %+v", watches[0])
	}
	if watches[0].CheckType != "storage" {
		t.Fatalf("check type = %q, want storage", watches[0].CheckType)
	}
	if len(watches[0].Notifiers) != 0 {
		t.Fatalf("notify none must suppress global default, got %v", watches[0].Notifiers)
	}
}

func TestResolveNotifiersWallSuppressesTTY(t *testing.T) {
	got := resolveNotifiers([]string{"tty", "wall"}, map[string]notify.Notifier{
		"tty":  typedNotifier{name: "tty", typ: "tty"},
		"wall": typedNotifier{name: "wall", typ: "wall"},
	})
	if len(got) != 1 || got[0].Name() != "wall" {
		t.Fatalf("resolveNotifiers = %v, want wall only", got)
	}
}

func TestBuildWatchesAbsentThenIsPureMonitorOnlyStorage(t *testing.T) {
	// Bare storage watch (no then): alert-only, globals ignored.
	cfg := cfgWithWatches(map[string]any{
		"storage-root": map[string]any{
			"check": map[string]any{
				"type":     "storage",
				"path":     "/",
				"used_pct": map[string]any{"op": ">=", "value": 90},
			},
			// no "then" key (bare = pure alert-only)
		},
	})
	watches, warns := BuildWatches(cfg, Deps{
		DefaultTimeout: time.Second,
		GlobalNotify:   []string{"ops"},
		Notifiers:      map[string]notify.Notifier{"ops": &fakeNotifier{name: "ops"}},
	}, 30*time.Second)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(watches) != 1 {
		t.Fatalf("expected 1 watch, got %d", len(watches))
	}
	if len(watches[0].Notifiers) != 0 {
		t.Fatalf("bare disk (absent then) must not inherit globals (pure monitor-only), got %v", watches[0].Notifiers)
	}
}

func TestBuildWatchesAppliesWatchMonitorMode(t *testing.T) {
	tests := []struct {
		name          string
		mode          any
		initialFound  bool
		initialActive bool
		wantActive    bool
		wantPaused    bool
	}{
		{name: "default enabled forces active", initialFound: true, initialActive: false, wantActive: true},
		{name: "explicit enabled forces active", mode: config.MonitorEnabled, initialFound: true, initialActive: false, wantActive: true},
		{name: "disabled forces paused", mode: config.MonitorDisabled, initialFound: true, initialActive: true, wantActive: false, wantPaused: true},
		{name: "previous preserves paused state", mode: config.MonitorPrevious, initialFound: true, initialActive: false, wantActive: false, wantPaused: true},
		{name: "previous first run defaults active", mode: config.MonitorPrevious, wantActive: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore()
			if tt.initialFound {
				store.active[watchMonitorKey("storage-root")] = tt.initialActive
			}
			entry := map[string]any{
				"check": map[string]any{
					"type":     "storage",
					"path":     "/",
					"used_pct": map[string]any{"op": ">=", "value": 90},
				},
				"then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			}
			if tt.mode != nil {
				entry["monitor"] = tt.mode
			}
			cfg := cfgWithWatches(map[string]any{"storage-root": entry})
			watches, warns := BuildWatches(cfg, Deps{Monitor: store, DefaultTimeout: time.Second}, 30*time.Second)
			if len(warns) != 0 || len(watches) != 1 {
				t.Fatalf("watches=%d warnings=%v", len(watches), warns)
			}
			if got := store.active[watchMonitorKey("storage-root")]; got != tt.wantActive {
				t.Fatalf("stored active = %v, want %v", got, tt.wantActive)
			}
			if got := watches[0].IsPaused != nil && watches[0].IsPaused(); got != tt.wantPaused {
				t.Fatalf("paused = %v, want %v", got, tt.wantPaused)
			}
		})
	}
}

func TestBuildWatchesNotifyNoneIsMonitorOnly(t *testing.T) {
	// The explicit `notify: [none]` opt-out builds the watch (state visible in
	// the dashboard and events) with no notifiers and no warning — unlike an
	// empty then, which stays rejected.
	cfg := cfgWithWatches(map[string]any{
		"storage-root": map[string]any{
			"check": map[string]any{
				"type":     "storage",
				"path":     "/",
				"used_pct": map[string]any{"op": ">=", "value": 90},
			},
			"then": map[string]any{"notify": []any{"none"}},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{DefaultTimeout: time.Second}, 30*time.Second)
	if len(watches) != 1 || len(warns) != 0 {
		t.Fatalf("watches=%d warns=%v, want one monitor-only watch", len(watches), warns)
	}
	if len(watches[0].Notifiers) != 0 || len(watches[0].Hook.Command) != 0 {
		t.Fatalf("opted-out watch must carry no notifiers/hook: %+v", watches[0])
	}
}

func TestBuildWatchesAbsentThenIsPureMonitorOnly(t *testing.T) {
	// Omitting `then` entirely: builds as pure alert-only (firing events for
	// web+log), globals are ignored, zero notifiers/hook, Cycle still wired.
	cfg := cfgWithWatches(map[string]any{
		"app-data": map[string]any{
			"check": map[string]any{
				"type": "file",
				"path": "/var/lib/app",
				"size": map[string]any{"on": "change"},
			},
			// no then key
		},
	})
	watches, warns := BuildWatches(cfg, Deps{
		DefaultTimeout: time.Second,
		GlobalNotify:   []string{"ops"},
		Notifiers:      map[string]notify.Notifier{"ops": &fakeNotifier{name: "ops"}},
	}, 30*time.Second)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(watches) != 1 {
		t.Fatalf("expected 1 watch, got %d", len(watches))
	}
	if watches[0].Cycle == nil {
		t.Fatal("file watch must wire a Cycle override")
	}
	if len(watches[0].Notifiers) != 0 || len(watches[0].Hook.Command) != 0 {
		t.Fatalf("absent-then must have no notifiers/hook (pure monitor-only): %+v", watches[0])
	}
}

func TestBuildWatchesExpandRejectedOnNonStorage(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"bad-expand": map[string]any{
			"check": map[string]any{"type": "load", "load1": map[string]any{"op": ">=", "value": 10}},
			"then":  map[string]any{"expand": map[string]any{"by": "5G"}},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{DefaultTimeout: time.Second, ExecxRunner: execx.CommandRunner{}}, 30*time.Second)
	if len(watches) != 0 || len(warns) == 0 {
		t.Fatalf("then.expand on a non-storage watch must warn and not build: watches=%d warns=%v", len(watches), warns)
	}
}

func TestBuildWatchesBuildsStorage(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"storage-root": map[string]any{
			"check": map[string]any{
				"type":     "storage",
				"path":     "/",
				"used_pct": map[string]any{"op": ">=", "value": 90},
			},
			"for":  map[string]any{"cycles": 3},
			"then": map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/alert.sh"}, "expect_exit": []any{0, 1}}},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{DefaultTimeout: time.Second, ExecxRunner: execx.CommandRunner{}}, 30*time.Second)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(watches) != 1 {
		t.Fatalf("expected 1 watch, got %d", len(watches))
	}
	w := watches[0]
	if w.Name != "storage-root" || w.CheckType != "storage" || w.Interval != 30*time.Second {
		t.Fatalf("unexpected watch: %+v", w)
	}
	if w.Window.For == nil || w.Window.For.Cycles != 3 {
		t.Fatalf("for window not parsed: %+v", w.Window)
	}
	if len(w.Hook.Command) != 1 {
		t.Fatalf("hook command not parsed: %+v", w.Hook)
	}
	if !slices.Equal(w.Hook.ExpectExit, []int{0, 1}) {
		t.Fatalf("hook expect_exit not parsed: %+v", w.Hook.ExpectExit)
	}
}

func TestBuildWatchesBuildsSingleWatch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		watchName string
		checkType string
		watch     map[string]any
	}{
		{
			name:      "file",
			watchName: "app-data",
			checkType: "file",
			watch: map[string]any{
				"check": map[string]any{
					"type":        "file",
					"paths":       []any{"/var/lib/app", "/srv/app"},
					"recursive":   true,
					"older_than":  "24h",
					"size":        map[string]any{"op": ">", "value": 1024},
					"permissions": map[string]any{"on": "change"},
					"existence":   map[string]any{"on": "delete"},
				},
				"then": map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/file.sh"}}},
			},
		},
		{
			name:      "process",
			watchName: "hot-workers",
			checkType: "process",
			watch: map[string]any{
				"check": map[string]any{
					"type":   "process",
					"name":   "myworker",
					"user":   "www-data",
					"for":    "5m",
					"cpu":    map[string]any{"op": ">", "value": 80},
					"memory": map[string]any{"op": ">", "value": 524288000},
					"io":     map[string]any{"op": ">", "value": 10485760},
				},
				"then": map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/proc.sh"}}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := cfgWithWatches(map[string]any{tc.watchName: tc.watch})
			watches, warns := BuildWatches(cfg, Deps{DefaultTimeout: time.Second, ExecxRunner: execx.CommandRunner{}}, 30*time.Second)
			if len(warns) != 0 {
				t.Fatalf("unexpected warnings: %v", warns)
			}
			if len(watches) != 1 {
				t.Fatalf("expected 1 watch, got %d", len(watches))
			}
			w := watches[0]
			if w.Name != tc.watchName || w.CheckType != tc.checkType || w.Interval != 30*time.Second {
				t.Fatalf("unexpected watch: %+v", w)
			}
			if w.Cycle == nil {
				t.Fatal("watch must wire a Cycle override")
			}
		})
	}
}

func TestBuildWatchesWarnsOnCheckWithoutCondition(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check map[string]any
	}{
		{"file", map[string]any{"type": "file", "path": "/x"}},
		{"process", map[string]any{"type": "process", "name": "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := cfgWithWatches(map[string]any{
				"bad": map[string]any{
					"check": tc.check,
					"then":  map[string]any{"hook": map[string]any{"command": []any{"/x.sh"}}},
				},
			})
			watches, warns := BuildWatches(cfg, Deps{}, time.Second)
			if len(watches) != 0 || len(warns) == 0 {
				t.Fatalf("expected a warning and no watch, got %d watches, warns %v", len(watches), warns)
			}
		})
	}
}

func TestNewStatefulWatch(t *testing.T) {
	now := func() time.Time { return time.Unix(0, 0) }
	emit := func(Event) {}
	cycle := func(context.Context) {}

	watch := newStatefulWatch("stateful", "file", map[string]any{}, Deps{Now: now, Emit: emit}, time.Minute, cycle)
	if watch.Name != "stateful" || watch.CheckType != "file" || watch.Interval != time.Minute {
		t.Fatalf("unexpected stateful watch: %+v", watch)
	}
	if watch.IsPaused == nil || watch.IsPaused() {
		t.Fatal("stateful watch must default to active monitoring")
	}
	if watch.Now == nil || watch.Emit == nil || watch.Cycle == nil {
		t.Fatalf("stateful watch must preserve runtime callbacks: %+v", watch)
	}
}

func TestNewCheckWatch(t *testing.T) {
	now := func() time.Time { return time.Unix(0, 0) }
	emit := func(Event) {}
	watch := newCheckWatch(checkWatchSpec{
		name:      "api",
		checkType: "http",
		actions:   watchActions{hook: HookSpec{Command: []string{"/bin/alert"}}, notifyInterval: time.Minute},
		dryRun:    true,
		interval:  30 * time.Second,
		stateSlot: "metric:latency",
	}, Deps{Now: now, Emit: emit})

	if watch.Name != "api" || watch.CheckType != "http" || watch.Interval != 30*time.Second || !watch.DryRun {
		t.Fatalf("unexpected check watch: %+v", watch)
	}
	if !watch.FireOnFail || watch.StateSlot != "metric:latency" || len(watch.Hook.Command) != 1 {
		t.Fatalf("check watch lost check-specific fields: %+v", watch)
	}
	if watch.IsPaused == nil || watch.IsPaused() || watch.Now == nil || watch.Emit == nil {
		t.Fatalf("check watch must wire shared runtime callbacks: %+v", watch)
	}
}

func TestBuildWatchesExpandsMetrics(t *testing.T) {
	for _, tc := range []struct {
		name      string
		watchName string
		checkType string
		watch     map[string]any
		wantCmds  []string
	}{
		{
			name:      "swap",
			watchName: "swap",
			checkType: "swap",
			watch: map[string]any{
				"check": map[string]any{"type": "swap"},
				"metrics": map[string]any{
					"usage": map[string]any{"used_pct": map[string]any{"op": ">=", "value": 80}, "then": map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/swap-usage.sh"}}}},
					"io":    map[string]any{"delta": map[string]any{"op": ">", "value": 1000}, "then": map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/swap-io.sh"}}}},
				},
			},
			wantCmds: []string{"/usr/local/bin/swap-usage.sh", "/usr/local/bin/swap-io.sh"},
		},
		{
			name:      "net",
			watchName: "net-eth0",
			checkType: "net",
			watch: map[string]any{
				"check": map[string]any{"type": "net", "interface": "eth0"},
				"metrics": map[string]any{
					"state":  map[string]any{"on": "change", "then": map[string]any{"hook": map[string]any{"command": []any{"/bin/state.sh"}}}},
					"errors": map[string]any{"delta": map[string]any{"op": ">", "value": 100}, "then": map[string]any{"hook": map[string]any{"command": []any{"/bin/err.sh"}}}},
				},
			},
			wantCmds: []string{"/bin/state.sh", "/bin/err.sh"},
		},
		{
			name:      "icmp",
			watchName: "ping-gw",
			checkType: "icmp",
			watch: map[string]any{
				"check": map[string]any{"type": "icmp", "host": "8.8.8.8", "count": 3},
				"metrics": map[string]any{
					"state":   map[string]any{"on": "change", "then": map[string]any{"hook": map[string]any{"command": []any{"/bin/state.sh"}}}},
					"latency": map[string]any{"threshold": map[string]any{"op": ">", "value": 100}, "then": map[string]any{"hook": map[string]any{"command": []any{"/bin/lat.sh"}}}},
				},
			},
			wantCmds: []string{"/bin/state.sh", "/bin/lat.sh"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := cfgWithWatches(map[string]any{tc.watchName: tc.watch})
			watches, warns := BuildWatches(cfg, Deps{DefaultTimeout: time.Second, ExecxRunner: execx.CommandRunner{}}, 30*time.Second)
			if len(warns) != 0 {
				t.Fatalf("unexpected warnings: %v", warns)
			}
			if len(watches) != 2 {
				t.Fatalf("expected 2 expanded watches, got %d", len(watches))
			}
			cmds := map[string]bool{}
			for _, w := range watches {
				if w.CheckType != tc.checkType || w.Name != tc.watchName || w.Interval != 30*time.Second {
					t.Fatalf("unexpected watch: %+v", w)
				}
				cmds[w.Hook.Command[0]] = true
			}
			for _, cmd := range tc.wantCmds {
				if !cmds[cmd] {
					t.Fatalf("missing per-metric hook %q, got %v", cmd, cmds)
				}
			}
		})
	}
}

// TestBuildWatchesMetricWiresSettling guards against metric watches (net/icmp/
// swap) being built without deps.Settling: without it their RunCycle never marks
// the watch observed, so the settling registry keeps the key pending forever —
// the watch is stuck in state "starting" and the daemon's first-cycle readiness
// gate never clears (/readyz wedged at 503). The single-shot builders already
// wire Settling; the metric builder must too.
func TestBuildWatchesMetricWiresSettling(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"net-eth0": map[string]any{
			"check": map[string]any{"type": "net", "interface": "eth0"},
			"metrics": map[string]any{
				"state": map[string]any{
					"on":   "change",
					"then": map[string]any{"hook": map[string]any{"command": []any{"/bin/state.sh"}}},
				},
			},
		},
	})
	settling := NewSettling(nil)
	watches, warns := BuildWatches(cfg, Deps{DefaultTimeout: time.Second, Settling: settling, Panic: &PanicGate{}}, 30*time.Second)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(watches) != 1 {
		t.Fatalf("expected 1 watch, got %d", len(watches))
	}
	if watches[0].Settling != settling {
		t.Fatalf("metric watch must carry deps.Settling so it can settle out of \"starting\"")
	}
	if watches[0].InPanic == nil {
		t.Fatalf("metric watch must carry deps.Panic.Active so panic mode suppresses it")
	}
}

func TestBuildWatchesMetricHonorsNotifyInterval(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"swap": map[string]any{
			"check": map[string]any{"type": "swap"},
			"metrics": map[string]any{
				"usage": map[string]any{
					"used_pct": map[string]any{"op": ">=", "value": 80},
					"then":     map[string]any{"notify": []any{"ops"}, "notify_interval": "30m"},
				},
			},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{DefaultTimeout: time.Second, GlobalNotify: []string{"ops"}}, 30*time.Second)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(watches) != 1 {
		t.Fatalf("expected 1 watch, got %d", len(watches))
	}
	if watches[0].NotifyInterval != 30*time.Minute {
		t.Fatalf("metric watch must honor notify_interval, got %v", watches[0].NotifyInterval)
	}
}

func TestBuildRaidWatchNotifyOn(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"raid-md0": map[string]any{
			"check": map[string]any{"type": "raid", "array": "md0", "sysfs_changes": true},
			"then":  map[string]any{"notify": []any{"ops"}, "notify_on": []any{"on_degraded", "on_array_change"}},
		},
	})
	n := typedNotifier{name: "ops", typ: "wall"}
	watches, warns := BuildWatches(cfg, Deps{DefaultTimeout: time.Second, Notifiers: map[string]notify.Notifier{"ops": n}}, time.Minute)
	if len(warns) != 0 || len(watches) != 1 {
		t.Fatalf("raid notify_on build = watches=%v warnings=%v", watches, warns)
	}
	if !watches[0].RaidNotifyEvents["on_degraded"] || !watches[0].RaidNotifyEvents["on_array_change"] {
		t.Fatalf("raid events = %+v", watches[0].RaidNotifyEvents)
	}
	if len(watches[0].Notifiers) != 1 {
		t.Fatalf("notifiers = %v, want ops", watches[0].Notifiers)
	}
}

func TestBuildWatchesServiceCheckAsWatch(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"health": map[string]any{
			"check": map[string]any{"type": "tcp", "port": 5432, "host": "127.0.0.1"},
			"then":  map[string]any{"hook": map[string]any{"command": []any{"/x.sh"}}},
		},
		"backlog": map[string]any{
			"check": map[string]any{"type": "count", "path": "/tmp", "op": ">", "value": 100},
			"then":  map[string]any{"hook": map[string]any{"command": []any{"/y.sh"}}},
		},
		"ws": map[string]any{
			"check": map[string]any{"type": "websocket", "url": "ws://127.0.0.1/ws"},
			"then":  map[string]any{"hook": map[string]any{"command": []any{"/z.sh"}}},
		},
		"automount": map[string]any{
			"check": map[string]any{"type": "autofs"},
			"then":  map[string]any{"hook": map[string]any{"command": []any{"/a.sh"}}},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{DefaultTimeout: time.Second, ExecxRunner: execx.CommandRunner{}}, 30*time.Second)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	byName := map[string]*Watch{}
	for _, w := range watches {
		byName[w.Name] = w
	}
	if h := byName["health"]; h == nil || !h.FireOnFail {
		t.Fatalf("a tcp (health) watch should fire on failure: %+v", h)
	}
	for _, name := range []string{"ws", "automount"} {
		if h := byName[name]; h == nil || !h.FireOnFail {
			t.Fatalf("%s health watch should fire on failure: %+v", name, h)
		}
	}
	if b := byName["backlog"]; b == nil || b.FireOnFail {
		t.Fatalf("a count (condition) watch should fire on OK, not failure: %+v", b)
	}
}

func TestWatchFireOnFailInvertsTrigger(t *testing.T) {
	var fired int32
	runner := HookRunnerFunc(func(context.Context, []string, map[string]string, time.Duration) error {
		atomic.AddInt32(&fired, 1)
		return nil
	})
	// A health check that is failing (OK=false) must fire when FireOnFail is set.
	w := &Watch{
		Name:       "health",
		Check:      stubCheck{name: "tcp", ok: false},
		FireOnFail: true,
		Runner:     runner,
		Hook:       HookSpec{Command: []string{"/bin/true"}},
	}
	w.RunCycle(context.Background())
	if atomic.LoadInt32(&fired) != 1 {
		t.Fatalf("FireOnFail watch should fire on a failing check, fired=%d", fired)
	}
	// A passing health check must NOT fire.
	fired = 0
	w.Check = stubCheck{name: "tcp", ok: true}
	w.state = rules.WindowState{}
	w.RunCycle(context.Background())
	if atomic.LoadInt32(&fired) != 0 {
		t.Fatalf("FireOnFail watch must not fire on a passing check, fired=%d", fired)
	}
}

func TestBuildWatchesWarnsOnBadCheck(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"bad": map[string]any{
			"check": map[string]any{"type": "storage"}, // missing path/predicate
			"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{}, time.Second)
	if len(watches) != 0 || len(warns) == 0 {
		t.Fatalf("expected 0 watches and a warning, got %d / %v", len(watches), warns)
	}
}

func TestBuildWatchesNetWarnsOnBadMetric(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"net-eth0": map[string]any{
			"check": map[string]any{"type": "net", "interface": "eth0"},
			"metrics": map[string]any{
				"state": map[string]any{ // missing on/expect -> check build error
					"then": map[string]any{"hook": map[string]any{"command": []any{"/bin/x.sh"}}},
				},
			},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{}, time.Second)
	if len(watches) != 0 || len(warns) == 0 {
		t.Fatalf("expected 0 watches and a warning, got %d / %v", len(watches), warns)
	}
}

func TestHasConfiguredTargets(t *testing.T) {
	// No services and no watches: nothing configured.
	empty := &config.Config{Global: config.Global{Raw: map[string]any{}}}
	if HasConfiguredTargets(empty) {
		t.Fatalf("empty config should report no configured targets")
	}

	// A watch present but disabled still counts as configured, so the daemon
	// starts (and can enable it later via reload/web UI) instead of erroring.
	disabledWatch := cfgWithWatches(map[string]any{
		"storage-root": map[string]any{
			"enabled": false,
			"check":   map[string]any{"type": "storage", "path": "/"},
		},
	})
	watches, _ := BuildWatches(disabledWatch, Deps{DefaultTimeout: time.Second, ExecxRunner: execx.CommandRunner{}}, time.Second)
	if len(watches) != 0 {
		t.Fatalf("disabled watch should not build, got %d", len(watches))
	}
	if !HasConfiguredTargets(disabledWatch) {
		t.Fatalf("disabled watch should still count as a configured target")
	}

	// A disabled service also counts as configured.
	disabledSvc := &config.Config{
		Global:   config.Global{Raw: map[string]any{}},
		Services: map[string]*config.Document{"web": {Body: map[string]any{"enabled": false}}},
	}
	if !HasConfiguredTargets(disabledSvc) {
		t.Fatalf("disabled service should still count as a configured target")
	}
}
