package checks

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
)

func TestSkipWhenIdleLeavesTheProbeUnrun(t *testing.T) {
	entry := map[string]any{
		CheckKeyType:         CheckTypeCommand,
		CheckKeyCommand:      []any{"virsh", "net-list"},
		CheckKeySummary:      "listed ${value}",
		CheckKeySkipWhenIdle: []any{"virtqemud", "virtnetworkd"},
	}
	for _, tc := range []struct {
		name      string
		idle      map[string]bool
		wantRun   bool
		wantAsked []string
	}{
		{name: "a named unit is idle", idle: map[string]bool{"virtnetworkd": true}, wantAsked: []string{"virtqemud", "virtnetworkd"}},
		{name: "the first unit is idle", idle: map[string]bool{"virtqemud": true}, wantAsked: []string{"virtqemud"}},
		{name: "no named unit is idle", wantRun: true, wantAsked: []string{"virtqemud", "virtnetworkd"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := execxtest.Fixed(execx.Result{Stdout: "default\n"}, nil)
			var asked []string
			check, err := BuildInline("networks", entry, Deps{
				DefaultTimeout: time.Second,
				Runner:         runner,
				Limiter:        NewLimiter(1),
				UnitIdle: func(_ context.Context, unit string) bool {
					asked = append(asked, unit)
					return tc.idle[unit]
				},
			})
			if err != nil {
				t.Fatalf("BuildInline() error = %v", err)
			}
			res := check.Run(t.Context())
			if !slices.Equal(asked, tc.wantAsked) {
				t.Fatalf("asked units %v, want %v", asked, tc.wantAsked)
			}
			if ran := len(runner.Calls()) > 0; ran != tc.wantRun {
				t.Fatalf("probe ran = %v, want %v (calls %v)", ran, tc.wantRun, runner.Calls())
			}
			if tc.wantRun {
				if res.Skipped || !res.OK {
					t.Fatalf("result = %+v, want the probe's own healthy result", res)
				}
				return
			}
			if res.Check != "networks" || !res.OK || res.Observation() != ObservationSkipped {
				t.Fatalf("result = %+v, want a skipped observation of the named check", res)
			}
			if !strings.Contains(res.Message, " is idle") || strings.Contains(res.Message, "listed") {
				t.Fatalf("message = %q, want the idle reason and no summary of an unread value", res.Message)
			}
		})
	}
}

// The gate sits outside the shared budget: an idle unit is skipped at once even
// when every slot is taken, and its queries never eat the probe's timeout.
func TestSkipWhenIdleNeedsNoLimiterSlot(t *testing.T) {
	limiter := NewLimiter(1)
	if err := limiter.acquire(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer limiter.release()
	check, err := BuildInline("networks", map[string]any{
		CheckKeyType:         CheckTypeCommand,
		CheckKeyCommand:      []any{"virsh", "net-list"},
		CheckKeySkipWhenIdle: "virtnetworkd",
	}, Deps{
		DefaultTimeout: time.Second,
		Runner:         execxtest.Fixed(execx.Result{}, nil),
		Limiter:        limiter,
		UnitIdle:       func(context.Context, string) bool { return true },
	})
	if err != nil {
		t.Fatalf("BuildInline() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if res := Execute(ctx, check); !res.Skipped || res.Unavailable {
		t.Fatalf("result = %+v, want skipped without waiting for a slot", res)
	}
}

func TestSkipWhenIdleWithoutUnitStateRunsTheProbe(t *testing.T) {
	runner := execxtest.Fixed(execx.Result{Stdout: "default\n"}, nil)
	check, err := BuildInline("networks", map[string]any{
		CheckKeyType:         CheckTypeCommand,
		CheckKeyCommand:      []any{"virsh", "net-list"},
		CheckKeySkipWhenIdle: "virtnetworkd",
	}, Deps{DefaultTimeout: time.Second, Runner: runner})
	if err != nil {
		t.Fatalf("BuildInline() error = %v", err)
	}
	if res := check.Run(t.Context()); res.Skipped || len(runner.Calls()) != 1 {
		t.Fatalf("result = %+v after %d calls, want the probe to run once", res, len(runner.Calls()))
	}
}
