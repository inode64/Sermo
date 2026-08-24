package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
)

// The check's own `timeout:` is the operator's declared budget for that probe. It
// wins in the daemon cycle, so it must win when the same check is run from the
// dashboard's "Probe now" — a buffered hdparm read of a spinning disk needs more
// than engine.default_timeout, and clamping it there failed every manual probe of
// a watch whose scheduled cycle succeeded.
func TestProbeTimeoutPrefersTheChecksOwnBudget(t *testing.T) {
	b := &WebBackend{defaultTimeout: 10 * time.Second, operationTimeout: 90 * time.Second}

	declared := map[string]any{
		checks.CheckKeyType:    checks.CheckTypeHdparm,
		checks.CheckKeyTimeout: "30s",
	}
	if got := b.probeTimeout(declared); got != 30*time.Second {
		t.Errorf("probeTimeout() = %s, want the check's own 30s", got)
	}

	// A check that declares none still falls back to engine.default_timeout.
	if got := b.probeTimeout(map[string]any{checks.CheckKeyType: checks.CheckTypeLVM}); got != 10*time.Second {
		t.Errorf("probeTimeout() = %s, want the 10s engine default", got)
	}

	// With no engine default either, the operation timeout is the last resort.
	bare := &WebBackend{operationTimeout: 90 * time.Second}
	if got := bare.probeTimeout(nil); got != 90*time.Second {
		t.Errorf("probeTimeout() = %s, want the 90s operation timeout", got)
	}

	// The deadline the context carries must be the one reported on expiry —
	// the mismatch is what made a probe say "timeout after 30s" ten seconds in.
	ctx, cancel := b.probeContext(context.Background(), declared)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("probeContext() set no deadline")
	}
	if remaining := time.Until(deadline); remaining <= 10*time.Second {
		t.Errorf("probe deadline in %s, want more than the 10s engine default", remaining)
	}
}

// A disk I/O sample is the delta between two readings of cumulative counters, so
// one run only baselines. Probing must open a window of its own or the operator
// gets "baseline" and no rates — which is what a manual probe used to answer.
func TestProbeDiskIOSamplesTwiceForARateWindow(t *testing.T) {
	b := &WebBackend{diskIOWindow: time.Millisecond}
	check := &countingProbeCheck{results: []checks.Result{
		{Check: "diskio-sdd", Message: "diskio sdd baseline"},
		{Check: "diskio-sdd", OK: true, Message: "diskio sdd util 0.0% read 0 B/s write 0 B/s await 0.0ms",
			Data: map[string]any{checks.DataKeyDevice: "sdd"}},
	}}
	res, err := b.probeDiskIORates(context.Background(), check)
	if err != nil {
		t.Fatalf("probeDiskIORates: %v", err)
	}
	if check.calls != 2 {
		t.Errorf("check ran %d times, want 2: one baseline and one rate sample", check.calls)
	}
	if strings.Contains(res.Message, "baseline") || res.Data == nil {
		t.Errorf("result = %+v, want the second sample's rates", res)
	}

	// A device that cannot be sampled at all is reported straight away rather
	// than waited on for a window that will never mean anything.
	gone := &countingProbeCheck{results: []checks.Result{{Check: "diskio-sdd", Unavailable: true, Message: "diskio sdd: missing"}}}
	if _, err := b.probeDiskIORates(context.Background(), gone); err != nil {
		t.Fatalf("probeDiskIORates: %v", err)
	}
	if gone.calls != 1 {
		t.Errorf("unavailable device sampled %d times, want 1", gone.calls)
	}
}

// The web dashboard and sermoctl gate the probe command on one list, so neither
// can offer a probe the other refuses.
func TestManualProbeCheckTypeCoversDiskIO(t *testing.T) {
	for _, typ := range []string{
		checks.CheckTypeDiskIO, checks.CheckTypeHdparm, checks.CheckTypeLVM, checks.CheckTypeRAID,
		checks.CheckTypeSmart, checks.CheckTypeStorCLI, checks.CheckTypeSSACLI,
	} {
		if !ManualProbeCheckType(typ) {
			t.Errorf("ManualProbeCheckType(%q) = false, want true", typ)
		}
	}
	if ManualProbeCheckType(checks.CheckTypeStorage) {
		t.Error("ManualProbeCheckType(storage) = true, want false")
	}
}

type countingProbeCheck struct {
	results []checks.Result
	calls   int
}

func (c *countingProbeCheck) Name() string { return "probe" }
func (c *countingProbeCheck) Run(context.Context) checks.Result {
	res := c.results[min(c.calls, len(c.results)-1)]
	c.calls++
	return res
}
