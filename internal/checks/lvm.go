package checks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"sermo/internal/execx"
	"sermo/internal/output"
)

const (
	lvsCommand = "lvs"
	// LVMHealthOK is the normalised healthy state exposed by the LVM check.
	LVMHealthOK = "ok"
	// LVMHealthError is the normalised failing state exposed by the LVM check.
	LVMHealthError = "error"
	// LVMNotifyOnChange is the state-transition selector for LVM watches.
	LVMNotifyOnChange = "on_change"
)

// LVMTransition carries the effective health-state change for a LVM watch.
type LVMTransition struct{ OldState, NewState, PreviousReasons, Reasons string }

type lvmCheck struct {
	base
	runner                          execx.Runner
	volumeGroup, logicalVolume      string
	preds                           []levelPred
	primed                          bool
	previousHealth, previousReasons string
}

type lvmReport struct {
	Report []struct {
		LV []lvmRow `json:"lv"`
	} `json:"report"`
}
type lvmRow struct {
	VGName          string `json:"vg_name"`
	LVName          string `json:"lv_name"`
	LVAttr          string `json:"lv_attr"`
	LVHealth        string `json:"lv_health_status"`
	VGFree          string `json:"vg_free"`
	VGSize          string `json:"vg_size"`
	DataPercent     string `json:"data_percent"`
	MetadataPercent string `json:"metadata_percent"`
}

func (c *lvmCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	res, runErr := c.runner.Run(ctx, lvsCommand, "--reportformat", "json", "--units", "b", "--nosuffix", "-o", "vg_name,lv_name,lv_attr,lv_health_status,vg_free,vg_size,data_percent,metadata_percent")
	if res.ExitCode == execx.ExitCodeRunFailure {
		msg := execx.OperatorFailure(runErr, res, c.timeout)
		if msg == "" {
			msg = execx.CommandDidNotStart
		}
		return c.result(false, "lvm: "+msg, start)
	}
	var report lvmReport
	if err := json.Unmarshal([]byte(res.Stdout), &report); err != nil {
		if line := output.FirstNonEmptyLine(res.Stderr); line != "" {
			return c.result(false, "lvm: "+line, start)
		}
		return c.result(false, "lvm: parse lvs JSON: "+err.Error(), start)
	}
	row, found := c.selectRow(report)
	if !found {
		return c.finish(start, LVMHealthError, "absent", map[string]float64{}, "lvm target absent")
	}
	values := lvmValues(row)
	reasons := lvmReasons(row)
	if len(c.preds) > 0 && levelPredsHold(c.preds, values) {
		reasons = append(reasons, "capacity_threshold")
	}
	health := LVMHealthOK
	if len(reasons) > 0 {
		health = LVMHealthError
	}
	message := fmt.Sprintf("lvm %s/%s health=%s", row.VGName, row.LVName, health)
	return c.finish(start, health, strings.Join(reasons, ","), values, message)
}

func (c *lvmCheck) selectRow(report lvmReport) (lvmRow, bool) {
	for _, group := range report.Report {
		for _, row := range group.LV {
			if c.volumeGroup != "" && row.VGName != c.volumeGroup {
				continue
			}
			if c.logicalVolume != "" && row.LVName != c.logicalVolume {
				continue
			}
			return row, true
		}
	}
	return lvmRow{}, false
}

func (c *lvmCheck) finish(start time.Time, health, reasons string, values map[string]float64, message string) Result {
	r := c.result(health == LVMHealthOK, message, start)
	r.Data = map[string]any{DataKeyHealth: health, DataKeyLVMReasons: reasons, DataKeyVolumeGroup: c.volumeGroup, DataKeyLogicalVolume: c.logicalVolume}
	for key, value := range values {
		r.Data[key] = value
	}
	if c.primed && health != c.previousHealth {
		r.Data["lvm_transition"] = LVMTransition{OldState: c.previousHealth, NewState: health, PreviousReasons: c.previousReasons, Reasons: reasons}
	}
	c.primed, c.previousHealth, c.previousReasons = true, health, reasons
	return r
}

func lvmValues(row lvmRow) map[string]float64 {
	values := map[string]float64{}
	if free, ok := parseLVMNumber(row.VGFree); ok {
		if size, ok := parseLVMNumber(row.VGSize); ok && size > 0 {
			values[DataKeyLVMFreePct] = free / size * 100
		}
	}
	if value, ok := parseLVMNumber(row.DataPercent); ok {
		values[DataKeyLVMThinDataPct] = value
	}
	if value, ok := parseLVMNumber(row.MetadataPercent); ok {
		values[DataKeyLVMThinMetadataPct] = value
	}
	return values
}

func parseLVMNumber(value string) (float64, bool) {
	value = strings.TrimSpace(value)
	if value == "" || value == "-" {
		return 0, false
	}
	var number float64
	if _, err := fmt.Sscan(value, &number); err != nil {
		return 0, false
	}
	return number, true
}

func lvmReasons(row lvmRow) []string {
	var reasons []string
	attr := row.LVAttr
	if strings.Contains(attr, "p") {
		reasons = append(reasons, "partial")
	}
	if strings.Contains(attr, "s") {
		reasons = append(reasons, "suspended")
	}
	if status := strings.TrimSpace(row.LVHealth); status != "" && status != "healthy" {
		reasons = append(reasons, status)
	}
	return reasons
}

// LVMTransitionFromResult returns the state change, when this sample crossed it.
func LVMTransitionFromResult(result Result) (LVMTransition, bool) {
	transition, ok := result.Data["lvm_transition"].(LVMTransition)
	return transition, ok
}
