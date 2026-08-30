package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/state"
	"sermo/internal/volume"
)

type deadlineStateMaintainer struct {
	*fakeStore
	deadline time.Time
}

func (s *deadlineStateMaintainer) CompactHistory(ctx context.Context, _, _ time.Time) (state.MaintainResult, error) {
	s.deadline, _ = ctx.Deadline()
	return state.MaintainResult{}, nil
}

type deadlineVolumeExpander struct {
	deadline time.Time
}

func (e *deadlineVolumeExpander) ExpandPath(ctx context.Context, _ string, _ int64) (volume.Result, error) {
	e.deadline, _ = ctx.Deadline()
	return volume.Result{}, nil
}

func assertTimeoutDeadline(t *testing.T, deadline, started, finished time.Time, want time.Duration) {
	t.Helper()
	if deadline.IsZero() {
		t.Fatal("dependency context has no deadline")
	}
	if earliest, latest := started.Add(want), finished.Add(want); deadline.Before(earliest) || deadline.After(latest) {
		t.Fatalf("dependency deadline = %s, want a %s timeout (window %s..%s)", deadline, want, earliest, latest)
	}
}

func TestWebBackendCompactStateTimeoutFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name             string
		operationTimeout time.Duration
		defaultTimeout   time.Duration
		want             time.Duration
	}{
		{name: "operation timeout wins", operationTimeout: 3 * time.Second, defaultTimeout: 2 * time.Second, want: 3 * time.Second},
		{name: "check timeout is the secondary fallback", defaultTimeout: 2 * time.Second, want: 2 * time.Second},
		{name: "operation default is the final fallback", want: DefaultEngineOperationTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &deadlineStateMaintainer{fakeStore: newFakeStore()}
			backend := &WebBackend{
				store:            store,
				operationTimeout: tc.operationTimeout,
				defaultTimeout:   tc.defaultTimeout,
			}
			started := time.Now()
			result := backend.CompactState(t.Context(), time.Time{})
			finished := time.Now()
			if !result.OK {
				t.Fatalf("CompactState() = %+v, want success", result)
			}
			assertTimeoutDeadline(t, store.deadline, started, finished, tc.want)
		})
	}
}

func TestWebBackendExpandWatchTimeoutFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name             string
		operationTimeout time.Duration
		want             time.Duration
	}{
		{name: "configured operation timeout", operationTimeout: 3 * time.Second, want: 3 * time.Second},
		{name: "operation default fallback", want: DefaultEngineOperationTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expander := &deadlineVolumeExpander{}
			backend := &WebBackend{
				watches: map[string]*webWatch{
					"storage-data": {
						checkType: checks.CheckTypeStorage,
						check:     map[string]any{checks.CheckKeyPath: "/data"},
						expand:    &ExpandSpec{By: 1},
					},
				},
				expander:         expander,
				operationTimeout: tc.operationTimeout,
			}
			started := time.Now()
			result := backend.ExpandWatch(t.Context(), "storage-data")
			finished := time.Now()
			if !result.OK {
				t.Fatalf("ExpandWatch() = %+v, want success", result)
			}
			assertTimeoutDeadline(t, expander.deadline, started, finished, tc.want)
		})
	}
}
