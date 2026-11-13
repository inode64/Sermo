package app

import (
	"context"
	"regexp"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/process"
)

func TestProcessStateWatchMissingAtStartupAndRecovery(t *testing.T) {
	procs := serviceOwnershipReader{}
	var events []Event
	entry := map[string]any{
		"check": map[string]any{"type": "process", "exe": "/opt/app/server", "user": "0", "state": "running"},
		"for":   map[string]any{"cycles": 2}, "clear": map[string]any{"cycles": 2},
	}
	watches, warnings := BuildWatches(cfgWithWatches(map[string]any{"worker": entry}), Deps{
		ProcReader: procs, DefaultTimeout: time.Second, Emit: func(event Event) { events = append(events, event) },
	}, time.Second)
	if len(warnings) != 0 || len(watches) != 1 {
		t.Fatalf("watches=%v, warnings=%v", watches, warnings)
	}
	w := watches[0]
	if w.Check == nil || !w.FireOnFail {
		t.Fatal("process state must be a health check, not a per-PID edge watcher")
	}
	for range 4 {
		w.RunCycle(t.Context())
	}
	procs[42] = process.Identity{PID: 42, PPID: 1, StartTicks: 42, StartTicksOK: true, Exe: "/opt/app/server", ExeOK: true, UID: 0, State: "S"}
	for range 3 {
		w.RunCycle(t.Context())
	}
	var kinds []string
	for _, event := range events {
		if eventNeedsNotification(event) {
			kinds = append(kinds, event.Kind)
		}
	}
	if len(kinds) != 2 || kinds[0] != eventKindFiring || kinds[1] != eventKindRecovered {
		t.Fatalf("incidents=%v, want one absence and one recovery", kinds)
	}
}

func TestProcessStateWatchReusesServiceObserver(t *testing.T) {
	for _, state := range []string{process.StateRunning, process.StateAbsent, process.StateUnknown} {
		t.Run(state, func(t *testing.T) {
			check := map[string]any{"type": "process", "cmd": "worker", "state": "running"}
			entry := map[string]any{"check": check, "then": map[string]any{"notify": []any{"none"}}}
			if why := unsupportedServiceWatchType(entry); why != "" {
				t.Fatal(why)
			}
			deps := Deps{WatchCheckDeps: &checks.Deps{ProcessesAny: func(context.Context, []string, string, *regexp.Regexp) string { return state }}}
			watches, warnings := buildWatchEntry("svc:worker", entry, deps, time.Second)
			if len(warnings) != 0 || len(watches) != 1 {
				t.Fatalf("watches=%v, warnings=%v", watches, warnings)
			}
			result := watches[0].Check.Run(t.Context())
			if result.Unavailable != (state == process.StateUnknown) || result.OK != (state == process.StateRunning) {
				t.Fatalf("state=%s result=%+v", state, result)
			}
			entry["then"] = map[string]any{"kill": map[string]any{"signal": "TERM"}}
			if ws, warns := buildWatchEntry("svc:worker", entry, deps, time.Second); len(ws) != 0 || len(warns) == 0 {
				t.Fatalf("state check acquired kill authority: watches=%v, warnings=%v", ws, warns)
			}
		})
	}
}

func TestProcessStateWatchObservesReplacedExecutablesWithoutSignalAuthority(t *testing.T) {
	procs := serviceOwnershipReader{42: {
		PID: 42, UID: 0, ExePrev: "/opt/app/server", State: "S", Cmdline: []string{"/opt/app/server", "--worker"},
	}}
	for _, commandOnly := range []bool{false, true} {
		check := map[string]any{"type": "process", "exe": "/opt/app/server", "user": "0", "state": "running"}
		if commandOnly {
			delete(check, "exe")
			check["cmd"] = "^/opt/app/server( |$)"
		}
		watches, warnings := BuildWatches(cfgWithWatches(map[string]any{"worker": map[string]any{"check": check}}), Deps{ProcReader: procs}, time.Second)
		if len(warnings) != 0 || len(watches) != 1 {
			t.Fatalf("watches=%v, warnings=%v", watches, warnings)
		}
		result := watches[0].Check.Run(t.Context())
		if commandOnly {
			if !result.OK {
				t.Fatalf("argv observation lost a live replaced process: %+v", result)
			}
		} else if result.Reports != checks.ReportsState || result.Data[checks.DataKeyReplacedBinaries] == nil {
			t.Fatalf("a replaced executable was reported as a vanished daemon: %+v", result)
		}
	}
}
