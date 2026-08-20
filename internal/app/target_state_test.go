package app

import (
	"testing"

	"sermo/internal/servicemgr"
)

func TestServiceState(t *testing.T) {
	tests := []struct {
		name             string
		enabled          bool
		monitored        bool
		backendStatus    string
		checkHealth      string
		observed         bool
		ready            bool
		processActive    bool
		processesMissing bool
		backendDegraded  bool
		want             string
	}{
		{name: "disabled", enabled: false, monitored: false, backendStatus: string(servicemgr.StatusActive), observed: true, want: TargetStateDisabled},
		{name: "starting monitored", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusInactive), observed: false, want: TargetStateStarting},
		{name: "started unmonitored", enabled: true, monitored: false, backendStatus: string(servicemgr.StatusActive), observed: true, want: TargetStateStarted},
		{name: "paused unmonitored", enabled: true, monitored: false, backendStatus: string(servicemgr.StatusPaused), observed: true, want: TargetStateStopped},
		{name: "stopped unmonitored", enabled: true, monitored: false, backendStatus: string(servicemgr.StatusInactive), observed: true, want: TargetStateStopped},
		{name: "failed unmonitored", enabled: true, monitored: false, backendStatus: string(servicemgr.StatusFailed), observed: true, want: TargetStateStopped},
		{name: "collecting active healthy without observability", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusActive), checkHealth: TargetStateOK, observed: true, want: TargetStateCollecting},
		{name: "active trusted process without observability", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusActive), checkHealth: TargetStateOK, observed: true, processActive: true, want: TargetStateActive},
		{name: "active trusted process before observed cycle", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusActive), processActive: true, want: TargetStateActive},
		{name: "monitored active healthy", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusActive), checkHealth: TargetStateOK, observed: true, ready: true, want: TargetStateMonitored},
		{name: "paused monitored", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusPaused), checkHealth: TargetStateOK, observed: true, want: TargetStateFailed},
		{name: "collecting active unknown checks", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusActive), checkHealth: checkHealthUnknown, observed: true, ready: true, want: TargetStateCollecting},
		{name: "failed backend", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusFailed), observed: true, want: TargetStateFailed},
		{name: "failed backend with verified live workload warns", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusFailed), checkHealth: checkHealthWarning, observed: true, processActive: true, backendDegraded: true, want: TargetStateWarning},
		{name: "failed checks", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusActive), checkHealth: checkHealthFailing, observed: true, ready: true, want: TargetStateFailed},
		{name: "warning checks", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusActive), checkHealth: checkHealthWarning, observed: true, ready: true, want: TargetStateWarning},
		{name: "warning active healthy with no attributable process", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusActive), checkHealth: TargetStateOK, observed: true, processesMissing: true, want: TargetStateWarning},
		{name: "trusted process outranks a stale empty tree", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusActive), checkHealth: TargetStateOK, observed: true, processActive: true, processesMissing: true, want: TargetStateActive},
		{name: "full observability outranks an empty tree", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusActive), checkHealth: TargetStateOK, observed: true, ready: true, processesMissing: true, want: TargetStateMonitored},
		{name: "startup settling still wins over an empty tree", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusActive), checkHealth: TargetStateOK, observed: false, processesMissing: true, want: TargetStateStarting},
		{name: "failed checks still win over an empty tree", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusActive), checkHealth: checkHealthFailing, observed: true, processesMissing: true, want: TargetStateFailed},
		// An unreadable backend status is not an outage. FireHOL's init script
		// overrides `status` with its own report, so rc-service answers
		// "unknown" for a service rc-status lists as started and whose every
		// check passes; calling that failed invented an outage.
		{name: "unknown backend status is not failed", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusUnknown), checkHealth: TargetStateOK, observed: true, ready: true, want: TargetStateCollecting},
		{name: "unknown backend status with trusted process reads active", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusUnknown), checkHealth: TargetStateOK, observed: true, ready: true, processActive: true, want: TargetStateActive},
		{name: "unknown backend status unmonitored stays stopped", enabled: true, monitored: false, backendStatus: string(servicemgr.StatusUnknown), observed: true, want: TargetStateStopped},
		// Inactive stays failed: that is the backend answering, not declining.
		{name: "inactive monitored still fails", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusInactive), checkHealth: TargetStateOK, observed: true, ready: true, want: TargetStateFailed},
		// Declining to answer must not swallow the checks' own verdict: an
		// unreadable status plus a failing required check is still an outage.
		{name: "unknown backend status with failing checks still fails", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusUnknown), checkHealth: checkHealthFailing, observed: true, ready: true, want: TargetStateFailed},
		{name: "unknown backend status with failing checks fails even with a live process", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusUnknown), checkHealth: checkHealthFailing, observed: true, ready: true, processActive: true, want: TargetStateFailed},
		{name: "unknown backend status with warning checks warns", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusUnknown), checkHealth: checkHealthWarning, observed: true, ready: true, want: TargetStateWarning},
		// Healthy checks under an unreadable status never claim "monitored".
		{name: "unknown backend status never reads monitored", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusUnknown), checkHealth: TargetStateOK, observed: true, ready: true, processesMissing: true, want: TargetStateCollecting},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ServiceState(tt.enabled, tt.monitored, tt.backendStatus, tt.checkHealth, tt.observed, tt.ready, tt.processActive, tt.processesMissing, tt.backendDegraded); got != tt.want {
				t.Fatalf("ServiceState() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWatchState(t *testing.T) {
	tests := []struct {
		name      string
		enabled   bool
		monitored bool
		failed    bool
		warning   bool
		observed  bool
		want      string
	}{
		{name: "disabled", enabled: false, observed: true, want: TargetStateDisabled},
		{name: "starting monitored", enabled: true, monitored: true, observed: false, want: TargetStateStarting},
		{name: "unmonitored ok", enabled: true, monitored: false, observed: true, want: TargetStateDisabled},
		{name: "unmonitored failed", enabled: true, monitored: false, failed: true, observed: true, want: TargetStateDisabled},
		{name: "ok", enabled: true, monitored: true, observed: true, want: TargetStateOK},
		{name: "failed", enabled: true, monitored: true, failed: true, observed: true, want: TargetStateFailed},
		{name: "warning", enabled: true, monitored: true, warning: true, observed: true, want: TargetStateWarning},
		// An outage outranks an advisory: a net watch whose error counter warns
		// while its link is down must read failed, not warning.
		{name: "failed outranks warning", enabled: true, monitored: true, failed: true, warning: true, observed: true, want: TargetStateFailed},
		{name: "unmonitored warning", enabled: true, monitored: false, warning: true, observed: true, want: TargetStateDisabled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WatchState(tt.enabled, tt.monitored, tt.failed, tt.warning, tt.observed); got != tt.want {
				t.Fatalf("WatchState() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWatchActivityFailed(t *testing.T) {
	tests := []struct {
		kind string
		want bool
	}{
		{kind: eventKindFiring, want: true},
		{kind: eventKindHookFail, want: true},
		{kind: eventKindNotifyFail, want: true},
		{kind: eventKindExpandFailed, want: true},
		{kind: eventKindRecovered},
		{kind: eventKindHook},
		{kind: eventKindNotify},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			if got := WatchActivityFailed(tt.kind); got != tt.want {
				t.Fatalf("WatchActivityFailed(%q) = %v, want %v", tt.kind, got, tt.want)
			}
		})
	}
}
