package checks

import (
	"context"
	"fmt"
	"sermo/internal/execx/execxtest"
	"testing"
	"time"
)

func TestLVMCheckHealthTransition(t *testing.T) {
	healthy := `{"report":[{"lv":[{"vg_name":"vg0","lv_name":"root","lv_attr":"-wi-a-----","lv_health_status":"healthy","vg_free":"100","vg_size":"1000"}]}]}`
	partial := `{"report":[{"lv":[{"vg_name":"vg0","lv_name":"root","lv_attr":"-wi-a---p-","lv_health_status":"healthy","vg_free":"100","vg_size":"1000"}]}]}`
	runner := execxtest.Outputs(healthy, partial, healthy)
	check := &lvmCheck{name: "lvm", timeout: time.Second, runner: runner, volumeGroup: "vg0", logicalVolume: "root"}
	if result := check.Run(context.Background()); !result.OK {
		t.Fatalf("healthy result = %+v", result)
	}
	failed := check.Run(context.Background())
	if failed.OK || failed.Data[DataKeyHealth] != LVMHealthError {
		t.Fatalf("partial result = %+v", failed)
	}
	transition, ok := LVMTransitionFromResult(failed)
	if !ok || transition.OldState != LVMHealthOK || transition.NewState != LVMHealthError || transition.Reasons != "partial" {
		t.Fatalf("failure transition = %+v ok=%v", transition, ok)
	}
	recovered := check.Run(context.Background())
	transition, ok = LVMTransitionFromResult(recovered)
	if !recovered.OK || !ok || transition.OldState != LVMHealthError || transition.NewState != LVMHealthOK || transition.PreviousReasons != "partial" {
		t.Fatalf("recovery = %+v transition=%+v ok=%v", recovered, transition, ok)
	}
}

func TestLVMDeviceState(t *testing.T) {
	runDeviceStateCases(t, lvmDeviceState, []deviceStateCase[lvmRow]{
		{name: "idle", in: lvmRow{LVAttr: "-wi-a-----"}},
		{name: "raid check", in: lvmRow{RaidSyncAction: "check", SyncPercent: "12.5"}, wantState: DeviceStateTesting, wantPct: 12.5, wantActive: true},
		{name: "raid recovery", in: lvmRow{RaidSyncAction: "recover", SyncPercent: "50"}, wantState: DeviceStateRecovering, wantPct: 50, wantActive: true},
		{name: "pvmove", in: lvmRow{LVAttr: "pwi-a-----", CopyPercent: "24"}, wantState: DeviceStateMoving, wantPct: 24, wantActive: true},
		{name: "snapshot merge", in: lvmRow{LVAttr: "Swi-a-----"}, wantState: DeviceStateMerging},
		{name: "raid reshape", in: lvmRow{LVAttr: "rwi-a---s-"}, wantState: DeviceStateRebuilding},
	})
}

func TestLVMCheckCapacityPredicate(t *testing.T) {
	data := `{"report":[{"lv":[{"vg_name":"vg0","lv_name":"root","lv_attr":"-wi-a-----","lv_health_status":"healthy","vg_free":"50","vg_size":"1000","data_percent":"85.5","metadata_percent":"81"}]}]}`
	check := &lvmCheck{name: "lvm", timeout: time.Second, runner: execxtest.Outputs(data), volumeGroup: "vg0", logicalVolume: "root", preds: []levelPred{{field: DataKeyLVMFreePct, op: "<", value: 10}}}
	result := check.Run(context.Background())
	if result.OK || result.Data[DataKeyHealth] != LVMHealthWarning || !IsWarning(result.Severity) {
		t.Fatalf("capacity result = %+v", result)
	}
	if got := result.Data[DataKeyVolumeGroup]; got != "vg0" {
		t.Fatalf("volume group = %v, want vg0", got)
	}
	if got := result.Data[DataKeyLogicalVolume]; got != "root" {
		t.Fatalf("logical volume = %v, want root", got)
	}
	if want := "lvm vg0/root health=warning"; result.Message != want {
		t.Fatalf("message = %q, want %q", result.Message, want)
	}
	if got := result.Data[DataKeyLVMFreeBytes]; got != float64(50) {
		t.Fatalf("free bytes = %v, want 50", got)
	}
	if got := result.Data[DataKeyLVMSizeBytes]; got != float64(1000) {
		t.Fatalf("size bytes = %v, want 1000", got)
	}
	if got := result.Data[DataKeyLVMUsedBytes]; got != float64(950) {
		t.Fatalf("used bytes = %v, want 950", got)
	}
}

func TestLVMVolumeGroupCapacityWatchKeepsLogicalVolumeEmpty(t *testing.T) {
	data := `{"report":[{"lv":[{"vg_name":"vg0","lv_name":"root","lv_attr":"-wi-a-----","lv_health_status":"healthy","vg_free":"50","vg_size":"1000"}]}]}`
	check := &lvmCheck{name: "lvm", timeout: time.Second, runner: execxtest.Outputs(data), volumeGroup: "vg0"}
	result := check.Run(context.Background())
	if !result.OK {
		t.Fatalf("result = %+v", result)
	}
	if got := result.Data[DataKeyVolumeGroup]; got != "vg0" {
		t.Fatalf("volume group = %v, want vg0", got)
	}
	if got := result.Data[DataKeyLogicalVolume]; got != "" {
		t.Fatalf("logical volume = %v, want empty for VG capacity watch", got)
	}
	if want := "lvm vg0 health=ok"; result.Message != want {
		t.Fatalf("message = %q, want %q (a VG watch must not name one member LV)", result.Message, want)
	}
}

func TestLVMCapacitySeverity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		attr     string
		field    string
		severity string
		absent   bool
		mixed    bool
		want     string
		warning  bool
	}{
		{name: "zero VG free", attr: "-wi-a-----", field: DataKeyLVMFreePct, want: LVMHealthWarning, warning: true},
		{name: "explicit error", attr: "-wi-a-----", field: DataKeyLVMFreePct, severity: SeverityError, want: LVMHealthWarning},
		{name: "partial outranks headroom", attr: "-wi-a---p-", field: DataKeyLVMFreePct, want: LVMHealthError},
		{name: "suspended outranks headroom", attr: "-wi-s-----", field: DataKeyLVMFreePct, want: LVMHealthError},
		{name: "missing volume", absent: true, field: DataKeyLVMFreePct, want: LVMHealthError},
		{name: "mixed capacity stays error", attr: "twi-a-tz--", field: DataKeyLVMFreePct, mixed: true, want: LVMHealthError},
		{name: "thin data stays error", attr: "twi-a-tz--", field: DataKeyLVMThinDataPct, want: LVMHealthError},
		{name: "thin metadata stays error", attr: "twi-a-tz--", field: DataKeyLVMThinMetadataPct, want: LVMHealthError},
		{name: "explicit warning", attr: "twi-a-tz--", field: DataKeyLVMThinDataPct, severity: SeverityWarning, want: LVMHealthError, warning: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := fmt.Sprintf(`{"report":[{"lv":[{"vg_name":"vg0","lv_name":"root","lv_attr":%q,"vg_free":"0","vg_size":"1000","data_percent":"90","metadata_percent":"90"}]}]}`, tc.attr)
			if tc.absent {
				data = `{"report":[{"lv":[]}]}`
			}
			predicate := levelPred{field: tc.field, op: ">", value: 80}
			if tc.field == DataKeyLVMFreePct {
				predicate.op, predicate.value = "<", 5
			}
			check := &lvmCheck{
				name: "lvm", timeout: time.Second, severity: tc.severity,
				runner: execxtest.Outputs(data), volumeGroup: "vg0", logicalVolume: "root", preds: []levelPred{predicate},
			}
			if tc.mixed {
				check.preds = append(check.preds, levelPred{field: DataKeyLVMThinDataPct, op: ">", value: 80})
			}
			result := check.Run(t.Context())
			if result.OK || result.Data[DataKeyHealth] != tc.want || result.Warning() != tc.warning {
				t.Fatalf("result = %+v, want health=%s warning=%v and failed raw verdict", result, tc.want, tc.warning)
			}
		})
	}
}

func TestLVMWarningEscalatesAndRecovers(t *testing.T) {
	low := `{"report":[{"lv":[{"vg_name":"vg0","lv_name":"root","lv_attr":"-wi-a-----","vg_free":"0","vg_size":"1000"}]}]}`
	partial := `{"report":[{"lv":[{"vg_name":"vg0","lv_name":"root","lv_attr":"-wi-a---p-","vg_free":"0","vg_size":"1000"}]}]}`
	healthy := `{"report":[{"lv":[{"vg_name":"vg0","lv_name":"root","lv_attr":"-wi-a-----","vg_free":"100","vg_size":"1000"}]}]}`
	check := &lvmCheck{
		name: "lvm", timeout: time.Second, runner: execxtest.Outputs(low, partial, low, healthy),
		volumeGroup: "vg0", preds: []levelPred{{field: DataKeyLVMFreePct, op: "<", value: 5}},
	}
	if result := check.Run(t.Context()); !result.Warning() {
		t.Fatalf("initial low headroom = %+v", result)
	}
	for _, tc := range []struct{ old, next string }{
		{LVMHealthWarning, LVMHealthError}, {LVMHealthError, LVMHealthWarning}, {LVMHealthWarning, LVMHealthOK},
	} {
		result := check.Run(t.Context())
		transition, ok := LVMTransitionFromResult(result)
		if !ok || transition.OldState != tc.old || transition.NewState != tc.next {
			t.Fatalf("transition = %+v, want %s -> %s", transition, tc.old, tc.next)
		}
	}
}
