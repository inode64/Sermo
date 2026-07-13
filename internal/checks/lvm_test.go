package checks

import (
	"context"
	"testing"
	"time"

	"sermo/internal/execx"
)

type lvmRunner struct {
	outputs []string
	calls   int
}

func (r *lvmRunner) Run(context.Context, string, ...string) (execx.Result, error) {
	output := r.outputs[min(r.calls, len(r.outputs)-1)]
	r.calls++
	return execx.Result{Stdout: output}, nil
}

func TestLVMCheckHealthTransition(t *testing.T) {
	healthy := `{"report":[{"lv":[{"vg_name":"vg0","lv_name":"root","lv_attr":"-wi-a-----","lv_health_status":"healthy","vg_free":"100","vg_size":"1000"}]}]}`
	partial := `{"report":[{"lv":[{"vg_name":"vg0","lv_name":"root","lv_attr":"pwi-a-----","lv_health_status":"healthy","vg_free":"100","vg_size":"1000"}]}]}`
	runner := &lvmRunner{outputs: []string{healthy, partial, healthy}}
	check := &lvmCheck{base: base{name: "lvm", timeout: time.Second}, runner: runner, volumeGroup: "vg0", logicalVolume: "root"}
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

func TestLVMCheckCapacityPredicate(t *testing.T) {
	data := `{"report":[{"lv":[{"vg_name":"vg0","lv_name":"root","lv_attr":"-wi-a-----","lv_health_status":"healthy","vg_free":"50","vg_size":"1000","data_percent":"85.5","metadata_percent":"81"}]}]}`
	check := &lvmCheck{base: base{name: "lvm", timeout: time.Second}, runner: &lvmRunner{outputs: []string{data}}, volumeGroup: "vg0", logicalVolume: "root", preds: []levelPred{{field: DataKeyLVMFreePct, op: "<", value: 10}}}
	result := check.Run(context.Background())
	if result.OK || result.Data[DataKeyHealth] != LVMHealthError {
		t.Fatalf("capacity result = %+v", result)
	}
	if got := result.Data[DataKeyVolumeGroup]; got != "vg0" {
		t.Fatalf("volume group = %v, want vg0", got)
	}
	if got := result.Data[DataKeyLogicalVolume]; got != "root" {
		t.Fatalf("logical volume = %v, want root", got)
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
	check := &lvmCheck{base: base{name: "lvm", timeout: time.Second}, runner: &lvmRunner{outputs: []string{data}}, volumeGroup: "vg0"}
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
}
