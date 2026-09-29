package checks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const mdstatHealthy = `Personalities : [raid1]
md0 : active raid1 sdb1[1] sda1[0]
      976630464 blocks super 1.2 [2/2] [UU]

unused devices: <none>
`

const mdstatDegraded = `Personalities : [raid1] [raid6]
md0 : active raid1 sda1[0]
      976630464 blocks super 1.2 [2/1] [U_]

md1 : active raid6 sde1[4] sdd1[3] sdc1[2] sdb1[1]
      blocks super 1.2 [5/4] [UUUU_]
      [==>..................]  recovery = 12.6% (1/8) finish=20.0min

unused devices: <none>
`

func TestParseMdstat(t *testing.T) {
	h := parseMdstat(mdstatHealthy)
	if h.Arrays != 1 || h.Degraded != 0 || h.Recovering != 0 {
		t.Fatalf("healthy = %+v", h)
	}
	d := parseMdstat(mdstatDegraded)
	if d.Arrays != 2 || d.Degraded != 2 {
		t.Fatalf("degraded = %+v, want 2 arrays / 2 degraded", d)
	}
	if d.Recovering != 1 {
		t.Errorf("recovering = %d, want 1", d.Recovering)
	}
	if len(d.DegradedNames) != 2 {
		t.Errorf("degraded names = %v", d.DegradedNames)
	}
	if len(d.Details) != 2 || d.Details[1].Operation != "recovery" || !d.Details[1].HasProgress || d.Details[1].ProgressPct != 12.6 {
		t.Fatalf("details = %+v, want md1 recovery at 12.6%%", d.Details)
	}
}

// An array md could not start is listed as "inactive" with no [n/m] ratio or
// [U_] map; its data is unavailable, so it must not read as good.
func TestParseMdstatInactiveArrayIsDegraded(t *testing.T) {
	const mdstatInactive = `Personalities : [raid1]
md127 : inactive sdb1[1](S)
      976630464 blocks super 1.2

md0 : active raid1 sdc1[1] sdd1[0]
      976630464 blocks super 1.2 [2/2] [UU]

unused devices: <none>
`
	st := parseMdstat(mdstatInactive)
	if st.Arrays != 2 || st.Degraded != 1 || len(st.DegradedNames) != 1 || st.DegradedNames[0] != "md127" {
		t.Fatalf("status = %+v, want md127 as the only degraded array", st)
	}
	if !st.Details[0].Inactive || st.Details[1].Inactive || st.Details[1].Degraded {
		t.Fatalf("details = %+v, want only md127 inactive", st.Details)
	}
	res := (&raidCheck{name: "r", timeout: time.Second, array: "md127", sampler: func() (RaidStatus, error) { return st, nil }}).Run(t.Context())
	if !res.OK || res.Message != "raid md127: inactive" {
		t.Fatalf("result = %v %q, want alert for the inactive array", res.OK, res.Message)
	}
}

// A scrub ("check") is an active operation but not a reconstruction, so it
// must not raise the recovering count or its predicates.
func TestParseMdstatScrubIsNotRecovering(t *testing.T) {
	const mdstatScrub = `Personalities : [raid1]
md0 : active raid1 sdb1[1] sda1[0]
      976630464 blocks super 1.2 [2/2] [UU]
      [=>...................]  check =  8.3% (81234/976630464) finish=90.1min speed=150000K/sec

unused devices: <none>
`
	st := parseMdstat(mdstatScrub)
	if st.Recovering != 0 || st.Details[0].Recovering || st.Details[0].Operation != "check" || !st.Details[0].HasProgress {
		t.Fatalf("status = %+v, want an active check that is not recovering", st)
	}
}

func TestEnrichRaidSysfsReadsArraySize(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "md0", "md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "md0", "size"), []byte("2048\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := RaidStatus{Details: []RaidArrayStatus{{Name: "md0"}}}
	enrichRaidSysfs(&st, root)
	if got, want := st.Details[0].SizeBytes, uint64(1<<20); got != want {
		t.Fatalf("array size = %d, want %d", got, want)
	}
}

func raidWith(st RaidStatus, preds ...levelPred) *raidCheck {
	return &raidCheck{name: "r", timeout: time.Second, sampler: func() (RaidStatus, error) { return st, nil }, preds: preds}
}

func TestRaidCheck(t *testing.T) {
	// Default: alert when any array is degraded.
	if res := raidWith(RaidStatus{Arrays: 2, Degraded: 1}).Run(context.Background()); !res.OK {
		t.Error("a degraded array should alert by default")
	}
	if res := raidWith(RaidStatus{Arrays: 2}).Run(context.Background()); res.OK {
		t.Error("a healthy array must not alert")
	}
	// No md arrays -> never alerts.
	if res := raidWith(RaidStatus{}).Run(context.Background()); res.OK {
		t.Error("no arrays must not alert")
	}
	// Predicate override: alert while recovering.
	res := raidWith(RaidStatus{Arrays: 1, Recovering: 1}, levelPred{"recovering", ">", 0}).Run(context.Background())
	if !res.OK {
		t.Error("recovering>0 predicate should alert")
	}
}

// deviceStateCase is one row for a device-state mapping function returning
// (state, progressPct, active).
type deviceStateCase[T any] struct {
	name       string
	in         T
	wantState  string
	wantPct    float64
	wantActive bool
}

// runDeviceStateCases exercises fn over the cases, asserting the (state, pct,
// active) triple per row.
func runDeviceStateCases[T any](t *testing.T, fn func(T) (string, float64, bool), cases []deviceStateCase[T]) {
	t.Helper()
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			state, pct, active := fn(tt.in)
			if state != tt.wantState || pct != tt.wantPct || active != tt.wantActive {
				t.Fatalf("(%+v) = %q, %v, %v; want %q, %v, %v", tt.in, state, pct, active, tt.wantState, tt.wantPct, tt.wantActive)
			}
		})
	}
}

func TestRaidDeviceState(t *testing.T) {
	runDeviceStateCases(t,
		func(d RaidArrayStatus) (string, float64, bool) { return raidDeviceState([]RaidArrayStatus{d}) },
		[]deviceStateCase[RaidArrayStatus]{
			{name: "idle", in: RaidArrayStatus{Name: "md0"}},
			{name: "check", in: RaidArrayStatus{Name: "md0", Operation: "check", ProgressPct: 12.5, HasProgress: true}, wantState: DeviceStateTesting, wantPct: 12.5, wantActive: true},
			{name: "recovery", in: RaidArrayStatus{Name: "md0", Operation: "recovery"}, wantState: DeviceStateRecovering},
			{name: "resync", in: RaidArrayStatus{Name: "md0", Operation: "resync"}, wantState: DeviceStateRebuilding},
			{name: "reshape", in: RaidArrayStatus{Name: "md0", Operation: "reshape"}, wantState: DeviceStateRebuilding},
		})
}

func TestRaidDegradedNamesSurfaced(t *testing.T) {
	// Degraded array names appear in the message and the data only when present.
	res := raidWith(RaidStatus{Arrays: 2, Degraded: 1, DegradedNames: []string{"md0", "md1"}}).Run(context.Background())
	if !strings.Contains(res.Message, "md0, md1") {
		t.Fatalf("message %q must list the degraded arrays", res.Message)
	}
	if res.Data["degraded_arrays"] != "md0,md1" {
		t.Fatalf("degraded_arrays = %v, want md0,md1", res.Data["degraded_arrays"])
	}
	res2 := raidWith(RaidStatus{Arrays: 2}).Run(context.Background())
	if _, has := res2.Data["degraded_arrays"]; has {
		t.Fatalf("a healthy array must not carry degraded_arrays: %v", res2.Data)
	}
	if strings.Contains(res2.Message, "(") {
		t.Fatalf("a healthy message must not have a names clause: %q", res2.Message)
	}
}

func TestRaidCheckIndividualArrayAndTransitions(t *testing.T) {
	samples := []RaidStatus{
		{Arrays: 1, Details: []RaidArrayStatus{{Name: "md0"}}},
		{Arrays: 1, Degraded: 1, DegradedNames: []string{"md0"}, Details: []RaidArrayStatus{{Name: "md0", Degraded: true}}},
		{Arrays: 1, Degraded: 1, Recovering: 1, DegradedNames: []string{"md0"}, Details: []RaidArrayStatus{{Name: "md0", Degraded: true, Recovering: true, Operation: "recovery", ProgressPct: 12.6, HasProgress: true}}},
		{Arrays: 1, Details: []RaidArrayStatus{{Name: "md0"}}},
	}
	index := 0
	c := &raidCheck{
		name: "r", timeout: time.Second, array: "md0",
		sampler: func() (RaidStatus, error) {
			st := samples[index]
			index++
			return st, nil
		},
	}
	if got := c.Run(context.Background()); got.OK {
		t.Fatalf("baseline = %+v, want healthy", got)
	}
	if got := RaidTransitions(c.Run(context.Background())); len(got) != 1 || got[0].Event != RaidNotifyOnDegraded {
		t.Fatalf("degraded transitions = %+v", got)
	}
	if got := RaidTransitions(c.Run(context.Background())); len(got) != 1 || got[0].Event != RaidNotifyOnRecovering || !got[0].HasProgress {
		t.Fatalf("recovering transitions = %+v", got)
	}
	if got := RaidTransitions(c.Run(context.Background())); len(got) != 1 || got[0].Event != RaidNotifyOnGood {
		t.Fatalf("good transitions = %+v", got)
	}
}

func TestRaidCheckSysfsTransitionsAndMissingArray(t *testing.T) {
	old := RaidArrayStatus{Name: "md0", MismatchCount: "0", Members: []RaidMemberStatus{{Name: "sda1", State: "in_sync", Errors: "0", BadBlocks: "none"}}}
	current := RaidArrayStatus{Name: "md0", MismatchCount: "1", Members: []RaidMemberStatus{{Name: "sda1", State: "faulty", Errors: "2", BadBlocks: "8"}}}
	samples := []RaidStatus{{Arrays: 1, Details: []RaidArrayStatus{old}}, {Arrays: 1, Details: []RaidArrayStatus{current}}}
	index := 0
	c := &raidCheck{name: "r", timeout: time.Second, array: "md0", sysfsChanges: true, sampler: func() (RaidStatus, error) {
		st := samples[index]
		index++
		return st, nil
	}}
	_ = c.Run(context.Background())
	transitions := RaidTransitions(c.Run(context.Background()))
	if len(transitions) != 4 {
		t.Fatalf("sysfs transitions = %+v, want mismatch plus three member fields", transitions)
	}
	for _, transition := range transitions {
		if transition.Event != RaidNotifyOnArrayChange {
			t.Fatalf("transition = %+v, want array change", transition)
		}
	}
	missing := raidWith(RaidStatus{Arrays: 1, Details: []RaidArrayStatus{{Name: "md0"}}})
	missing.array = "md1"
	if got := missing.Run(context.Background()); !got.OK || got.Data[DataKeyPresent] != false {
		t.Fatalf("missing array = %+v, want alert with present=false", got)
	}
}

// md only holds a reconstruction while sync_action is "frozen": "idle" stops
// the running pass but lets the kernel relaunch the pending one, and resume
// must unfreeze with "idle" so md restarts whichever pass is needed.
func TestSetRaidRebuildState(t *testing.T) {
	tests := []struct {
		name    string
		resume  bool
		before  RaidArrayStatus
		after   RaidArrayStatus
		want    string
		wantErr string
	}{
		{name: "pause freezes", before: RaidArrayStatus{Name: "md0", Operation: "recovery", SyncAction: raidSyncActionRecover}, after: RaidArrayStatus{Name: "md0", SyncAction: raidSyncActionFrozen}, want: raidSyncActionFrozen},
		{name: "pause relaunched", before: RaidArrayStatus{Name: "md0", Operation: "recovery", SyncAction: raidSyncActionRecover}, after: RaidArrayStatus{Name: "md0", Operation: "recovery", SyncAction: raidSyncActionIdle}, want: raidSyncActionFrozen, wantErr: "did not pause"},
		{name: "pause idle array", before: RaidArrayStatus{Name: "md0", SyncAction: raidSyncActionIdle}, wantErr: "not reconstructing"},
		{name: "resume frozen", resume: true, before: RaidArrayStatus{Name: "md0", SyncAction: raidSyncActionFrozen}, after: RaidArrayStatus{Name: "md0", Operation: "recovery", SyncAction: raidSyncActionRecover}, want: raidSyncActionIdle},
		{name: "resume with nothing pending", resume: true, before: RaidArrayStatus{Name: "md0", SyncAction: raidSyncActionFrozen}, after: RaidArrayStatus{Name: "md0", SyncAction: raidSyncActionIdle}, want: raidSyncActionIdle},
		{name: "resume idle array", resume: true, before: RaidArrayStatus{Name: "md0", SyncAction: raidSyncActionIdle}, wantErr: "is not paused"},
		{name: "resume still frozen", resume: true, before: RaidArrayStatus{Name: "md0", SyncAction: raidSyncActionFrozen}, after: RaidArrayStatus{Name: "md0", SyncAction: raidSyncActionFrozen}, want: raidSyncActionIdle, wantErr: "did not resume"},
		{name: "resume running", resume: true, before: RaidArrayStatus{Name: "md0", Operation: "resync", SyncAction: raidSyncActionResync}, wantErr: "is not paused"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "md0", "md", raidSyncActionFile)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("untouched\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			calls := 0
			sample := func() (RaidStatus, error) {
				calls++
				detail := tc.before
				if calls > 1 {
					detail = tc.after
				}
				return RaidStatus{Arrays: 1, Details: []RaidArrayStatus{detail}}, nil
			}
			_, err := setRaidRebuildState(t.Context(), "md0", tc.resume, root, sample)
			if tc.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.want
			if want == "" {
				want = "untouched" // a rejected preflight must not write
			}
			if strings.TrimSpace(string(got)) != want {
				t.Fatalf("sync_action = %q, want %q", got, want)
			}
		})
	}
}
