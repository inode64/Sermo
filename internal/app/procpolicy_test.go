package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/process"
)

func testProcessPolicyWatcher(t *testing.T, sampler ProcSampler, allow map[string]any) (*processPolicyWatcher, *[]Event, *checks.Result) {
	t.Helper()
	allows, err := parseProcessPolicyAllows("postgres", map[string]any{checks.CheckKeyAllow: allow})
	if err != nil {
		t.Fatalf("parseProcessPolicyAllows() error = %v", err)
	}
	events := []Event{}
	var snapshot checks.Result
	watcher := &processPolicyWatcher{
		name:    "postgres-execution-policy",
		user:    "postgres",
		allows:  allows,
		sampler: sampler,
		resolve: func(user string) (uint32, bool) { return 70, user == "postgres" },
		emit:    func(event Event) { events = append(events, event) },
		publish: func(watch, checkType string, result checks.Result) {
			if watch != "postgres-execution-policy" || checkType != checks.CheckTypeProcessPolicy {
				t.Fatalf("published %s/%s, want process policy snapshot", watch, checkType)
			}
			snapshot = result
		},
	}
	return watcher, &events, &snapshot
}

func TestProcessPolicyWatcherAlertsOncePerPIDIncarnation(t *testing.T) {
	const secretArgument = "--password=never-publish-this"
	invalid := ProcInfo{
		PID: 42, UID: 70, Exe: "/usr/bin/bash", ExeOK: true, StartTicks: 100,
		Cmdline: []string{"bash", "-c", secretArgument},
	}
	watcher, events, snapshot := testProcessPolicyWatcher(t,
		&fakeProcSampler{cycles: [][]ProcInfo{{invalid}, {invalid}, {{PID: 42, UID: 70, Exe: "/usr/bin/bash", ExeOK: true, StartTicks: 200, Cmdline: invalid.Cmdline}}}},
		map[string]any{"postgres": map[string]any{checks.CheckKeyExe: "/usr/lib64/postgresql-18/bin/postgres"}},
	)

	watcher.runCycle(context.Background())
	watcher.runCycle(context.Background())
	watcher.runCycle(context.Background())

	if len(*events) != 2 {
		t.Fatalf("events = %d, want one alert for each PID incarnation", len(*events))
	}
	if (*events)[0].Kind != eventKindFiring || strings.Contains((*events)[0].Message, secretArgument) {
		t.Fatalf("unsafe policy event = %+v", (*events)[0])
	}
	if snapshot.OK || snapshot.Condition || snapshot.Data[checks.DataKeyViolationCount] != 1 {
		t.Fatalf("policy snapshot = %+v, want one current violation", *snapshot)
	}
	if got := snapshot.Data[checks.DataKeyViolations].(string); strings.Contains(got, secretArgument) {
		t.Fatalf("policy readings leaked command argument: %q", got)
	}
}

func TestProcessPolicyWatcherRealertsWhenPIDIncarnationIsUnknown(t *testing.T) {
	invalid := ProcInfo{PID: 42, UID: 70, Exe: "/usr/bin/bash", ExeOK: true}
	watcher, events, _ := testProcessPolicyWatcher(t,
		&fakeProcSampler{cycles: [][]ProcInfo{{invalid}, {invalid}}},
		map[string]any{"postgres": map[string]any{checks.CheckKeyExe: "/usr/lib64/postgresql-18/bin/postgres"}},
	)

	watcher.runCycle(context.Background())
	watcher.runCycle(context.Background())

	if got := len(*events); got != 2 {
		t.Fatalf("events = %d, want an alert for every unidentifiable PID sample", got)
	}
}

func TestProcessPolicyWatcherPacesUnknownPIDNotifications(t *testing.T) {
	invalid := ProcInfo{PID: 42, UID: 70, Exe: "/usr/bin/bash", ExeOK: true}
	watcher, _, _ := testProcessPolicyWatcher(t,
		&fakeProcSampler{cycles: [][]ProcInfo{{invalid}, {invalid}, {invalid}}},
		map[string]any{"postgres": map[string]any{checks.CheckKeyExe: "/usr/lib64/postgresql-18/bin/postgres"}},
	)
	notifier := &fakeNotifier{name: "ops"}
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	watcher.notifiers = append(watcher.notifiers, notifier)
	watcher.notifyInterval = 10 * time.Minute
	watcher.now = func() time.Time { return now }

	watcher.runCycle(context.Background()) // initial unknown process → notify
	now = now.Add(5 * time.Minute)
	watcher.runCycle(context.Background()) // still unknown, within interval
	now = now.Add(5 * time.Minute)
	watcher.runCycle(context.Background()) // reminder interval elapsed

	if got := len(notifier.msgs); got != 2 {
		t.Fatalf("notifications = %d, want initial alert and one interval reminder", got)
	}
}

func TestProcessPolicyWatcherAllowsAnchoredCommandOnly(t *testing.T) {
	valid := ProcInfo{
		PID: 7, UID: 70, Exe: "/usr/lib64/postgresql-18/bin/postgres", ExeOK: true, StartTicks: 10,
		Cmdline: []string{"postgres", "-D", "/srv/postgres"},
	}
	invalid := valid
	invalid.Cmdline = []string{"postgres", "-D", "/tmp/other", "--password=hidden"}
	watcher, events, snapshot := testProcessPolicyWatcher(t,
		&fakeProcSampler{cycles: [][]ProcInfo{{valid}, {invalid}, {invalid}}},
		map[string]any{"postmaster": map[string]any{
			checks.CheckKeyExe:     "/usr/lib64/postgresql-18/bin/postgres",
			process.SelectorKeyCmd: "^postgres -D /srv/postgres$",
		}},
	)

	watcher.runCycle(context.Background())
	if !snapshot.OK || snapshot.Condition || len(*events) != 0 {
		t.Fatalf("allowlisted command snapshot/events = %+v/%v", *snapshot, *events)
	}
	watcher.runCycle(context.Background())
	watcher.runCycle(context.Background())
	if len(*events) != 1 || !strings.Contains((*events)[0].Message, processPolicyReasonCommand) {
		t.Fatalf("command policy events = %+v", *events)
	}
	if strings.Contains((*events)[0].Message, "hidden") {
		t.Fatalf("command policy event leaked cmdline: %+v", (*events)[0])
	}
}

func TestProcessPolicyWatcherReportsDeletedExecutable(t *testing.T) {
	deleted := ProcInfo{PID: 9, UID: 70, ExePrev: "/usr/lib64/postgresql-18/bin/postgres", StartTicks: 30}
	watcher, events, snapshot := testProcessPolicyWatcher(t,
		&fakeProcSampler{cycles: [][]ProcInfo{{deleted}}},
		map[string]any{"postgres": map[string]any{checks.CheckKeyExe: "/usr/lib64/postgresql-18/bin/postgres"}},
	)

	watcher.runCycle(context.Background())
	if snapshot.OK || len(*events) != 1 || !strings.Contains((*events)[0].Message, processPolicyReasonReplacedExe) {
		t.Fatalf("deleted executable snapshot/events = %+v/%+v", *snapshot, *events)
	}
	if strings.Contains((*events)[0].Message, deleted.ExePrev) {
		t.Fatalf("deleted executable event must not present a previous path as verified identity: %+v", (*events)[0])
	}
}

func TestProcessPolicyWatcherBoundsPublishedViolationDetails(t *testing.T) {
	invalid := make([]ProcInfo, processPIDListLimit+1)
	for i := range invalid {
		invalid[i] = ProcInfo{
			PID:        i + 1,
			UID:        70,
			Exe:        "/usr/bin/bash",
			ExeOK:      true,
			StartTicks: uint64(i + 1),
		}
	}
	watcher, _, snapshot := testProcessPolicyWatcher(t,
		&fakeProcSampler{cycles: [][]ProcInfo{invalid}},
		map[string]any{"postgres": map[string]any{checks.CheckKeyExe: "/usr/lib64/postgresql-18/bin/postgres"}},
	)

	watcher.runCycle(context.Background())

	for _, field := range []string{checks.DataKeyPIDs, checks.DataKeyViolations} {
		got := snapshot.Data[field].(string)
		if !strings.Contains(got, "+1 more") || strings.Contains(got, "21") {
			t.Fatalf("%s = %q, want a 20-entry bounded list", field, got)
		}
	}
}

func TestProcessPolicyWatcherSampleFailurePublishesFailure(t *testing.T) {
	watcher, events, snapshot := testProcessPolicyWatcher(t,
		&fakeProcSampler{cycles: [][]ProcInfo{{}}, failCycles: []bool{true}},
		map[string]any{"postgres": map[string]any{checks.CheckKeyExe: "/usr/lib64/postgresql-18/bin/postgres"}},
	)

	watcher.runCycle(context.Background())
	if snapshot.OK || snapshot.Message != "process policy user postgres: sample unavailable" || len(*events) != 0 {
		t.Fatalf("sample failure snapshot/events = %+v/%+v", *snapshot, *events)
	}
}

func TestBuildProcessPolicyWatchRejectsActions(t *testing.T) {
	entry := map[string]any{
		"check": map[string]any{
			"type":  checks.CheckTypeProcessPolicy,
			"user":  "postgres",
			"allow": map[string]any{"postgres": map[string]any{"exe": "/usr/lib64/postgresql-18/bin/postgres"}},
		},
		"then": map[string]any{"hook": map[string]any{"command": []any{"/bin/false"}}},
	}
	watches, warnings := BuildWatches(cfgWithWatches(map[string]any{"postgres-policy": entry}), Deps{}, time.Minute)
	if len(watches) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "alert-only process_policy") {
		t.Fatalf("BuildWatches() = watches:%+v warnings:%v", watches, warnings)
	}
}

func TestProcessPolicyWatchIsHostScoped(t *testing.T) {
	entry := map[string]any{"check": map[string]any{"type": checks.CheckTypeProcessPolicy}}
	if got := unsupportedServiceWatchType(entry); got == "" {
		t.Fatal("process_policy must stay host-scoped")
	}
}
