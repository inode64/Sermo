package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
	"sermo/internal/severity"
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
	res, err := b.probeDiskIORates(context.Background(), check, time.Second)
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
	if _, err := b.probeDiskIORates(context.Background(), gone, time.Second); err != nil {
		t.Fatalf("probeDiskIORates: %v", err)
	}
	if gone.calls != 1 {
		t.Errorf("unavailable device sampled %d times, want 1", gone.calls)
	}
}

// The check timeout bounds each sample, not the rate window between them: a
// timeout no longer than the window (engine.default_timeout: 2s against the 2s
// window) used to fail every manual probe with "context deadline exceeded".
func TestProbeDiskIOWindowOutlivesCheckTimeout(t *testing.T) {
	b := &WebBackend{diskIOWindow: 50 * time.Millisecond}
	check := &countingProbeCheck{results: []checks.Result{
		{Check: "diskio-sdd", Message: "diskio sdd baseline"},
		{Check: "diskio-sdd", OK: true, Message: "diskio sdd util 0.0%", Data: map[string]any{checks.DataKeyDevice: "sdd"}},
	}}
	res, err := b.probeDiskIORates(context.Background(), check, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("probeDiskIORates: %v, want the window to run outside the sample timeout", err)
	}
	if check.calls != 2 || !res.OK {
		t.Fatalf("calls=%d result=%+v, want the rate sample", check.calls, res)
	}
	for i, deadline := range check.deadlines {
		if deadline.IsZero() || time.Until(deadline) > 10*time.Millisecond {
			t.Fatalf("sample %d deadline %v, want each sample bounded by the check timeout", i, deadline)
		}
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
	results   []checks.Result
	calls     int
	deadlines []time.Time
}

func (c *countingProbeCheck) Name() string { return "probe" }
func (c *countingProbeCheck) Run(ctx context.Context) checks.Result {
	deadline, _ := ctx.Deadline()
	c.deadlines = append(c.deadlines, deadline)
	res := c.results[min(c.calls, len(c.results)-1)]
	c.calls++
	return res
}

func TestProbeContextWithUnsetTimeoutIsBounded(t *testing.T) {
	b := &WebBackend{}
	ctx, cancel := b.probeContext(context.Background(), nil)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > DefaultEngineCheckTimeout {
		t.Fatalf("unexpected probe deadline: %v, present=%v", deadline, ok)
	}
	cancel()
	if ctx.Err() != context.Canceled {
		t.Fatalf("cancel: %v", ctx.Err())
	}
}

// A watch that declares no severity lets its check grade its own finding. The
// manual probe must hand the check that same absence, not the watch's resolved
// error default, or a controller with an advisory reads as an outage only when
// probed.
func TestManualProbeLetsTheCheckGradeItsOwnFinding(t *testing.T) {
	const storcli = "/usr/bin/storcli64"
	envelope := func(data string) string {
		return `{"Controllers":[{"Command Status":{"Controller":0,"Status":"Success"},"Response Data":` + data + `}]}`
	}
	runner := &execxtest.Runner{ByLine: map[string]execx.Result{
		storcli + " /call show all J": {Stdout: envelope(`{"Status":{"Controller Status":"Optimal"}}`)},
		storcli + " /call/eall/sall show all J": {Stdout: envelope(`{
		  "Drive /c0/e252/s0": [{"EID:Slt":"252:0","State":"Onln"}],
		  "Drive /c0/e252/s0 - Detailed Information": {"Drive /c0/e252/s0 State": {"Media Error Count": 2}}}`)},
		storcli + " /call/vall show all J": {Stdout: envelope(`{"/c0/v0":[{"DG/VD":"0/0","State":"Optl"}]}`)},
	}, Default: execx.Result{ExitCode: 1}}
	w, warn := newWebWatch("raid", map[string]any{
		config.WatchKeyCheck: map[string]any{checks.CheckKeyType: checks.CheckTypeStorCLI, checks.CheckKeyBinary: storcli},
	}, nil, time.Minute, false)
	if warn != "" {
		t.Fatal(warn)
	}
	b := &WebBackend{defaultTimeout: 5 * time.Second, execRunner: runner}

	result, err := b.probeWatchResult(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || result.Severity != severity.Warning {
		t.Fatalf("probe ok=%v severity=%q, want a warning: %s", result.OK, result.Severity, result.Message)
	}
}
