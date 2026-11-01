package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/notify"
	"sermo/internal/process"
)

// TestReloadPreservesWatchLastNotifyAt pins that a reload keeps a firing watch's
// lastNotifyAt. Before the fix the snapshot restored firing but reset
// lastNotifyAt to the zero time, so a watch with then.notify_interval re-sent its
// reminder on the first cycle after every reload (now - zero >= interval).
func TestReloadPreservesWatchLastNotifyAt(t *testing.T) {
	when := time.Now().Add(-30 * time.Minute)
	old := &Watch{Name: "storage", firing: true, lastNotifyAt: when}

	saved := captureWatchState([]*Watch{old})

	fresh := &Watch{Name: "storage"}
	applyWatchState([]*Watch{fresh}, saved)

	if !fresh.firing {
		t.Fatal("firing should be preserved across reload")
	}
	if !fresh.lastNotifyAt.Equal(when) {
		t.Fatalf("lastNotifyAt = %v, want %v (preserved across reload)", fresh.lastNotifyAt, when)
	}
}

// A reload rebuilds every stateful process watcher. Losing what each had already
// reported ran its hook, notification and kill again for the same PID on every
// SIGHUP, against the once-per-incarnation these watches promise.
func TestReloadKeepsThePerPIDEdgesOfProcessWatches(t *testing.T) {
	long := map[string]any{"for": "1m"}
	longer := map[string]any{"for": "2m"}
	for _, tc := range []struct {
		name  string
		check func(extra map[string]any) map[string]any
	}{
		{"process", func(extra map[string]any) map[string]any {
			return map[string]any{"type": checks.CheckTypeProcess, "name": "worker", "for": extra["for"]}
		}},
		{"process_policy", func(extra map[string]any) map[string]any {
			return map[string]any{"type": checks.CheckTypeProcessPolicy, "user": "70", "allow": map[string]any{
				"backend": map[string]any{checks.CheckKeyExe: "/opt/sermo-test/" + extra["for"].(string)},
			}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := &fakeNotifier{name: "ops"}
			offender := ProcInfo{
				PID: 7, StartTicks: 11, StartTicksOK: true, UID: 70, Exe: "/opt/sermo-test/worker", ExeOK: true,
				StartTime: time.Now().Add(-time.Hour),
			}
			deps := Deps{
				ProcSampler: fixedSampler{infos: []ProcInfo{offender}, ok: true},
				Notifiers:   map[string]notify.Notifier{"ops": n},
				UserLookup:  process.NewUserLookup(process.UserLookupConfig{Mode: process.UserLookupNumeric}),
			}
			build := func(check map[string]any) *Watch {
				t.Helper()
				cfg := cfgWithWatches(map[string]any{"edge": map[string]any{
					"check": check, "then": map[string]any{"notify": []any{"ops"}},
				}})
				watches, warnings := BuildWatches(cfg, deps, time.Minute)
				if len(watches) != 1 || len(warnings) != 0 {
					t.Fatalf("BuildWatches() = %d watches, warnings %v", len(watches), warnings)
				}
				return watches[0]
			}
			reload := func(old, fresh *Watch) *Watch {
				saved := captureWatchState([]*Watch{old})
				applyWatchState([]*Watch{fresh}, saved)
				return fresh
			}

			first := build(tc.check(long))
			first.RunCycle(context.Background())
			if len(n.msgs) != 1 {
				t.Fatalf("first cycle notified %d times, want 1", len(n.msgs))
			}

			same := reload(first, build(tc.check(long)))
			same.RunCycle(context.Background())
			if len(n.msgs) != 1 {
				t.Fatalf("after a reload the same PID notified %d times, want still 1", len(n.msgs))
			}

			// What the old selector established says nothing about the new one.
			changed := reload(same, build(tc.check(longer)))
			changed.RunCycle(context.Background())
			if len(n.msgs) != 2 {
				t.Fatalf("after a changed check notified %d times, want a fresh report", len(n.msgs))
			}
		})
	}
}

func TestReloadKeepsTheIncidentsOfAnUnownedWatch(t *testing.T) {
	h := &unownedHarness{}
	sampler := fixedSampler{infos: []ProcInfo{unownedProc(42, "/opt/sermo-test/sleep", "0::/")}, ok: true}
	n := &fakeNotifier{name: "ops"}
	build := func() *unownedWatcher {
		w := h.watcher(t, sampler, map[string]any{}, systemdFixture().runtime())
		w.notifiers = []notify.Notifier{n}
		return w
	}
	first, second := build(), build()
	first.runCycle(context.Background())

	const configID = "same-check"
	saved := captureWatchState([]*Watch{{Name: "unowned", edges: first, configID: configID}})
	applyWatchState([]*Watch{{Name: "unowned", edges: second, configID: configID}}, saved)
	second.runCycle(context.Background())

	if len(n.msgs) != 1 {
		t.Fatalf("the same process notified %d times across a reload, want 1", len(n.msgs))
	}
	if kinds := h.kinds(); len(kinds) != 1 || kinds[0] != eventKindFiring {
		t.Fatalf("events = %v, want one firing", kinds)
	}

	entry := map[string]any{"check": map[string]any{"type": checks.CheckTypeUnownedProcesses}, "then": map[string]any{"notify": []any{"none"}}}
	watches, warnings := BuildWatches(cfgWithWatches(map[string]any{"unowned": entry}), Deps{PackageIndex: &fakePackages{}}, time.Minute)
	if len(watches) != 1 || len(warnings) != 0 || watches[0].edges == nil {
		t.Fatalf("BuildWatches() = %+v, warnings %v, want a watch that carries its edges", watches, warnings)
	}
}
