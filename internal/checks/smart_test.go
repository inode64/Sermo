package checks

import (
	"context"
	"strings"
	"testing"
	"time"

	"sermo/internal/execx"
)

const smartATA = `{
  "smart_status": { "passed": true },
  "temperature": { "current": 38 },
  "power_on_time": { "hours": 12000 },
  "ata_smart_attributes": { "table": [
    { "id": 5, "name": "Reallocated_Sector_Ct", "raw": { "value": 4 } },
    { "id": 9, "name": "Power_On_Hours", "raw": { "value": 12000 } }
  ] }
}`

const smartNVMeFailing = `{
  "smart_status": { "passed": false },
  "temperature": { "current": 65 },
  "nvme_smart_health_information_log": { "percentage_used": 92 }
}`

const smartSelfTestRunningJSON = `{
  "smart_status": { "passed": true },
  "ata_smart_data": { "self_test": { "status": {
    "value": 249,
    "string": "Self-test routine in progress 90% of test remaining."
  } } }
}`

func TestParseSmart(t *testing.T) {
	d, err := parseSmart(smartATA)
	if err != nil {
		t.Fatal(err)
	}
	if !d.healthKnown || !d.passed {
		t.Errorf("health = %+v, want known/passed", d)
	}
	if d.values["temperature"] != 38 || d.values["reallocated"] != 4 || d.values["power_on_hours"] != 12000 {
		t.Errorf("values = %v", d.values)
	}

	n, err := parseSmart(smartNVMeFailing)
	if err != nil {
		t.Fatal(err)
	}
	if n.passed || n.values["wear"] != 92 {
		t.Errorf("nvme = %+v, want failed / wear 92", n)
	}

	running, err := parseSmart(smartSelfTestRunningJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !running.selfTestRunning {
		t.Errorf("self-test state = %+v, want running", running)
	}

	if _, err := parseSmart(""); err == nil {
		t.Error("empty output must error")
	}
}

func TestSmartCheckSurfacesSelfTestState(t *testing.T) {
	result := smartWith(smartSelfTestRunningJSON).Run(context.Background())
	if got := result.Data[DataKeyDeviceState]; got != DeviceStateTesting {
		t.Fatalf("device state = %v, want %q", got, DeviceStateTesting)
	}
}

func smartWith(out string, preds ...levelPred) smartCheck {
	return smartCheck{
		base:   base{name: "sm", timeout: time.Second},
		runner: fakeRunner{execx.Result{Stdout: out}},
		device: "/dev/sda", preds: preds,
	}
}

func TestSmartCheck(t *testing.T) {
	// Default: alert when SMART health is FAILED.
	if res := smartWith(smartNVMeFailing).Run(context.Background()); !res.OK {
		t.Error("a FAILED SMART verdict should alert by default")
	}
	if res := smartWith(smartATA).Run(context.Background()); res.OK {
		t.Error("a PASSED verdict must not alert by default")
	}
	// Predicate: alert on reallocated sectors.
	if res := smartWith(smartATA, levelPred{"reallocated", ">", 0}).Run(context.Background()); !res.OK {
		t.Error("reallocated>0 predicate should alert")
	}
	if res := smartWith(smartATA, levelPred{"temperature", ">", 50}).Run(context.Background()); res.OK {
		t.Error("temperature 38 is not > 50")
	}
}

func TestSmartCheckError(t *testing.T) {
	c := smartCheck{
		base:   base{name: "sm", timeout: time.Second},
		runner: fakeRunner{execx.Result{Stderr: "/dev/sda: Unable to detect device type\n", ExitCode: 2}},
		device: "/dev/sda",
	}
	if res := c.Run(context.Background()); res.OK {
		t.Fatal("a smartctl error must fail the check")
	}
}

func TestStartSmartShortTest(t *testing.T) {
	runner := &recordingSmartRunner{result: execx.Result{ExitCode: execx.ExitCodeSuccess}}
	if err := StartSmartShortTest(context.Background(), runner, "/dev/sda", time.Second); err != nil {
		t.Fatalf("StartSmartShortTest() error = %v", err)
	}
	if runner.name != smartctlCommand || len(runner.args) != 2 || runner.args[0] != smartctlShortTest || runner.args[1] != "/dev/sda" {
		t.Fatalf("smartctl invocation = %q %v, want %q [%q %q]", runner.name, runner.args, smartctlCommand, smartctlShortTest, "/dev/sda")
	}
}

func TestStartSmartShortTestFailure(t *testing.T) {
	runner := recordingSmartRunner{result: execx.Result{ExitCode: 2, Stderr: "short self-test is already running\n"}}
	err := StartSmartShortTest(context.Background(), &runner, "/dev/sda", time.Second)
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("StartSmartShortTest() error = %v, want smartctl diagnostic", err)
	}
}

type recordingSmartRunner struct {
	result execx.Result
	name   string
	args   []string
}

func (r *recordingSmartRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	r.name = name
	r.args = append([]string(nil), args...)
	return r.result, nil
}
