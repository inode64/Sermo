package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/process"
)

func TestLibraryRestartPrincipal(t *testing.T) {
	const exe = "/opt/sermo-fixture/sshd"
	for _, name := range []string{"main", "child", "delegated", "stray", "external", "wrong UID", "wrong exe", "no strict selector", "multiple masters"} {
		t.Run(name, func(t *testing.T) {
			p := process.Process{PID: 42, UID: 0, Exe: exe, ExeOK: true, Role: process.RoleMain, Source: process.SelectorCommandMatch}
			ids := pidfileClaimReader{42: {PID: 42, UID: 0, Exe: exe, ExeOK: true}, 43: {PID: 43, UID: 0, Exe: exe, ExeOK: true}}
			sels := []process.Selector{{Name: process.RoleMain, Type: process.SelectorCommandMatch, Exe: exe, User: "0"}}
			switch name {
			case "child":
				p.Source = process.SourceChild
			case "delegated":
				p.Delegated = true
			case "stray":
				p.Stray = true
			case "external":
				p.External = true
			case "wrong UID":
				id := ids[42]
				id.UID = 1000
				ids[42] = id
			case "wrong exe":
				id := ids[42]
				id.Exe = "/opt/other/sshd"
				ids[42] = id
			case "no strict selector":
				sels[0].User = ""
			}
			procs := []process.Process{p}
			if name == "multiple masters" {
				other := p
				other.PID = 43
				procs = append(procs, other)
			}
			d := process.Discoverer{Reader: ids, ResolveUser: func(string) (uint32, bool) { return 0, true }}
			want := 0
			if name == "main" {
				want = 42
			}
			if got := libraryRestartPrincipal(d, sels, procs); got != want {
				t.Fatalf("got %d want %d", got, want)
			}
		})
	}
}

func TestStaleLibrariesRestartState(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		b, e := staleBinaryBackend(t, true)
		e.checkNames = []string{"libraries"}
		e.checkTypes = map[string]string{"libraries": checks.CheckTypeStaleLibraries}
		e.checkReports = map[string]string{"libraries": checks.ReportsState}
		e.checkIntervals = map[string]time.Duration{"libraries": time.Minute}
		b.snapshots.publishWithCheckTypes("web", map[string]checks.Result{"libraries": {Check: "libraries", Reports: checks.ReportsState, Data: map[string]any{checks.DataKeyActionBlocked: blocked}}}, map[string]bool{"libraries": true}, e.checkTypes)
		svc := b.view(t.Context(), "web", e)
		if !blocked && (svc.State != TargetStateRestartRequired || svc.StateReason != stateReasonStaleLibraries) {
			t.Fatalf("restart evidence lost: %+v", svc)
		}
		if blocked && svc.State == TargetStateRestartRequired {
			t.Fatal("advisory sessions requested restart")
		}
	}
}

func TestStaleLibrariesWatchPolarity(t *testing.T) {
	w := newCheckWatch(checkWatchSpec{name: "libraries", checkType: checks.CheckTypeStaleLibraries, interval: time.Minute}, Deps{})
	if !w.FireOnFail {
		t.Fatal("healthy inventory would fire the watch")
	}
}

func TestAdvisoryWatchWithholdsFiringAndRecoveryHooks(t *testing.T) {
	hooks := 0
	w := &Watch{Name: "libraries", CheckType: checks.CheckTypeStaleLibraries, FireOnFail: true,
		Check: &scriptedCheck{results: []checks.Result{{Check: "libraries", OK: false, ActionBlocked: true}, {Check: "libraries", OK: true, ActionBlocked: true}}},
		Hook:  HookSpec{Command: []string{"fixture"}}, RecoverHook: HookSpec{Command: []string{"fixture"}},
		Runner: HookRunnerFunc(func(context.Context, []string, map[string]string, time.Duration) error { hooks++; return nil }),
	}
	w.RunCycle(t.Context())
	w.RunCycle(t.Context())
	if hooks != 0 {
		t.Fatalf("advisory inventory executed %d hooks", hooks)
	}
}

func TestLibraryRestartEligibleNeedsTheMainProcessOnSermosRoot(t *testing.T) {
	for _, tc := range []struct {
		name string
		lib  process.StaleLibrary
		want bool
	}{
		{"main process", process.StaleLibrary{PID: 10}, true},
		{"worker", process.StaleLibrary{PID: 11}, false},
		{"chrooted main process", process.StaleLibrary{PID: 10, ChrootedProcess: true}, false},
	} {
		if got := libraryRestartEligible(tc.lib, 10); got != tc.want {
			t.Errorf("%s: eligible = %v, want %v", tc.name, got, tc.want)
		}
	}
	if libraryRestartEligible(process.StaleLibrary{}, 0) {
		t.Error("no unique main process must never be eligible")
	}
}
