package checks

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"sermo/internal/execx"
	"sermo/internal/output"
)

const (
	smartctlCommand    = "smartctl"
	smartctlShortTest  = "--test=short"
	smartHealthUnknown = "unknown"
	smartHealthPassed  = "PASSED"
	smartHealthFailed  = "FAILED"
	// smartFailureUnknown stands in when smartctl produced no sample and named
	// no reason, so the operator still gets a message instead of a blank one.
	smartFailureUnknown = "smartctl produced no usable report"
	// smartMessageSeverityError is the severity smartctl stamps on the messages
	// that explain why a report carries no reading.
	smartMessageSeverityError = "error"
	// smartSelfTestHoursFormat appends the drive's own lifetime hour to a
	// self-test verdict, which is what makes "completed without error" mean
	// "recently" rather than "at some point in the last nine years".
	smartSelfTestHoursFormat = "%s at %s h"
)

// ATA SMART attribute ids Sermo reads raw values from, in smartmontools'
// vocabulary. Pending sectors are sectors the drive could not read and has not
// yet been able to reallocate — the count that rises *before* `reallocated`
// does — and CRC errors count corrupted transfers on the link itself, which
// blames the cable or the backplane rather than the media.
const (
	smartAttrReallocatedSectorCt  = 5
	smartAttrCurrentPendingSector = 197
	smartAttrUDMACRCErrorCount    = 199
)

// smartctl exit-status bits, from smartctl(8) RETURN VALUES. Only these two mean
// smartctl produced no reading at all; every higher bit is a SMART verdict
// carried by an otherwise valid report and must not void the sample.
const (
	smartExitCommandLine = 1 << 0 // command line did not parse
	smartExitDeviceOpen  = 1 << 1 // open failed, or no IDENTIFY DEVICE answer
)

// smartSelfTestRunningNibble is the low nibble the ATA self-test status byte
// carries while a test is in progress.
const smartSelfTestRunningNibble = 0x0f

// smartCheck reads a drive's SMART health and attributes via `smartctl -j`. With
// no predicate it alerts when the overall SMART health verdict is FAILED;
// predicates on `temperature` (°C), `reallocated`, `pending_sectors`,
// `crc_errors` and `media_errors` (counts), `wear` (SSD/NVMe percentage used)
// and `power_on_hours` are independent early-warning conditions that augment
// that verdict. The numeric attributes are
// recorded over time, so a rising reallocated-sector or wear count (a failing/
// aging drive) is visible on the graph. Each report also carries what the drive
// *is* — model, serial number, firmware, capacity — so the dashboard names the
// disk an operator has to pull rather than only its device node.
//
// A device smartctl cannot open or identify is reported missing and unavailable,
// never as a verdictless sample. Such a report still identifies the drive from
// sysfs and republishes the last readings the drive did answer with, because
// that is precisely when an operator needs them. Needs smartmontools (and root).
type smartCheck struct {
	base
	runner         execx.Runner
	device         string
	preds          []levelPred
	deviceBus      BlockDeviceBusFunc
	deviceIdentity BlockDeviceIdentityFunc
	last           *lastSample
}

func (c *smartCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start

	prefix := CheckTypeSmart + " " + c.device
	res, runErr := c.runner.Run(ctx, smartctlCommand, smartctlArgs(c.device)...)
	if res.ExitCode == execx.ExitCodeRunFailure {
		msg := execx.OperatorFailureOr(runErr, res, c.timeout, execx.CommandDidNotStart)
		return c.unreadableResult(prefix+": "+msg, start)
	}
	data, err := parseSmart(res.Stdout)
	if err != nil {
		if s := output.FirstNonEmptyLine(res.Stderr); s != "" {
			return c.unreadableResult(prefix+": "+s, start)
		}
		return c.unreadableResult(prefix+": "+err.Error(), start)
	}

	// smartctl still emits well-formed JSON when it could not sample the drive:
	// the report carries an error message and an exit status in place of a
	// verdict. Reading only the verdict would turn a drive that fell off its bus
	// into a healthy, verdictless sample, so the envelope is classified first.
	if data.deviceUnreadable {
		r := c.missingDeviceResult(c.deviceIdentity, prefix, c.device, start)
		r.Data = c.withLastKnown(r.Data, start)
		return r
	}
	if data.usageError {
		return c.unreadableResult(prefix+": "+smartctlFailure(data.failure, res.Stderr), start)
	}

	ok := data.healthKnown && !data.passed // default alert condition: health FAILED
	if len(c.preds) > 0 {
		ok = ok || anyLevelPredHolds(c.preds, data.values)
	}

	health := smartHealthUnknown
	if data.healthKnown {
		if data.passed {
			health = smartHealthPassed
		} else {
			health = smartHealthFailed
		}
	}
	c.last.record(health, data.values, start)
	data.identity.Rotation = cmp.Or(data.identity.Rotation, c.sysfsRotation())
	r := c.result(ok, prefix+" health="+health, start)
	r.Data = withDeviceBus(SmartResultData(c.device, health, data.SmartSample), c.deviceBus, c.device)
	return r
}

// sysfsRotation is the kernel's answer to what medium the drive uses, for the
// drives whose own report does not say. NVMe publishes no rotation rate at all,
// so without this fallback a flash drive would describe itself less completely
// than a platter disk does. It is read only when smartctl left the field empty.
func (c *smartCheck) sysfsRotation() string {
	return resolveDeviceIdentity(c.deviceIdentity, c.device).Rotation
}

// unreadableResult is the unavailable observation for a smartctl run that
// produced no sample without proving the device gone. It still identifies the
// drive and republishes its last known readings: the operator's question after
// "smartctl failed" is which disk, and what it looked like before.
func (c *smartCheck) unreadableResult(message string, start time.Time) Result {
	r := c.unavailableResult(message, start)
	r.Data = c.withLastKnown(map[string]any{DataKeyDevice: c.device}, start)
	return r
}

// withLastKnown completes a failed sample with everything Sermo still knows
// about the drive: the identity sysfs keeps publishing after the device stops
// answering, and the newest readings this check managed to take.
func (c *smartCheck) withLastKnown(data map[string]any, start time.Time) map[string]any {
	data = withIdentity(data, resolveDeviceIdentity(c.deviceIdentity, c.device))
	return c.last.into(data, start)
}

// SmartResultData is the persisted reading data for one SMART sample, shared
// by the check cycle and the snapshot-backed watch view.
func SmartResultData(device, health string, sample SmartSample) map[string]any {
	data := map[string]any{DataKeyDevice: device, DataKeyHealth: health}
	if sample.selfTestRunning {
		data[DataKeyDeviceState] = DeviceStateTesting
		if sample.selfTestProgressPct > 0 {
			data[DataKeyProgressPct] = sample.selfTestProgressPct
		}
	}
	if sample.selfTest != "" {
		data[DataKeySelfTest] = sample.selfTest
	}
	for k, v := range sample.values {
		data[k] = v
	}
	return withIdentity(data, sample.identity)
}

func smartctlArgs(device string) []string {
	// -i is what names the hardware (model, serial, firmware, capacity); -c
	// exposes self-test progress and -l selftest the drive's own verdict on the
	// last test it ran. -H and -A remain the health and attribute readings.
	return []string{"-i", "-H", "-A", "-c", "-l", "selftest", "-j", device}
}

// StartSmartShortTest asks a device to begin its built-in SMART short self-test.
// The command normally returns after scheduling the test; callers must not treat
// that acknowledgement as a new SMART-health verdict.
func StartSmartShortTest(ctx context.Context, runner execx.Runner, device string, timeout time.Duration) error {
	runner = execx.RunnerOrDefault(runner)
	res, runErr := runner.Run(ctx, smartctlCommand, smartctlShortTestArgs(device)...)
	if res.ExitCode == execx.ExitCodeSuccess {
		return nil
	}
	if msg := execx.OperatorFailure(runErr, res, timeout); msg != "" {
		return errors.New(msg)
	}
	if msg := output.FirstNonEmptyLine(res.Stderr); msg != "" {
		return errors.New(msg)
	}
	if msg := output.FirstNonEmptyLine(res.Stdout); msg != "" {
		return errors.New(msg)
	}
	return fmt.Errorf("smartctl %s exited with code %d", smartctlShortTest, res.ExitCode)
}

func smartctlShortTestArgs(device string) []string {
	return []string{smartctlShortTest, device}
}

// SmartSample is the parsed subset of `smartctl -j` output that describes the
// drive: its identity, its readings and the state of its self-test.
type SmartSample struct {
	identity        BlockDeviceIdentity
	values          map[string]float64
	selfTest        string
	selfTestRunning bool
	// selfTestProgressPct is how much of a running self-test is done, when the
	// drive reports how much of it remains.
	selfTestProgressPct float64
}

// smartData is one parsed report: a sample plus the envelope that says whether
// the sample means anything.
type smartData struct {
	SmartSample
	passed      bool
	healthKnown bool
	// deviceUnreadable means smartctl could not open the device or it returned
	// no identification — the drive is gone, not merely unhealthy.
	deviceUnreadable bool
	// usageError means smartctl rejected the command line Sermo built for it.
	usageError bool
	// failure is smartctl's own first error-severity message, when it gave one.
	failure string
}

// smartReport is the shape of the smartctl JSON Sermo reads. smartctl 7.x
// normalizes the fields that mean the same thing on every transport
// (temperature, power-on time, power cycles, endurance) to the top level, so
// most readings need no ATA/NVMe branching at all.
type smartReport struct {
	Smartctl struct {
		ExitStatus *int              `json:"exit_status"`
		Messages   []smartctlMessage `json:"messages"`
	} `json:"smartctl"`
	ModelName       string `json:"model_name"`
	SerialNumber    string `json:"serial_number"`
	FirmwareVersion string `json:"firmware_version"`
	// RotationRate is the drive's own answer in rpm, with 0 meaning solid
	// state. NVMe drives omit the field entirely rather than reporting 0.
	RotationRate *int `json:"rotation_rate"`
	WWN          struct {
		NAA *int64 `json:"naa"`
		OUI *int64 `json:"oui"`
		ID  *int64 `json:"id"`
	} `json:"wwn"`
	UserCapacity struct {
		Bytes *float64 `json:"bytes"`
	} `json:"user_capacity"`
	NVMeTotalCapacity *float64 `json:"nvme_total_capacity"`
	SmartStatus       *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	Temperature struct {
		Current *float64 `json:"current"`
	} `json:"temperature"`
	PowerOnTime struct {
		Hours *float64 `json:"hours"`
	} `json:"power_on_time"`
	PowerCycleCount *float64 `json:"power_cycle_count"`
	EnduranceUsed   struct {
		CurrentPercent *float64 `json:"current_percent"`
	} `json:"endurance_used"`
	AtaAttrs struct {
		Table []struct {
			ID  int `json:"id"`
			Raw struct {
				Value *float64 `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	AtaSmartData struct {
		SelfTest struct {
			Status struct {
				Value            *int     `json:"value"`
				String           string   `json:"string"`
				RemainingPercent *float64 `json:"remaining_percent"`
			} `json:"status"`
		} `json:"self_test"`
	} `json:"ata_smart_data"`
	AtaSelfTestLog struct {
		Standard struct {
			Table []smartAtaSelfTestEntry `json:"table"`
		} `json:"standard"`
	} `json:"ata_smart_self_test_log"`
	NVMe struct {
		PercentageUsed *float64 `json:"percentage_used"`
		MediaErrors    *float64 `json:"media_errors"`
	} `json:"nvme_smart_health_information_log"`
	NVMeSelfTestLog struct {
		Current struct {
			Value  *int   `json:"value"`
			String string `json:"string"`
		} `json:"current_self_test_operation"`
		Table []smartNVMeSelfTestEntry `json:"table"`
	} `json:"nvme_self_test_log"`
}

// smartAtaSelfTestEntry is one row of an ATA drive's own self-test log.
type smartAtaSelfTestEntry struct {
	Status struct {
		String string `json:"string"`
	} `json:"status"`
	LifetimeHours *float64 `json:"lifetime_hours"`
}

// smartNVMeSelfTestEntry is one row of an NVMe drive's own self-test log.
type smartNVMeSelfTestEntry struct {
	SelfTestResult struct {
		String string `json:"string"`
	} `json:"self_test_result"`
	PowerOnHours *float64 `json:"power_on_hours"`
}

// parseSmart extracts the health verdict, the drive's identity and the graphable
// attributes from smartctl's JSON (ATA and NVMe shapes).
func parseSmart(out string) (smartData, error) {
	if strings.TrimSpace(out) == "" {
		return smartData{}, errors.New("no smartctl output")
	}
	var j smartReport
	if err := json.Unmarshal([]byte(out), &j); err != nil {
		return smartData{}, fmt.Errorf("invalid smartctl JSON: %w", err)
	}

	d := smartData{failure: smartctlErrorMessage(j.Smartctl.Messages),
		values: map[string]float64{}}
	if status := j.Smartctl.ExitStatus; status != nil {
		d.deviceUnreadable = *status&smartExitDeviceOpen != 0
		d.usageError = *status&smartExitCommandLine != 0
	}
	if j.SmartStatus != nil {
		d.passed, d.healthKnown = j.SmartStatus.Passed, true
	}
	d.identity = j.identity()
	j.readInto(d.values)
	d.selfTest = j.selfTestResult()
	d.selfTestRunning, d.selfTestProgressPct = j.selfTestProgress()
	return d, nil
}

// identity names the physical drive the report came from. NVMe spells its
// capacity twice, so the namespace total stands in when the user-capacity block
// is absent.
func (j smartReport) identity() BlockDeviceIdentity {
	id := BlockDeviceIdentity{
		Model:    j.ModelName,
		Serial:   j.SerialNumber,
		Firmware: j.FirmwareVersion,
		WWN:      j.wwn(),
		Rotation: j.rotation(),
	}
	if bytes := cmpFirst(j.UserCapacity.Bytes, j.NVMeTotalCapacity); bytes != nil && *bytes > 0 {
		id.CapacityBytes = uint64(*bytes)
	}
	return id
}

// rotation renders what kind of drive this is. smartctl reports the exact rate
// a platter drive spins at, which sysfs never knows, so a SMART sample always
// describes the medium more precisely than the fallback does.
func (j smartReport) rotation() string {
	switch {
	case j.RotationRate == nil:
		return ""
	case *j.RotationRate <= 0:
		return rotationSolidState
	default:
		return fmt.Sprintf(rotationRPMFormat, *j.RotationRate)
	}
}

// wwn renders the drive's World Wide Name the way smartctl prints it and
// /dev/disk/by-id spells it, so the value can be matched against a real path.
// A USB bridge that passes no WWN through fills the field with zeroes rather
// than omitting it, so an all-zero name is reported as no name at all: it
// identifies nothing, and every such drive would otherwise share it.
func (j smartReport) wwn() string {
	if j.WWN.NAA == nil || j.WWN.OUI == nil || j.WWN.ID == nil {
		return ""
	}
	if *j.WWN.NAA == 0 && *j.WWN.OUI == 0 && *j.WWN.ID == 0 {
		return ""
	}
	return fmt.Sprintf("0x%x%06x%09x", *j.WWN.NAA, *j.WWN.OUI, *j.WWN.ID)
}

// readInto collects every numeric reading the report carries. The ATA attribute
// table and the NVMe health log are alternative sources for the same concepts:
// a drive answers one of them, never both, so no precedence is needed.
func (j smartReport) readInto(values map[string]float64) {
	for _, reading := range [...]struct {
		field string
		value *float64
	}{
		{fieldTemperature, j.Temperature.Current},
		{fieldPowerOnHours, j.PowerOnTime.Hours},
		{fieldPowerCycles, j.PowerCycleCount},
		{fieldWear, cmpFirst(j.EnduranceUsed.CurrentPercent, j.NVMe.PercentageUsed)},
		{fieldMediaErrors, j.NVMe.MediaErrors},
	} {
		if reading.value != nil {
			values[reading.field] = *reading.value
		}
	}
	for _, a := range j.AtaAttrs.Table {
		field, ok := smartAtaAttrFields[a.ID]
		if ok && a.Raw.Value != nil {
			values[field] = *a.Raw.Value
		}
	}
}

// smartAtaAttrFields maps the ATA attribute ids Sermo graphs to their reading
// names. NVMe drives have no attribute table; their equivalents are read from
// the health log.
var smartAtaAttrFields = map[int]string{
	smartAttrReallocatedSectorCt:  fieldReallocated,
	smartAttrCurrentPendingSector: fieldPendingSectors,
	smartAttrUDMACRCErrorCount:    fieldCRCErrors,
}

// selfTestResult renders the drive's verdict on the last self-test it completed,
// with the lifetime hour it ran at so "completed without error" carries a date.
// The ATA capability block is the fallback: it always states the last result,
// but never says when.
func (j smartReport) selfTestResult() string {
	if len(j.AtaSelfTestLog.Standard.Table) > 0 {
		last := j.AtaSelfTestLog.Standard.Table[0]
		return smartSelfTestSummary(last.Status.String, last.LifetimeHours)
	}
	if len(j.NVMeSelfTestLog.Table) > 0 {
		last := j.NVMeSelfTestLog.Table[0]
		return smartSelfTestSummary(last.SelfTestResult.String, last.PowerOnHours)
	}
	return j.AtaSmartData.SelfTest.Status.String
}

// smartSelfTestSummary joins a self-test verdict with the drive hour it ran at.
func smartSelfTestSummary(status string, hours *float64) string {
	if status == "" || hours == nil {
		return status
	}
	return fmt.Sprintf(smartSelfTestHoursFormat, status, strconv.FormatFloat(*hours, 'f', -1, numericBits64))
}

// selfTestProgress reports whether a self-test is running and how far it has
// got. It recognises smartctl's stable JSON status text, the ATA status low
// nibble (0xf means in progress) and the NVMe log's current-operation code. The
// numeric forms keep the result reliable when a smartctl version localises its
// text, and the NVMe form is the only one an NVMe drive publishes at all.
func (j smartReport) selfTestProgress() (bool, float64) {
	status := j.AtaSmartData.SelfTest.Status
	running := smartSelfTestRunning(status.Value, status.String) ||
		(j.NVMeSelfTestLog.Current.Value != nil && *j.NVMeSelfTestLog.Current.Value != 0)
	if !running || status.RemainingPercent == nil {
		return running, 0
	}
	return running, percentScale - *status.RemainingPercent
}

// smartSelfTestRunning reads the ATA self-test status, numerically first.
func smartSelfTestRunning(value *int, status string) bool {
	if value != nil && *value&smartSelfTestRunningNibble == smartSelfTestRunningNibble {
		return true
	}
	return strings.Contains(strings.ToLower(status), "in progress")
}

// cmpFirst returns the first reading a report actually carries, for the values
// two transports spell with two different field names.
func cmpFirst(values ...*float64) *float64 {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

// smartctlMessage is one diagnostic line smartctl attaches to a JSON report.
type smartctlMessage struct {
	String   string `json:"string"`
	Severity string `json:"severity"`
}

// smartctlErrorMessage returns smartctl's first error-severity message. It is
// the operator-facing reason a report carries no reading ("Smartctl open device:
// /dev/sda failed: INQUIRY failed"), which stderr does not repeat under -j.
func smartctlErrorMessage(messages []smartctlMessage) string {
	for _, m := range messages {
		if strings.EqualFold(m.Severity, smartMessageSeverityError) {
			return m.String
		}
	}
	return ""
}

// smartctlFailure picks the most specific description of a smartctl run that
// produced no sample: its own JSON message first, then its stderr.
func smartctlFailure(reported, stderr string) string {
	return cmp.Or(reported, output.FirstNonEmptyLine(stderr), smartFailureUnknown)
}
