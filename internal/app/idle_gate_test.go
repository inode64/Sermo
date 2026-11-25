package app

import (
	"context"
	"testing"

	"sermo/internal/control"
	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
	"sermo/internal/servicemgr"
)

// idleDaemonRunner scripts a socket-activated virtnetworkd that is stopped and
// waiting for its socket.
func idleDaemonRunner() *execxtest.Runner {
	return &execxtest.Runner{ByLine: map[string]execx.Result{
		"systemctl is-active -- virtnetworkd.service":                   {Stdout: "inactive\n", ExitCode: 3},
		"systemctl show -p TriggeredBy --value -- virtnetworkd.service": {Stdout: "virtnetworkd.socket\n"},
		"systemctl is-active -- virtnetworkd.socket":                    {Stdout: "active\n"},
	}}
}

type triggerWaitingManager struct {
	servicemgr.Manager
	waits bool
	asked *int
}

func (m triggerWaitingManager) WaitsForTrigger(context.Context) bool {
	*m.asked++
	return m.waits
}

// The manager is asked once and before the live read: Status asks it again, and
// a daemon woken between two reads must not turn an idle network into inactive.
func TestIdleUnitStatusAsksAManagerThatWaitsOnAnotherUnit(t *testing.T) {
	for _, tc := range []struct {
		live     servicemgr.Status
		waits    bool
		want     servicemgr.Status
		wantLive bool
	}{
		{live: servicemgr.StatusInactive, waits: true, want: servicemgr.StatusIdle},
		{live: servicemgr.StatusInactive, want: servicemgr.StatusInactive, wantLive: true},
		{live: servicemgr.StatusActive, want: servicemgr.StatusActive, wantLive: true},
		{live: servicemgr.StatusFailed, want: servicemgr.StatusFailed, wantLive: true},
	} {
		asked, reads := 0, 0
		runner := &execxtest.Runner{}
		deps := Deps{
			Backend:     servicemgr.BackendLibvirtNetwork,
			Manager:     triggerWaitingManager{waits: tc.waits, asked: &asked},
			ExecxRunner: runner,
		}
		status := idleUnitStatus(deps, "default", func(context.Context) (servicemgr.Status, error) {
			reads++
			return tc.live, nil
		})
		if got, err := status(t.Context()); err != nil || got != tc.want {
			t.Errorf("live %s, waits %v: status = %s, %v, want %s", tc.live, tc.waits, got, err, tc.want)
		}
		if asked != 1 || (reads > 0) != tc.wantLive || len(runner.Calls()) != 0 {
			t.Errorf("live %s, waits %v: asked %d, live reads %d, unit queries %v; want 1, live %v, none",
				tc.live, tc.waits, asked, reads, runner.Calls(), tc.wantLive)
		}
	}
}

// A control target replaces Backend with its own manager's; skip_when_idle names
// a unit of the host's init system, which the checks must still be able to judge.
func TestSkipWhenIdleJudgesUnitsOnTheHostInitSystem(t *testing.T) {
	deps := Deps{Backend: servicemgr.BackendSystemd, ExecxRunner: idleDaemonRunner()}
	asked := 0
	target := control.Target{
		Unit:        "default",
		Backend:     servicemgr.BackendLibvirtNetwork,
		Manager:     triggerWaitingManager{asked: &asked},
		BackendPIDs: func() []int { return []int{42} },
	}
	serviceDeps := WithControlTarget(deps, target)
	if serviceDeps.Backend != target.Backend || serviceDeps.Manager != target.Manager || serviceDeps.BackendPIDs()[0] != 42 {
		t.Fatalf("WithControlTarget kept the host's backend, manager or PIDs: %+v", serviceDeps)
	}
	runtime := BuildServiceRuntime(t.Context(), ServiceRuntimeConfig{Service: "libvirt-net-default", Unit: "default", Deps: serviceDeps, Tree: map[string]any{}})
	if !runtime.CheckDeps.UnitIdle(t.Context(), "virtnetworkd") {
		t.Fatal("service checks behind a control target lost the host's init system")
	}
	if !watchInlineDeps(deps).UnitIdle(t.Context(), "virtnetworkd") {
		t.Fatal("host watch checks cannot judge an idle unit")
	}
	replaced := deps
	replaced.Backend = servicemgr.BackendLibvirtNetwork
	if hostUnitIdle(replaced)(t.Context(), "virtnetworkd") {
		t.Fatal("a non-systemd backend judged a unit idle")
	}
}
