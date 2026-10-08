package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/web"
)

func unownedKillBackend(t *testing.T, sampler ProcSampler, dryRun bool) (*WebBackend, *fakeSignaler, *[]Event) {
	t.Helper()
	events := []Event{}
	signaler := &fakeSignaler{}
	rt := systemdFixture().runtime()
	rt.sampler = sampler
	b := &WebBackend{
		cfg: &config.Config{Global: config.Global{Runtime: t.TempDir()}},
		watches: map[string]*webWatch{
			"unowned":  {name: "unowned", checkType: checks.CheckTypeUnownedProcesses, check: map[string]any{checks.CheckKeyType: checks.CheckTypeUnownedProcesses}, dryRun: dryRun, interval: time.Minute},
			"disabled": {name: "disabled", checkType: checks.CheckTypeUnownedProcesses, check: map[string]any{}, disabled: true},
			"load":     {name: "load", checkType: checks.CheckTypeLoad, check: map[string]any{}},
		},
		emit:          func(e Event) { events = append(events, e) },
		now:           func() time.Time { return testUnownedClock },
		unowned:       rt,
		unownedKiller: pidKiller{signaler: signaler, resolve: rt.resolve, sleep: func(time.Duration) {}, emit: func(e Event) { events = append(events, e) }},
	}
	return b, signaler, &events
}

func TestManualKillSelectorBindsNumericUID(t *testing.T) {
	info := unownedProc(42, "/opt/acme/bin/php", "0::/")
	info.UID, info.User = 33, "www-data"
	selector := manualKillSelector(info)
	if !slices.Equal(selector.Users, []string{"33"}) || !slices.Equal(selector.ExeAny, []string{"/opt/acme/bin/php"}) {
		t.Fatalf("selector = %+v, want the numeric uid and the exact exe", selector)
	}
	// The gate must pass on a resolver that knows no names at all.
	noNames := func(string) (uint32, bool) { return 0, false }
	if selector.Killable(info.asProcess(), noNames) {
		t.Fatal("a resolver that cannot parse numbers must not pass; the real ones do")
	}
	numeric := func(name string) (uint32, bool) {
		if name == "33" {
			return 33, true
		}
		return 0, false
	}
	if !selector.Killable(info.asProcess(), numeric) {
		t.Fatal("the verified identity must pass its own selector through a numeric-only resolver")
	}
}

func TestKillWatchProcessRefusals(t *testing.T) {
	stray := unownedProc(42, "/usr/bin/sleep", "0::/")
	b, signaler, events := unownedKillBackend(t, fixedSampler{infos: []ProcInfo{stray}, ok: true}, false)
	for _, tc := range []struct {
		name  string
		watch string
		req   web.WatchProcessKillRequest
		want  string
	}{
		{"unknown watch", "nope", web.WatchProcessKillRequest{PID: 42, StartTicks: 420}, "unknown watch"},
		{"disabled watch", "disabled", web.WatchProcessKillRequest{PID: 42, StartTicks: 420}, "not an enabled unowned_processes watch"},
		{"other type", "load", web.WatchProcessKillRequest{PID: 42, StartTicks: 420}, "not an enabled unowned_processes watch"},
		{"missing ticks", "unowned", web.WatchProcessKillRequest{PID: 42}, "start_ticks"},
		{"not listed", "unowned", web.WatchProcessKillRequest{PID: 77, StartTicks: 1}, "no longer listed"},
		{"recycled pid", "unowned", web.WatchProcessKillRequest{PID: 42, StartTicks: 1}, "now another process"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(*events)
			res := b.KillWatchProcess(context.Background(), tc.watch, tc.req)
			if res.OK || !strings.Contains(res.Message, tc.want) {
				t.Fatalf("result = %+v, want refusal %q", res, tc.want)
			}
			if len(*events) != before+1 {
				t.Fatalf("refusal events = %+v, want one event", (*events)[before:])
			}
		})
	}
	if len(signaler.sent) != 0 {
		t.Fatalf("a refusal signalled: %v", signaler.sent)
	}
	for _, e := range *events {
		if e.Kind != eventKindError || e.Watch == "" {
			t.Fatalf("refusal event = %+v", e)
		}
	}

	replaced := unownedProc(43, "", "0::/")
	replaced.ExeOK, replaced.ExePrev = false, "/usr/bin/sleep"
	b2, signaler2, _ := unownedKillBackend(t, fixedSampler{infos: []ProcInfo{replaced}, ok: true}, false)
	res := b2.KillWatchProcess(context.Background(), "unowned", web.WatchProcessKillRequest{PID: 43, StartTicks: 430})
	if res.OK || !strings.Contains(res.Message, unownedKillReasonReplacedExe) || len(signaler2.sent) != 0 {
		t.Fatalf("replaced executable: result = %+v signals = %v", res, signaler2.sent)
	}
}

func TestKillWatchProcessSendsSIGTERMToVerifiedIdentity(t *testing.T) {
	stray := unownedProc(42, "/usr/bin/sleep", "0::/")
	b, signaler, events := unownedKillBackend(t, fixedSampler{infos: []ProcInfo{stray}, ok: true}, false)

	res := b.KillWatchProcess(context.Background(), "unowned", web.WatchProcessKillRequest{PID: 42, StartTicks: 420})

	if !res.OK || len(signaler.sent) != 1 || signaler.sent[0] != (sigCall{42, syscall.SIGTERM}) {
		t.Fatalf("result = %+v signals = %v", res, signaler.sent)
	}
	if len(*events) != 1 || (*events)[0].Kind != eventKindKill || (*events)[0].Watch != "unowned" {
		t.Fatalf("events = %+v", *events)
	}
}

func TestKillWatchProcessEscalatesOnRequest(t *testing.T) {
	stray := unownedProc(42, "/usr/bin/sleep", "0::/")
	// Verification sample, post-TERM re-sample (still there), post-KILL (gone).
	b, signaler, _ := unownedKillBackend(t, &fakeProcSampler{cycles: [][]ProcInfo{{stray}, {stray}, {}}}, false)
	res := b.KillWatchProcess(context.Background(), "unowned", web.WatchProcessKillRequest{PID: 42, StartTicks: 420, Escalate: true})
	if !res.OK || len(signaler.sent) != 2 || signaler.sent[1] != (sigCall{42, syscall.SIGKILL}) {
		t.Fatalf("result = %+v signals = %v", res, signaler.sent)
	}
}

func TestKillWatchProcessReportsFailureOnce(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		escalate bool
		message  string
		kinds    []string
	}{
		{name: "signal failed", err: errors.New("signal denied"), message: "signal denied", kinds: []string{eventKindKillFailed}},
		{name: "survived SIGKILL", escalate: true, message: "survived SIGKILL", kinds: []string{eventKindKill, eventKindKill, eventKindKillFailed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stray := unownedProc(42, "/usr/bin/sleep", "0::/")
			b, signaler, events := unownedKillBackend(t, fixedSampler{infos: []ProcInfo{stray}, ok: true}, false)
			signaler.err = tc.err
			res := b.KillWatchProcess(t.Context(), "unowned", web.WatchProcessKillRequest{PID: 42, StartTicks: 420, Escalate: tc.escalate})
			if res.OK || !strings.Contains(res.Message, tc.message) {
				t.Fatalf("result = %+v, want failure %q", res, tc.message)
			}
			var kinds []string
			for _, event := range *events {
				kinds = append(kinds, event.Kind)
			}
			if !slices.Equal(kinds, tc.kinds) {
				t.Fatalf("events = %+v, want kinds %v", *events, tc.kinds)
			}
		})
	}
}

func TestKillWatchProcessAuditsLockFailure(t *testing.T) {
	stray := unownedProc(42, "/usr/bin/sleep", "0::/")
	b, signaler, events := unownedKillBackend(t, fixedSampler{infos: []ProcInfo{stray}, ok: true}, false)
	// A file at the runtime path prevents creating the operation lock.
	runtime := filepath.Join(t.TempDir(), "runtime")
	if err := os.WriteFile(runtime, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	b.cfg.Global.Runtime = runtime
	res := b.KillWatchProcess(t.Context(), "unowned", web.WatchProcessKillRequest{PID: 42, StartTicks: 420})
	if res.OK || !strings.Contains(res.Message, "lock watch unowned") || len(signaler.sent) != 0 {
		t.Fatalf("result = %+v signals = %v", res, signaler.sent)
	}
	if len(*events) != 1 || (*events)[0].Kind != eventKindError || (*events)[0].Message != res.Message {
		t.Fatalf("events = %+v, want the lock refusal once", *events)
	}
}

func TestKillWatchProcessDryRunSignalsNothing(t *testing.T) {
	stray := unownedProc(42, "/usr/bin/sleep", "0::/")
	b, signaler, events := unownedKillBackend(t, fixedSampler{infos: []ProcInfo{stray}, ok: true}, true)
	res := b.KillWatchProcess(context.Background(), "unowned", web.WatchProcessKillRequest{PID: 42, StartTicks: 420})
	if !res.OK || !strings.Contains(res.Message, "would send SIGTERM to pid 42") || len(signaler.sent) != 0 {
		t.Fatalf("result = %+v signals = %v", res, signaler.sent)
	}
	if len(*events) != 1 || (*events)[0].Kind != eventKindDryRun {
		t.Fatalf("events = %+v", *events)
	}
}

func TestWatchViewListsUnownedProcessRows(t *testing.T) {
	snapshots := NewWatchSnapshots()
	snapshots.now = func() time.Time { return testUnownedClock }
	rows := []checks.UnownedProcess{{PID: 42, StartTicks: 420, User: "root", Exe: "/usr/bin/sleep", ExeResolved: true, RSS: 4096, CPU: 1.5, HasCPU: true, Reason: unownedReasonNoUnit, CanKill: true}}
	w := &webWatch{name: "unowned", checkType: checks.CheckTypeUnownedProcesses, check: map[string]any{}, interval: time.Minute, configID: "cfg"}
	snapshots.publishConfigured("unowned", checks.CheckTypeUnownedProcesses, checks.Result{Check: "unowned", Data: map[string]any{checks.DataKeyProcesses: rows}}, "cfg")
	b := &WebBackend{watchSnapshots: snapshots, now: func() time.Time { return testUnownedClock }, watches: map[string]*webWatch{"unowned": w}, watchOrder: []string{"unowned"}, cfg: &config.Config{}}

	view := b.watchView(w, b.watchSystemSnapshot(), watchActivity{})
	if len(view.Processes) != 1 || view.Processes[0].PID != 42 || view.Processes[0].StartTicks != 420 || !view.Processes[0].CanKill || view.Processes[0].RSS != 4096 || !view.Processes[0].HasCPU {
		t.Fatalf("view.Processes = %+v", view.Processes)
	}
	// Another config generation's rows are never shown.
	w.configID = "other"
	if view := b.watchView(w, b.watchSystemSnapshot(), watchActivity{}); len(view.Processes) != 0 {
		t.Fatalf("stale generation rows shown: %+v", view.Processes)
	}
}
