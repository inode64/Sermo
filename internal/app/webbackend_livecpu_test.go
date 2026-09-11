package app

import (
	"testing"

	"sermo/internal/web"
)

func TestAttachLiveCPUWithoutProcessTotals(t *testing.T) {
	live := NewLiveMetrics()
	live.Publish("service", ServiceLive{CPUReady: true, CPU: 12, NumCPU: 4})
	detail := web.Detail{}
	attachLiveCPU(&detail, live, "service")
	if detail.ProcessTotals != nil || len(detail.Processes) != 0 {
		t.Fatalf("processless detail gained process data: %+v", detail)
	}
}

func TestAttachLiveCPUWithProcessTotals(t *testing.T) {
	live := NewLiveMetrics()
	live.Publish("service", ServiceLive{CPUReady: true, CPU: 12, NumCPU: 4})
	detail := web.Detail{ProcessTotals: &web.ProcessTotals{Count: 1}}
	attachLiveCPU(&detail, live, "service")
	if !detail.ProcessTotals.HasCPU || detail.ProcessTotals.CPU != 12 || detail.ProcessTotals.NumCPU != 4 {
		t.Fatalf("live totals = %+v", detail.ProcessTotals)
	}
}
