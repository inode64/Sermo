package app

import (
	"context"
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
