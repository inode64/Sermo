package operation

import (
	"context"
	"errors"
	"testing"
	"time"

	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

func TestResetStoppedWaitsForFreshAbsenceProof(t *testing.T) {
	for _, tc := range []struct {
		name       string
		persistent bool
		survivor   bool
		readError  bool
		cancel     bool
		wantError  bool
	}{
		{name: "transient unreadable executable"},
		{name: "persistent uncertainty", persistent: true, wantError: true},
		{name: "process appears during retry", survivor: true, wantError: true},
		{name: "next snapshot fails", readError: true, wantError: true},
		{name: "cancel during retry", cancel: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := defaultHarness()
			h.mgr.status = servicemgr.StatusInactive
			e := h.engine()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls, waits int
			e.Sleep = func(time.Duration) {
				waits++
				if tc.cancel {
					cancel()
				}
			}
			e.ObserveProcesses = func() (process.Observation, error) {
				calls++
				if calls > 1 && tc.readError {
					return process.Observation{}, errors.New("snapshot unavailable")
				}
				ids := map[int]process.Identity{}
				if calls == 1 || tc.persistent {
					ids[100] = process.Identity{PID: 100, UID: 0, PPID: 1}
				} else if tc.survivor {
					ids[100] = process.Identity{PID: 100, UID: 0, PPID: 1, Exe: "/opt/daemon", ExeOK: true}
				}
				d := process.Discoverer{
					Reader:      &countingPIDReader{ids: ids},
					ResolveUser: func(string) (uint32, bool) { return 0, true },
				}
				return d.Observe([]process.Selector{{Name: process.RoleMain, Type: process.SelectorCommandMatch, Exe: "/opt/daemon", User: "root"}})
			}
			err := e.resetStopped(ctx, true)
			if (err != nil) != tc.wantError || h.mgr.did("reset mysqld") == tc.wantError {
				t.Fatalf("error=%v calls=%v, want error=%v", err, h.mgr.calls, tc.wantError)
			}
			if waits == 0 || waits >= postflightMaxAttempts || calls > postflightMaxAttempts {
				t.Fatalf("absence retries must be bounded: observations=%d waits=%d", calls, waits)
			}
			if tc.cancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}
