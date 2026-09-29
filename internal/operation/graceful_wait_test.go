package operation

import (
	"context"
	"errors"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/process"
)

func TestStopGracefulWait(t *testing.T) {
	for _, tc := range []struct {
		name         string
		exitAfter    time.Duration
		uncertain    bool
		discoveryErr bool
		wantWait     time.Duration
		wantStatus   ResultStatus
	}{
		{name: "backend already stopped process", wantStatus: ResultOK},
		{name: "process exits during grace", exitAfter: time.Second, wantWait: time.Second, wantStatus: ResultOK},
		{name: "survivor exhausts grace", exitAfter: time.Minute, wantWait: 2 * time.Second, wantStatus: ResultOrphanProcesses},
		{name: "unknown absence never shortens grace", uncertain: true, wantWait: 2 * time.Second, wantStatus: ResultFailed},
		{name: "incomplete discovery fails closed", discoveryErr: true, wantStatus: ResultFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := defaultHarness()
			e := h.engine()
			e.Lifecycle.ProcessMode = config.ServiceProcessResident
			e.KillPolicy.GracefulTimeout = 2 * time.Second
			var slept time.Duration
			live := []process.Process{{PID: 100, StartTicks: 10}}
			e.Sleep = func(d time.Duration) { slept += d }
			e.Discover = func() ([]process.Process, error) {
				if !h.mgr.stopped || slept < tc.exitAfter {
					return live, nil
				}
				return nil, nil
			}
			e.ObserveProcesses = func() (process.Observation, error) {
				if h.mgr.stopped && tc.discoveryErr {
					return process.Observation{}, errors.New("incomplete process table")
				}
				procs, err := e.Discover()
				return process.Observation{Processes: procs, Trusted: len(procs) > 0, IdentityRequired: true, AbsenceKnown: !tc.uncertain}, err
			}
			result := e.Stop(t.Context())
			if result.Status != tc.wantStatus || slept != tc.wantWait {
				t.Fatalf("result=%+v waited=%v, want status=%s waited=%v", result, slept, tc.wantStatus, tc.wantWait)
			}
			if len(h.emitted) != 1 || h.released != 1 || h.mgr.did("start mysqld") {
				t.Fatalf("events=%d releases=%d calls=%v", len(h.emitted), h.released, h.mgr.calls)
			}
		})
	}
}

func TestRestartCleanStopDoesNotConsumeDatabaseGrace(t *testing.T) {
	h := defaultHarness()
	e := h.engine()
	e.Lifecycle.ProcessMode = config.ServiceProcessResident
	e.KillPolicy.GracefulTimeout = 120 * time.Second
	e.OperationTimeout = 3 * time.Minute
	e.ObserveProcesses = func() (process.Observation, error) {
		if h.mgr.stopped {
			return process.Observation{AbsenceKnown: true, IdentityRequired: true}, nil
		}
		return process.Observation{Trusted: true, IdentityRequired: true, Processes: []process.Process{{PID: 100}}}, nil
	}
	var slept time.Duration
	e.Sleep = func(d time.Duration) { slept += d }
	result := e.Restart(t.Context())
	if result.Status != ResultOK || !h.mgr.did("start mysqld") {
		t.Fatalf("result=%+v calls=%v", result, h.mgr.calls)
	}
	want := time.Duration(postflightMaxAttempts-1) * postflightRetryInterval
	if slept != want {
		t.Fatalf("slept %v, want only postflight settling %v", slept, want)
	}
}

func TestProcessFreeStopWaitsForInit(t *testing.T) {
	h := defaultHarness()
	h.mgr.stopLeavesActive = true
	e := h.engine()
	e.Lifecycle.ProcessMode = config.ServiceProcessNone
	e.KillPolicy.GracefulTimeout = time.Second
	e.ObserveProcesses = func() (process.Observation, error) { return process.Observation{AbsenceKnown: true}, nil }
	sleeps := 0
	e.Sleep = func(time.Duration) {
		sleeps++
		h.mgr.stopped = true
	}
	result := e.Stop(t.Context())
	if result.Status != ResultOK || sleeps != 1 {
		t.Fatalf("result=%+v sleeps=%d, want one wait for init to stop", result, sleeps)
	}
}

func TestRestartCancelledWhileObservingGrace(t *testing.T) {
	h := defaultHarness()
	e := h.engine()
	e.KillPolicy.GracefulTimeout = time.Hour
	e.ObserveProcesses = func() (process.Observation, error) {
		return process.Observation{Processes: []process.Process{{PID: 100}}}, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e.Sleep = func(time.Duration) { cancel() }
	result := e.Restart(ctx)
	if result.Status != ResultFailed || result.Message != "operation cancelled during graceful stop wait" {
		t.Fatalf("result=%+v", result)
	}
	if h.mgr.did("start mysqld") || len(h.emitted) != 1 || h.released != 1 {
		t.Fatalf("calls=%v events=%d releases=%d", h.mgr.calls, len(h.emitted), h.released)
	}
}
