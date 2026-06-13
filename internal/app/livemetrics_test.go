package app

import "testing"

func TestLiveMetrics(t *testing.T) {
	// A nil registry is a safe no-op (callers need not nil-check).
	var nilReg *LiveMetrics
	nilReg.Publish("svc", ServiceLive{CPU: 1})
	if _, ok := nilReg.Get("svc"); ok {
		t.Fatal("nil registry Get must report absent")
	}

	l := NewLiveMetrics()
	if _, ok := l.Get("svc"); ok {
		t.Fatal("an unsampled service must be absent")
	}

	l.Publish("svc", ServiceLive{
		CPU: 12.5, CPUReady: true, CPUThread: 90, NumCPU: 4,
		PerProcCPU: map[int]float64{10: 50},
	})
	got, ok := l.Get("svc")
	if !ok {
		t.Fatal("published service must be present")
	}
	if got.CPU != 12.5 || !got.CPUReady || got.CPUThread != 90 || got.NumCPU != 4 || got.PerProcCPU[10] != 50 {
		t.Fatalf("Get = %+v", got)
	}
	if got.At.IsZero() {
		t.Error("Publish must stamp the observation time")
	}

	// A later Publish replaces the prior sample.
	l.Publish("svc", ServiceLive{CPU: 99})
	if got, _ := l.Get("svc"); got.CPU != 99 || got.CPUReady {
		t.Fatalf("second publish did not replace: %+v", got)
	}
}
