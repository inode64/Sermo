package app

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/notify"
	"sermo/internal/process"
)

func TestProcessPolicyAnnouncementUsesTheDispatchDecision(t *testing.T) {
	n := &fakeNotifier{name: "ops"}
	calls := 0
	w := &processPolicyWatcher{
		name: "execution-policy", notifiers: []notify.Notifier{n},
		inPanic:   func() bool { calls++; return calls > 1 },
		incidents: &pidIncidents{watch: "execution-policy", activeLoaded: true, active: true}}
	w.notify(t.Context(), "unexpected process", map[string]string{})
	if !w.incidents.announced || len(n.msgs) != 1 || calls != 1 {
		t.Fatalf("announced=%v messages=%d panic reads=%d", w.incidents.announced, len(n.msgs), calls)
	}
}

func testProcessPolicyWatcher(t *testing.T, sampler ProcSampler, allow map[string]any) (*processPolicyWatcher, *[]Event, *checks.Result) {
	t.Helper()
	allows, err := parseProcessPolicyAllows("postgres", map[string]any{checks.CheckKeyAllow: allow})
	if err != nil {
		t.Fatalf("parseProcessPolicyAllows() error = %v", err)
	}
	events := []Event{}
	var snapshot checks.Result
	emit := NewPersistenceEvents(func(event Event) { events = append(events, event) }, nil).Emit
	watcher := &processPolicyWatcher{
		name:      "postgres-execution-policy",
		checkType: checks.CheckTypeProcessPolicy,
		incidents: &pidIncidents{watch: "postgres-execution-policy", slot: processPolicyStateSlot, emit: emit},
		sampler:   sampler,
		emit:      emit,
		publish: func(watch, checkType string, result checks.Result) {
			if watch != "postgres-execution-policy" || checkType != checks.CheckTypeProcessPolicy {
				t.Fatalf("published %s/%s, want process policy snapshot", watch, checkType)
			}
			snapshot = result
		},
		user:    "postgres",
		allows:  allows,
		resolve: func(user string) (uint32, bool) { return 70, user == "postgres" },
	}
	return watcher, &events, &snapshot
}

func TestProcessPolicyWatcherAlertsOncePerPIDIncarnation(t *testing.T) {
	const secretArgument = "--password=never-publish-this"
	invalid := ProcInfo{PID: 42, UID: 70, Exe: "/usr/bin/bash", ExeOK: true, StartTicks: 100, Cmdline: []string{"bash", "-c", secretArgument}}
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

func TestProcessPolicyWatcherRecoversAfterRestart(t *testing.T) {
	store := openRuleStateStore(t)
	allow := map[string]any{"postgres": map[string]any{checks.CheckKeyExe: "/usr/bin/postgres"}}
	invalid := ProcInfo{PID: 42, UID: 70, Exe: "/usr/bin/bash", ExeOK: true, StartTicks: 100}
	first, firstEvents, _ := testProcessPolicyWatcher(t, &fakeProcSampler{cycles: [][]ProcInfo{{invalid}}}, allow)
	first.incidents.stateStore = store
	first.runCycle(t.Context())
	if len(*firstEvents) != 1 || (*firstEvents)[0].Kind != eventKindFiring {
		t.Fatalf("initial policy events = %+v", *firstEvents)
	}
	second, secondEvents, _ := testProcessPolicyWatcher(t, &fakeProcSampler{cycles: [][]ProcInfo{{}}}, allow)
	second.incidents.stateStore = store
	second.runCycle(t.Context())
	second.runCycle(t.Context())
	if len(*secondEvents) != 1 || (*secondEvents)[0].Kind != eventKindRecovered {
		t.Fatalf("recovery after restart = %+v", *secondEvents)
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
	watcher.incidents.notifyInterval = 10 * time.Minute
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
	valid := ProcInfo{PID: 7, UID: 70, Exe: "/usr/lib64/postgresql-18/bin/postgres", ExeOK: true, StartTicks: 10, Cmdline: []string{"postgres", "-D", "/srv/postgres"}}
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

func TestProcessPolicyWatcherWithoutActionsRecordsOnlyFinding(t *testing.T) {
	for _, mode := range []string{"normal", "dry-run", "panic"} {
		t.Run(mode, func(t *testing.T) {
			invalid := ProcInfo{PID: 9, UID: 70, ExePrev: "/usr/bin/postgres", StartTicks: 30}
			watcher, events, snapshot := testProcessPolicyWatcher(t,
				&fakeProcSampler{cycles: [][]ProcInfo{{invalid}, {invalid}}},
				map[string]any{"postgres": map[string]any{checks.CheckKeyExe: "/usr/bin/postgres"}},
			)
			watcher.dryRun = mode == "dry-run"
			watcher.inPanic = func() bool { return mode == "panic" }
			watcher.runCycle(context.Background())
			watcher.runCycle(context.Background())
			if snapshot.OK || len(*events) != 1 || (*events)[0].Kind != eventKindFiring {
				t.Fatalf("monitor-only policy snapshot/events = %+v/%+v", *snapshot, *events)
			}
		})
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

func TestProcessPolicyWatcherSkipsOnlyExitedUnresolvedProcesses(t *testing.T) {
	errUnverifiable := errors.New("cannot verify exit")
	unresolved := ProcInfo{PID: 11, UID: 70, StartTicks: 40, StartTicksOK: true}
	zombie := unresolved
	zombie.State = process.ProcStateZombie
	unknownGeneration := unresolved
	unknownGeneration.StartTicksOK = false
	replaced := unresolved
	replaced.ExePrev = "/usr/lib64/postgresql-18/bin/postgres"
	tests := []struct {
		name       string
		info       ProcInfo
		exited     bool
		exitErr    error
		wantReason string
	}{
		{name: "zombie", info: zombie},
		{name: "exited after the sample", info: unresolved, exited: true},
		{name: "still running", info: unresolved, wantReason: processPolicyReasonUnresolvedExe},
		{name: "exit unverifiable", info: unresolved, exited: true, exitErr: errUnverifiable, wantReason: processPolicyReasonUnresolvedExe},
		{name: "unknown generation", info: unknownGeneration, exited: true, wantReason: processPolicyReasonUnresolvedExe},
		{name: "replaced executable", info: replaced, exited: true, wantReason: processPolicyReasonReplacedExe},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			watcher, events, snapshot := testProcessPolicyWatcher(t,
				&fakeProcSampler{cycles: [][]ProcInfo{{tt.info}}},
				map[string]any{"postgres": map[string]any{checks.CheckKeyExe: "/usr/lib64/postgresql-18/bin/postgres"}},
			)
			watcher.exited = func(pid int, ticks uint64) (bool, error) {
				if pid != tt.info.PID || ticks != tt.info.StartTicks {
					t.Fatalf("exited(%d, %d), want the sampled generation", pid, ticks)
				}
				return tt.exited, tt.exitErr
			}

			watcher.runCycle(context.Background())
			if tt.wantReason == "" {
				if !snapshot.OK || len(*events) != 0 {
					t.Fatalf("exited process snapshot/events = %+v/%+v, want no violation", *snapshot, *events)
				}
				return
			}
			if snapshot.OK || len(*events) != 1 || !strings.Contains((*events)[0].Message, tt.wantReason) {
				t.Fatalf("snapshot/events = %+v/%+v, want one %q violation", *snapshot, *events, tt.wantReason)
			}
		})
	}
}

func TestProcessPolicyWatcherBoundsPublishedViolationDetails(t *testing.T) {
	invalid := make([]ProcInfo, processPIDListLimit+1)
	for i := range invalid {
		invalid[i] = ProcInfo{PID: i + 1, UID: 70, Exe: "/usr/bin/bash", ExeOK: true, StartTicks: uint64(i + 1)}
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

func TestBuildProcessPolicyWatchRejectsUnknownAllowField(t *testing.T) {
	entry := map[string]any{
		"check": map[string]any{
			"type": checks.CheckTypeProcessPolicy,
			"user": "postgres",
			"allow": map[string]any{
				"postgres": map[string]any{
					"exe":   "/usr/lib64/postgresql-18/bin/postgres",
					"extra": true,
				},
			},
		},
	}
	watches, warnings := BuildWatches(cfgWithWatches(map[string]any{"postgres-policy": entry}), Deps{}, time.Minute)
	if len(watches) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "extra is not supported") {
		t.Fatalf("BuildWatches() = watches:%+v warnings:%v", watches, warnings)
	}
}

func TestProcessPolicyWatchIsHostScoped(t *testing.T) {
	entry := map[string]any{"check": map[string]any{"type": checks.CheckTypeProcessPolicy}}
	if got := unsupportedServiceWatchType(entry); got == "" {
		t.Fatal("process_policy must stay host-scoped")
	}
}

func TestProcessPolicyWatcherAllowsDescendantsOfEntryPointsOnly(t *testing.T) {
	const bash, server = "/usr/bin/bash", "/usr/lib64/postgresql-18/bin/postgres"
	proc := func(pid, ppid int, exe string, cmdline ...string) ProcInfo {
		return ProcInfo{PID: pid, PPID: ppid, UID: 70, Exe: exe, ExeOK: true, StartTicks: uint64(pid), Cmdline: cmdline}
	}
	replaced := proc(13, 10, "")
	replaced.ExeOK, replaced.ExePrev = false, "/usr/bin/pg_dump"
	samples := []ProcInfo{
		proc(10, 1, bash, "/bin/bash", "/opt/backup.sh"), // reviewed entry point
		proc(11, 10, "/usr/bin/df", "df", "-Pm"),         // its child
		proc(12, 11, "/usr/bin/gawk", "awk", "{print}"),  // and grandchild
		replaced, // a replaced binary is still reported
		proc(20, 1, server, "postgres", "-D", "/srv/pg"),
		proc(21, 20, bash, "sh", "-c", "id"),           // a shell the server spawned
		proc(30, 1, "/usr/bin/df", "df"),               // reparented to init
		proc(40, 1, bash, "/bin/bash", "/tmp/evil.sh"), // bash outside the entry pattern
		proc(41, 40, "/usr/bin/df", "df"),              // and its child
	}
	allow := map[string]any{
		"server": map[string]any{checks.CheckKeyExe: server},
		"backup": map[string]any{checks.CheckKeyExe: bash, process.SelectorKeyCmd: `^/bin/bash /opt/backup\.sh$`, checks.CheckKeyDescendants: true},
	}
	reported := func(allow map[string]any) []int {
		t.Helper()
		watcher, events, _ := testProcessPolicyWatcher(t, &fakeProcSampler{cycles: [][]ProcInfo{samples}}, allow)
		watcher.runCycle(context.Background())
		var pids []int
		for _, e := range *events {
			for _, info := range samples {
				if strings.Contains(e.Message, "pid "+strconv.Itoa(info.PID)+":") {
					pids = append(pids, info.PID)
				}
			}
		}
		slices.Sort(pids)
		return pids
	}

	if got, want := reported(allow), []int{13, 21, 30, 40, 41}; !slices.Equal(got, want) {
		t.Fatalf("violations = %v, want %v", got, want)
	}
	allow["backup"] = map[string]any{checks.CheckKeyExe: bash, process.SelectorKeyCmd: `^/bin/bash /opt/backup\.sh$`}
	if got, want := reported(allow), []int{11, 12, 13, 21, 30, 40, 41}; !slices.Equal(got, want) {
		t.Fatalf("without descendants violations = %v, want %v", got, want)
	}
}
