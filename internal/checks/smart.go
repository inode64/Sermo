package checks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
)

// smartCheck reads a drive's SMART health and attributes via `smartctl -j`. With
// no predicate it alerts when the overall SMART health verdict is FAILED;
// predicates on `temperature` (°C), `reallocated` (sectors), `wear` (SSD/NVMe
// percentage used) and `power_on_hours` override/augment that. The numeric
// attributes are recorded over time, so a rising reallocated-sector or wear count
// (a failing/aging drive) is visible on the graph. Needs smartmontools (and root).
type smartCheck struct {
	base
	runner execx.Runner
	device string
	preds  []levelPred
}

func (c smartCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	prefix := CheckTypeSmart + " " + c.device
	res, runErr := c.runner.Run(ctx, smartctlCommand, smartctlArgs(c.device)...)
	if res.ExitCode == execx.ExitCodeRunFailure {
		msg := execx.OperatorFailureOr(runErr, res, c.timeout, execx.CommandDidNotStart)
		return c.result(false, prefix+": "+msg, start)
	}
	data, err := parseSmart(res.Stdout)
	if err != nil {
		if s := output.FirstNonEmptyLine(res.Stderr); s != "" {
			return c.result(false, prefix+": "+s, start)
		}
		return c.result(false, prefix+": "+err.Error(), start)
	}

	ok := data.healthKnown && !data.passed // default alert condition: health FAILED
	if len(c.preds) > 0 {
		ok = levelPredsHold(c.preds, data.values)
	}

	health := smartHealthUnknown
	if data.healthKnown {
		if data.passed {
			health = smartHealthPassed
		} else {
			health = smartHealthFailed
		}
	}
	r := c.result(ok, prefix+" health="+health, start)
	r.Data = SmartResultData(c.device, health, data.selfTestRunning, data.values)
	return r
}

// SmartResultData is the persisted reading data for one SMART sample, shared
// by the check cycle and the snapshot-backed watch view.
func SmartResultData(device, health string, selfTestRunning bool, values map[string]float64) map[string]any {
	data := map[string]any{DataKeyDevice: device, DataKeyHealth: health}
	if selfTestRunning {
		data[DataKeyDeviceState] = DeviceStateTesting
	}
	for k, v := range values {
		data[k] = v
	}
	return data
}

// SmartSample is one smartctl observation.
type SmartSample struct {
	Health          string
	HealthKnown     bool
	SelfTestRunning bool
	Values          map[string]float64
}

func smartctlArgs(device string) []string {
	// -c exposes ATA self-test progress; health and attribute readings stay the
	// same. This makes a manually requested test observable on later cycles.
	return []string{"-H", "-A", "-c", "-j", device}
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

// smartData is the parsed subset of `smartctl -j` output.
type smartData struct {
	passed          bool
	healthKnown     bool
	selfTestRunning bool
	values          map[string]float64 // temperature, reallocated, wear, power_on_hours
}

// parseSmart extracts the health verdict and the graphable attributes from
// smartctl's JSON (ATA and NVMe shapes).
func parseSmart(out string) (smartData, error) {
	if strings.TrimSpace(out) == "" {
		return smartData{}, errors.New("no smartctl output")
	}
	var j struct {
		SmartStatus *struct {
			Passed bool `json:"passed"`
		} `json:"smart_status"`
		Temperature struct {
			Current *float64 `json:"current"`
		} `json:"temperature"`
		PowerOnTime struct {
			Hours *float64 `json:"hours"`
		} `json:"power_on_time"`
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
					Value  *int   `json:"value"`
					String string `json:"string"`
				} `json:"status"`
			} `json:"self_test"`
		} `json:"ata_smart_data"`
		NVMe struct {
			PercentageUsed *float64 `json:"percentage_used"`
		} `json:"nvme_smart_health_information_log"`
	}
	if err := json.Unmarshal([]byte(out), &j); err != nil {
		return smartData{}, fmt.Errorf("invalid smartctl JSON: %w", err)
	}

	d := smartData{values: map[string]float64{}}
	if j.SmartStatus != nil {
		d.passed, d.healthKnown = j.SmartStatus.Passed, true
	}
	if j.Temperature.Current != nil {
		d.values[fieldTemperature] = *j.Temperature.Current
	}
	if j.PowerOnTime.Hours != nil {
		d.values[fieldPowerOnHours] = *j.PowerOnTime.Hours
	}
	for _, a := range j.AtaAttrs.Table {
		if a.ID == 5 && a.Raw.Value != nil { // Reallocated_Sector_Ct
			d.values[fieldReallocated] = *a.Raw.Value
		}
	}
	if j.NVMe.PercentageUsed != nil {
		d.values[fieldWear] = *j.NVMe.PercentageUsed
	}
	d.selfTestRunning = smartSelfTestRunning(j.AtaSmartData.SelfTest.Status.Value, j.AtaSmartData.SelfTest.Status.String)
	return d, nil
}

// smartSelfTestRunning recognises both smartctl's stable JSON status text and
// the ATA status low nibble (0xf means a self-test is in progress). The numeric
// form keeps the result reliable when a smartctl version localises its text.
func smartSelfTestRunning(value *int, status string) bool {
	if value != nil && *value&0x0f == 0x0f {
		return true
	}
	return strings.Contains(strings.ToLower(status), "in progress")
}

// parseSmartPreds reads the optional temperature/reallocated/wear/power_on_hours
// predicates.
