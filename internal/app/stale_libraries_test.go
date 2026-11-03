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

// A mail server detaches short-lived deliveries that run its own executable as
// its own user, and a backend seed labels them main: only the pidfile tells the
// daemon apart for as long as they live.
func TestLibraryRestartPrincipalFollowsTheMainPidfile(t *testing.T) {
	const exe = "/opt/sermo-fixture/exim"
	ids := pidfileClaimReader{
		10: {PID: 10, UID: 8, Exe: exe, ExeOK: true},
		20: {PID: 20, UID: 8, Exe: exe, ExeOK: true},
		30: {PID: 30, UID: 8, Exe: "/opt/sermo-fixture/other", ExeOK: true},
		40: {PID: 40, UID: 0, Exe: exe, ExeOK: true},
	}
	sels := []process.Selector{{Name: process.RoleMain, Type: process.SelectorCommandMatch, Exe: exe, User: "mail"}}
	d := process.Discoverer{Reader: ids, ResolveUser: func(string) (uint32, bool) { return 8, true }}
	seed := func(pid int) process.Process {
		id := ids[pid]
		return process.Process{PID: pid, UID: id.UID, Exe: id.Exe, ExeOK: true, Role: process.RoleMain, Source: process.SourceBackend}
	}
	named := func(pid int) process.Process {
		proc := seed(pid)
		proc.MainPidfile = true
		return proc
	}
	unseeded := named(10)
	unseeded.Role, unseeded.Source = process.SelectorPidfile, process.SelectorPidfile
	external := named(10)
	external.External = true

	for name, tc := range map[string]struct {
		procs []process.Process
		want  int
	}{
		"daemon beside a detached delivery":    {procs: []process.Process{named(10), seed(20)}, want: 10},
		"daemon without backend seeds":         {procs: []process.Process{unseeded, seed(20)}, want: 10},
		"no pidfile stays ambiguous":           {procs: []process.Process{seed(10), seed(20)}},
		"pidfile recycled by another exe":      {procs: []process.Process{named(30), seed(10), seed(20)}},
		"pidfile recycled by another user":     {procs: []process.Process{named(40), seed(10), seed(20)}},
		"pidfile naming an external candidate": {procs: []process.Process{external, seed(20)}, want: 20},
	} {
		t.Run(name, func(t *testing.T) {
			if got := libraryRestartPrincipal(d, sels, tc.procs); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}

func TestLibraryRestartPrincipalWithNamedCatalogSelector(t *testing.T) {
	const exe = "/opt/sermo-fixture/postgres"
	ids := pidfileClaimReader{
		10: {PID: 10, UID: 70, Exe: exe, ExeOK: true},
		20: {PID: 20, UID: 70, Exe: exe, ExeOK: true},
	}
	d := process.Discoverer{Reader: ids, ResolveUser: func(string) (uint32, bool) { return 70, true }}
	selector := process.Selector{Name: "postgres", Type: process.SelectorCommandMatch, Exe: exe, User: "postgres"}
	main := process.Process{PID: 10, UID: 70, Exe: exe, ExeOK: true, Role: process.RoleMain, Source: process.SourceBackend}
	worker := main
	worker.PID, worker.Role = 20, "postgres"
	pidfile := main
	pidfile.Role, pidfile.Source, pidfile.MainPidfile = process.SelectorPidfile, process.SelectorPidfile, true
	otherMain := worker
	otherMain.Role = process.RoleMain
	explicitMain := selector
	explicitMain.Name, explicitMain.Exe = process.RoleMain, "/opt/sermo-fixture/master"
	delegated := selector
	delegated.Delegated = true

	for name, tc := range map[string]struct {
		selectors []process.Selector
		procs     []process.Process
		want      int
	}{
		"unique backend main":  {selectors: []process.Selector{selector}, procs: []process.Process{main}, want: 10},
		"pidfile with workers": {selectors: []process.Selector{selector}, procs: []process.Process{pidfile, worker}, want: 10},
		"worker alone":         {selectors: []process.Selector{selector}, procs: []process.Process{worker}},
		"ambiguous masters":    {selectors: []process.Selector{selector}, procs: []process.Process{main, otherMain}},
		"explicit main absent": {selectors: []process.Selector{selector, explicitMain}, procs: []process.Process{main}},
		"delegated identity":   {selectors: []process.Selector{delegated}, procs: []process.Process{main}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := libraryRestartPrincipal(d, tc.selectors, tc.procs); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
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
