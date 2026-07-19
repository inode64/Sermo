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
	LVMNotifyOnChange       = "on_change"
	lvmLVAttrTypeIndex      = 0
	lvmLVAttrSuspendedIndex = 4
	lvmLVAttrHealthIndex    = 8
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
	RaidSyncAction  string `json:"raid_sync_action"`
	SyncPercent     string `json:"sync_percent"`
	CopyPercent     string `json:"copy_percent"`
}

func (c *lvmCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	res, runErr := c.runner.Run(ctx, lvsCommand, "--reportformat", "json", "--units", "b", "--nosuffix", "-a", "-o", "vg_name,lv_name,lv_attr,lv_health_status,vg_free,vg_size,data_percent,metadata_percent,raid_sync_action,sync_percent,copy_percent")
	if res.ExitCode == execx.ExitCodeRunFailure {
		msg := execx.OperatorFailureOr(runErr, res, c.timeout, execx.CommandDidNotStart)
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
		return c.finish(start, lvmRow{}, LVMHealthError, "absent", map[string]float64{}, "lvm target absent")
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
	return c.finish(start, row, health, strings.Join(reasons, ","), values, message)
}

func (c *lvmCheck) selectRow(report lvmReport) (lvmRow, bool) {
	for i := range report.Report {
		for j := range report.Report[i].LV {
			if c.volumeGroup != "" && report.Report[i].LV[j].VGName != c.volumeGroup {
				continue
			}
			if c.logicalVolume != "" && report.Report[i].LV[j].LVName != c.logicalVolume {
				continue
			}
			return report.Report[i].LV[j], true
		}
	}
	return lvmRow{}, false
}

func (c *lvmCheck) finish(start time.Time, row lvmRow, health, reasons string, values map[string]float64, message string) Result {
	r := c.result(health == LVMHealthOK, message, start)
	vg, lv := c.resultTarget(row)
	r.Data = map[string]any{DataKeyHealth: health, DataKeyLVMReasons: reasons, DataKeyVolumeGroup: vg, DataKeyLogicalVolume: lv}
	if state, progress, hasProgress := lvmDeviceState(row); state != "" {
		r.Data[DataKeyDeviceState] = state
		if hasProgress {
			r.Data[DataKeyProgressPct] = progress
		}
	}
	for key, value := range values {
		r.Data[key] = value
	}
	if c.primed && health != c.previousHealth {
		r.Data[DataKeyLVMTransition] = LVMTransition{OldState: c.previousHealth, NewState: health, PreviousReasons: c.previousReasons, Reasons: reasons}
	}
	c.primed, c.previousHealth, c.previousReasons = true, health, reasons
	return r
}

func (c *lvmCheck) resultTarget(row lvmRow) (string, string) {
	vg := c.volumeGroup
	if vg == "" {
		vg = row.VGName
	}
	lv := c.logicalVolume
	if row.LVName != "" && (c.logicalVolume != "" || c.volumeGroup == "") {
		lv = row.LVName
	}
	return vg, lv
}

func lvmValues(row lvmRow) map[string]float64 {
	values := map[string]float64{}
	if free, ok := parseLVMNumber(row.VGFree); ok {
		values[DataKeyLVMFreeBytes] = free
		if size, ok := parseLVMNumber(row.VGSize); ok && size > 0 {
			values[DataKeyLVMSizeBytes] = size
			if used := size - free; used >= 0 {
				values[DataKeyLVMUsedBytes] = used
			}
			values[DataKeyLVMFreePct] = free / size * percentScale
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
	if lvmAttributeAt(row.LVAttr, lvmLVAttrHealthIndex) == 'p' {
		reasons = append(reasons, "partial")
	}
	if lvmAttributeAt(row.LVAttr, lvmLVAttrSuspendedIndex) == 's' {
		reasons = append(reasons, "suspended")
	}
	if status := strings.TrimSpace(row.LVHealth); status != "" && status != "healthy" {
		reasons = append(reasons, status)
	}
	return reasons
}

// lvmDeviceState maps documented lvs activity fields to one concise device
// state. It leaves ordinary healthy, idle volumes blank so their watch state
// remains the monitoring health state.
func lvmDeviceState(row lvmRow) (string, float64, bool) {
	switch strings.ToLower(strings.TrimSpace(row.RaidSyncAction)) {
	case raidSyncActionCheck:
		return lvmProgressState(DeviceStateTesting, row)
	case raidSyncActionRepair:
		return lvmProgressState(DeviceStateRepairing, row)
	case raidSyncActionRecover, raidSyncActionResync:
		return lvmProgressState(DeviceStateRecovering, row)
	case raidSyncActionReshape:
		return lvmProgressState(DeviceStateRebuilding, row)
	}

	switch lvmAttributeAt(row.LVAttr, lvmLVAttrTypeIndex) {
	case 'p':
		return lvmProgressState(DeviceStateMoving, row)
	case 'O', 'S':
		return lvmProgressState(DeviceStateMerging, row)
	case 'M', 'R':
		return lvmProgressState(DeviceStateRebuilding, row)
	}
	if lvmAttributeAt(row.LVAttr, lvmLVAttrHealthIndex) == 's' {
		return lvmProgressState(DeviceStateRebuilding, row)
	}
	return "", 0, false
}

func lvmProgressState(state string, row lvmRow) (string, float64, bool) {
	for _, value := range []string{row.SyncPercent, row.CopyPercent} {
		if progress, ok := parseLVMNumber(value); ok {
			return state, progress, true
		}
	}
	return state, 0, false
}

func lvmAttributeAt(attr string, index int) byte {
	if index < 0 || index >= len(attr) {
		return 0
	}
	return attr[index]
}

// LVMTransitionFromResult returns the state change, when this sample crossed it.
func LVMTransitionFromResult(result Result) (LVMTransition, bool) {
	transition, ok := result.Data[DataKeyLVMTransition].(LVMTransition)
	return transition, ok
}
