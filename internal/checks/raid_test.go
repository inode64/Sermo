package checks

import (
	"context"
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

func raidWith(st RaidStatus, preds ...levelPred) *raidCheck {
	return &raidCheck{base: base{name: "r", timeout: time.Second}, sampler: func() (RaidStatus, error) { return st, nil }, preds: preds}
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
		base: base{name: "r", timeout: time.Second}, array: "md0",
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
	c := &raidCheck{base: base{name: "r", timeout: time.Second}, array: "md0", sysfsChanges: true, sampler: func() (RaidStatus, error) {
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
