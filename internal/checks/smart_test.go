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

func smartWith(out string, preds ...levelPred) *smartCheck {
	return &smartCheck{
		base:   base{name: "sm", timeout: time.Second},
		runner: fakeRunner{execx.Result{Stdout: out}},
		device: "/dev/sda", preds: preds,
		deviceIdentity: testDeviceIdentity,
		last:           &lastSample{},
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
	if res := smartWith(smartATA,
		levelPred{"reallocated", ">", 0},
		levelPred{"temperature", ">", 100},
	).Run(context.Background()); !res.OK {
		t.Error("each SMART predicate is an independent early-warning condition")
	}
	// Predicates add early-warning conditions; they must not replace the drive's
	// own FAILED verdict.
	if res := smartWith(smartNVMeFailing, levelPred{"temperature", ">", 100}).Run(context.Background()); !res.OK {
		t.Error("a FAILED SMART verdict should still alert when predicates do not hold")
	}
}

func TestSmartCheckError(t *testing.T) {
	c := &smartCheck{
		base:   base{name: "sm", timeout: time.Second},
		runner: fakeRunner{execx.Result{Stderr: "/dev/sda: Unable to detect device type\n", ExitCode: 2}},
		device: "/dev/sda", deviceIdentity: testDeviceIdentity, last: &lastSample{},
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

// smartDeviceGone is smartctl's JSON envelope for a drive that fell off its bus:
// well-formed output with no smart_status and exit-status bit 1 set. Captured
// from a SATA disk that stopped answering INQUIRY while its /dev node lived on.
const smartDeviceGone = `{
  "smartctl": {
    "messages": [
      { "string": "Smartctl open device: /dev/sda failed: INQUIRY failed", "severity": "error" }
    ],
    "exit_status": 2
  },
  "local_time": { "time_t": 1787231441 }
}`

func TestSmartCheckReportsMissingDevice(t *testing.T) {
	res := smartWith(smartDeviceGone).Run(context.Background())
	if !res.Unavailable {
		t.Errorf("Unavailable = false, want true: a device smartctl cannot open is not a healthy sample")
	}
	if got := res.Observation(); got != ObservationUnavailable {
		t.Errorf("Observation() = %q, want %q", got, ObservationUnavailable)
	}
	if got := res.Data[DataKeyDeviceState]; got != DeviceStateMissing {
		t.Errorf("device state = %v, want %q", got, DeviceStateMissing)
	}
	if got := res.Data[DataKeyHealth]; got != DeviceStateMissing {
		t.Errorf("health = %v, want %q", got, DeviceStateMissing)
	}
	if !strings.Contains(res.Message, "/dev/sda") || !strings.Contains(res.Message, DeviceStateMissing) {
		t.Errorf("message = %q, want it to name the device and say it is missing", res.Message)
	}
}

// smartATAFull is a real `smartctl -i -H -A -c -l selftest -j` report, trimmed to
// the fields Sermo reads. Captured from a SATA disk in service.
const smartATAFull = `{
  "device": { "name": "/dev/sdb", "type": "sat", "protocol": "ATA" },
  "model_family": "Western Digital Red Plus",
  "model_name": "WDC WD20EFRX-68EUZN0",
  "serial_number": "WD-WCC4M4SZ375K",
  "wwn": { "naa": 5, "oui": 5358, "id": 10257889635 },
  "firmware_version": "82.00A82",
  "user_capacity": { "blocks": 3907029168, "bytes": 2000398934016 },
  "rotation_rate": 5400,
  "smart_status": { "passed": true },
  "ata_smart_data": { "self_test": { "status": { "value": 0, "string": "completed without error", "passed": true } } },
  "ata_smart_attributes": { "table": [
    { "id": 5, "name": "Reallocated_Sector_Ct", "raw": { "value": 0 } },
    { "id": 9, "name": "Power_On_Hours", "raw": { "value": 82390 } },
    { "id": 12, "name": "Power_Cycle_Count", "raw": { "value": 137 } },
    { "id": 197, "name": "Current_Pending_Sector", "raw": { "value": 3 } },
    { "id": 199, "name": "UDMA_CRC_Error_Count", "raw": { "value": 7 } }
  ] },
  "power_on_time": { "hours": 82390 },
  "power_cycle_count": 137,
  "temperature": { "current": 41 },
  "ata_smart_self_test_log": { "standard": { "table": [
    { "type": { "value": 1, "string": "Short offline" },
      "status": { "value": 0, "string": "Completed without error", "passed": true },
      "lifetime_hours": 3468 }
  ] } }
}`

// smartNVMeFull is the same report for an NVMe drive: no attribute table, and
// the identity, endurance and error counters spelled the NVMe way.
const smartNVMeFull = `{
  "device": { "name": "/dev/nvme0n1", "type": "nvme", "protocol": "NVMe" },
  "model_name": "Samsung SSD 980 500GB",
  "serial_number": "S64DNL0T923705D",
  "firmware_version": "2B4QFXO7",
  "nvme_total_capacity": 500107862016,
  "smart_status": { "passed": true, "nvme": { "value": 0 } },
  "temperature": { "current": 45 },
  "nvme_smart_health_information_log": {
    "critical_warning": 0, "available_spare": 100, "percentage_used": 11,
    "power_cycles": 86, "power_on_hours": 1482, "unsafe_shutdowns": 75, "media_errors": 2
  },
  "endurance_used": { "current_percent": 11 },
  "power_on_time": { "hours": 1482 },
  "power_cycle_count": 86,
  "nvme_self_test_log": { "nsid": -1, "current_self_test_operation": { "value": 0, "string": "No self-test in progress" } }
}`

func TestParseSmartReadsDriveIdentity(t *testing.T) {
	d, err := parseSmart(smartATAFull)
	if err != nil {
		t.Fatal(err)
	}
	want := smartIdentity{
		Model:         "WDC WD20EFRX-68EUZN0",
		Serial:        "WD-WCC4M4SZ375K",
		Firmware:      "82.00A82",
		WWN:           "0x50014ee2636af963",
		Rotation:      "5400 rpm",
		CapacityBytes: 2000398934016,
	}
	if d.identity != want {
		t.Errorf("identity = %+v, want %+v", d.identity, want)
	}
	if got := d.selfTest; got != "Completed without error at 3468 h" {
		t.Errorf("self-test = %q, want the verdict and the drive hour it ran at", got)
	}
	for field, want := range map[string]float64{
		fieldTemperature: 41, fieldPowerOnHours: 82390, fieldPowerCycles: 137,
		fieldReallocated: 0, fieldPendingSectors: 3, fieldCRCErrors: 7,
	} {
		if got := d.values[field]; got != want {
			t.Errorf("values[%s] = %v, want %v", field, got, want)
		}
	}
}

func TestParseSmartReadsNVMeIdentityAndCounters(t *testing.T) {
	d, err := parseSmart(smartNVMeFull)
	if err != nil {
		t.Fatal(err)
	}
	want := smartIdentity{
		Model:         "Samsung SSD 980 500GB",
		Serial:        "S64DNL0T923705D",
		Firmware:      "2B4QFXO7",
		CapacityBytes: 500107862016,
	}
	if d.identity != want {
		t.Errorf("identity = %+v, want %+v", d.identity, want)
	}
	for field, want := range map[string]float64{
		fieldTemperature: 45, fieldWear: 11, fieldMediaErrors: 2,
		fieldPowerOnHours: 1482, fieldPowerCycles: 86,
	} {
		if got := d.values[field]; got != want {
			t.Errorf("values[%s] = %v, want %v", field, got, want)
		}
	}
	if _, ok := d.values[fieldReallocated]; ok {
		t.Error("an NVMe drive has no attribute table, so it must report no reallocated-sector count")
	}
}

func TestSmartCheckPublishesDriveIdentity(t *testing.T) {
	res := smartWith(smartATAFull).Run(context.Background())
	for key, want := range map[string]any{
		DataKeyModel:             "WDC WD20EFRX-68EUZN0",
		DataKeySerialNumber:      "WD-WCC4M4SZ375K",
		DataKeyFirmware:          "82.00A82",
		DataKeyWWN:               "0x50014ee2636af963",
		DataKeyRotationRate:      "5400 rpm",
		DataKeyCapacityBytes:     uint64(2000398934016),
		DataKeySelfTest:          "Completed without error at 3468 h",
		SmartFieldPendingSectors: 3.0,
	} {
		if got := res.Data[key]; got != want {
			t.Errorf("Data[%s] = %v (%T), want %v", key, got, got, want)
		}
	}
}

// smartctlArgs must ask for the device information block and the self-test log:
// without -i a report names no drive, and without -l selftest it never says how
// the last test ended.
func TestSmartctlArgsRequestIdentityAndSelfTestLog(t *testing.T) {
	args := strings.Join(smartctlArgs("/dev/sda"), " ")
	for _, want := range []string{"-i", "-H", "-A", "-c", "-l selftest", "-j", "/dev/sda"} {
		if !strings.Contains(args, want) {
			t.Errorf("smartctl args %q must contain %q", args, want)
		}
	}
}

func TestSmartCheckReportsLastKnownReadingsWhenDeviceGone(t *testing.T) {
	runner := &scriptedRunner{results: []execx.Result{{Stdout: smartATAFull}, {Stdout: smartDeviceGone}}}
	c := &smartCheck{
		base:   base{name: "sm", timeout: time.Second},
		runner: runner, device: "/dev/sda",
		deviceIdentity: testDeviceIdentity,
		last:           &lastSample{},
	}
	if res := c.Run(context.Background()); res.Unavailable {
		t.Fatalf("first sample must succeed: %s", res.Message)
	}
	res := c.Run(context.Background())
	if !res.Unavailable || res.Data[DataKeyHealth] != DeviceStateMissing {
		t.Fatalf("second sample = %+v, want an unavailable missing device", res)
	}
	if got := res.Data[DataKeyLastHealth]; got != smartHealthPassed {
		t.Errorf("Data[%s] = %v, want the verdict the drive last gave", DataKeyLastHealth, got)
	}
	if got := res.Data[LastSampleKey(fieldTemperature)]; got != 41.0 {
		t.Errorf("Data[%s] = %v, want the temperature the drive last reported", LastSampleKey(fieldTemperature), got)
	}
	if _, ok := res.Data[fieldTemperature]; ok {
		t.Error("a retained reading must never be republished under its live key: the graph would record it as a fresh sample")
	}
	if _, ok := res.Data[DataKeyLastSeenSeconds]; !ok {
		t.Errorf("Data must date the retained sample under %s", DataKeyLastSeenSeconds)
	}
}

func TestSmartCheckIdentifiesAMissingDeviceFromSysfs(t *testing.T) {
	res := smartWith(smartDeviceGone).Run(context.Background())
	if got := res.Data[DataKeyModel]; got != "TEST DISK" {
		t.Errorf("Data[%s] = %v, want the model sysfs still publishes for a drive that stopped answering", DataKeyModel, got)
	}
	if _, ok := res.Data[DataKeyLastSeenSeconds]; ok {
		t.Error("a check that never got a reading must invent no history")
	}
}

func TestSmartCheckIdentifiesADriveSmartctlCouldNotRead(t *testing.T) {
	c := &smartCheck{
		base:   base{name: "sm", timeout: time.Second},
		runner: fakeRunner{execx.Result{Stderr: "/dev/sda: Unable to detect device type\n", ExitCode: 2}},
		device: "/dev/sda", deviceIdentity: testDeviceIdentity, last: &lastSample{},
	}
	res := c.Run(context.Background())
	if !res.Unavailable {
		t.Fatal("a smartctl error must fail the check")
	}
	if res.Data[DataKeyDevice] != "/dev/sda" || res.Data[DataKeySerialNumber] != "SN0" {
		t.Errorf("Data = %v, want the device and the identity sysfs holds for it", res.Data)
	}
}

// scriptedRunner answers each Run with the next prepared result, so one check
// instance can be walked through a sequence of cycles.
type scriptedRunner struct {
	results []execx.Result
	calls   int
}

func (r *scriptedRunner) Run(context.Context, string, ...string) (execx.Result, error) {
	res := r.results[min(r.calls, len(r.results)-1)]
	r.calls++
	return res, nil
}

// An NVMe drive publishes no rotation rate at all, so the medium has to come
// from the kernel or the dashboard would describe a flash drive less completely
// than it describes a platter one.
func TestSmartCheckFallsBackToKernelRotation(t *testing.T) {
	c := smartWith(smartNVMeFull)
	c.deviceIdentity = func(string) BlockDeviceIdentity {
		return BlockDeviceIdentity{Rotation: rotationSolidState}
	}
	if got := c.Run(context.Background()).Data[DataKeyRotationRate]; got != rotationSolidState {
		t.Errorf("Data[%s] = %v, want the kernel's answer when smartctl gave none", DataKeyRotationRate, got)
	}
	// A drive that reported its own rate keeps it: smartctl knows the rpm figure
	// the kernel's bare rotational flag never carries.
	spinning := smartWith(smartATAFull)
	spinning.deviceIdentity = func(string) BlockDeviceIdentity {
		return BlockDeviceIdentity{Rotation: rotationRotational}
	}
	if got := spinning.Run(context.Background()).Data[DataKeyRotationRate]; got != "5400 rpm" {
		t.Errorf("Data[%s] = %v, want the drive's own rate to win over the kernel flag", DataKeyRotationRate, got)
	}
}

// A USB bridge reports a WWN of all zeroes when it passes none through. That is
// the absence of a name, not a name every such drive shares. Captured from a
// TOSHIBA MQ03UBB200 behind a USB bridge.
func TestParseSmartRejectsZeroWWN(t *testing.T) {
	d, err := parseSmart(`{
	  "model_name": "TOSHIBA MQ03UBB200",
	  "wwn": { "naa": 0, "oui": 0, "id": 0 },
	  "smart_status": { "passed": true }
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if d.identity.WWN != "" {
		t.Errorf("WWN = %q, want no name for an all-zero one", d.identity.WWN)
	}
}
