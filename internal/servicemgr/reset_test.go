package servicemgr

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
)

func TestSystemdResetStateChecksForFailedMarker(t *testing.T) {
	const query = "systemctl show -p ActiveState --value -- exim.service"
	const reset = "systemctl reset-failed -- exim.service"
	for _, tc := range []struct {
		name     string
		state    string
		queryErr error
		resetErr error
		wantErr  string
		wantRuns int
	}{
		{name: "unloaded after clean stop", state: "inactive\n", resetErr: errors.New("Unit exim.service not loaded"), wantRuns: 1},
		{name: "failed marker", state: "failed\n", wantRuns: 2},
		{name: "active rate limit reset", state: "active\n", wantRuns: 2},
		{name: "empty state", wantErr: "indeterminate active state", wantRuns: 1},
		{name: "transition", state: "deactivating\n", wantErr: "indeterminate active state", wantRuns: 1},
		{name: "query denied", queryErr: errors.New("permission denied"), wantErr: "permission denied", wantRuns: 1},
		{name: "query timeout", queryErr: context.DeadlineExceeded, wantErr: "query state before resetting", wantRuns: 1},
		{name: "reset denied", state: "failed\n", resetErr: errors.New("permission denied"), wantErr: "permission denied", wantRuns: 2},
		{name: "reset timeout", state: "failed\n", resetErr: context.DeadlineExceeded, wantErr: "reset-failed", wantRuns: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &execxtest.Runner{
				ByLine: map[string]execx.Result{query: {Stdout: tc.state}},
				Errs:   map[string]error{query: tc.queryErr, reset: tc.resetErr},
			}
			err := (systemdManager{runner: runner}).ResetState(context.Background(), "exim")
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("ResetState() = %v, want error containing %q", err, tc.wantErr)
			}
			calls := runner.Lines()
			if len(calls) != tc.wantRuns || calls[0] != query || len(calls) == 2 && calls[1] != reset {
				t.Fatalf("calls = %v, want %d calls: query then reset only when needed", calls, tc.wantRuns)
			}
		})
	}
}
