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
}

func raidWith(st RaidStatus, preds ...levelPred) raidCheck {
	return raidCheck{base: base{name: "r", timeout: time.Second}, sampler: func() (RaidStatus, error) { return st, nil }, preds: preds}
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
