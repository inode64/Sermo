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
	if result := check.Run(context.Background()); result.OK || result.Data[DataKeyHealth] != LVMHealthError {
		t.Fatalf("capacity result = %+v", result)
	}
}
