package app

import (
	"testing"

	"sermo/internal/servicemgr"
)

func TestServiceState(t *testing.T) {
	tests := []struct {
		name          string
		enabled       bool
		monitored     bool
		backendStatus string
		checkHealth   string
		observed      bool
		ready         bool
		want          string
	}{
		{name: "disabled", enabled: false, monitored: false, backendStatus: string(servicemgr.StatusActive), observed: true, want: TargetStateDisabled},
		{name: "starting monitored", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusInactive), observed: false, want: TargetStateStarting},
		{name: "started unmonitored", enabled: true, monitored: false, backendStatus: string(servicemgr.StatusActive), observed: true, want: TargetStateStarted},
		{name: "paused unmonitored", enabled: true, monitored: false, backendStatus: string(servicemgr.StatusPaused), observed: true, want: TargetStateStopped},
		{name: "stopped unmonitored", enabled: true, monitored: false, backendStatus: string(servicemgr.StatusInactive), observed: true, want: TargetStateStopped},
		{name: "failed unmonitored", enabled: true, monitored: false, backendStatus: string(servicemgr.StatusFailed), observed: true, want: TargetStateStopped},
		{name: "collecting active healthy without observability", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusActive), checkHealth: TargetStateOK, observed: true, want: TargetStateCollecting},
		{name: "monitored active healthy", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusActive), checkHealth: TargetStateOK, observed: true, ready: true, want: TargetStateMonitored},
		{name: "paused monitored", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusPaused), checkHealth: TargetStateOK, observed: true, want: TargetStateFailed},
		{name: "collecting active unknown checks", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusActive), checkHealth: checkHealthUnknown, observed: true, ready: true, want: TargetStateCollecting},
		{name: "failed backend", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusFailed), observed: true, want: TargetStateFailed},
		{name: "failed checks", enabled: true, monitored: true, backendStatus: string(servicemgr.StatusActive), checkHealth: checkHealthFailing, observed: true, ready: true, want: TargetStateFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ServiceState(tt.enabled, tt.monitored, tt.backendStatus, tt.checkHealth, tt.observed, tt.ready); got != tt.want {
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
		observed  bool
		want      string
	}{
		{name: "disabled", enabled: false, observed: true, want: TargetStateDisabled},
		{name: "starting monitored", enabled: true, monitored: true, observed: false, want: TargetStateStarting},
		{name: "unmonitored ok", enabled: true, monitored: false, observed: true, want: TargetStateDisabled},
		{name: "unmonitored failed", enabled: true, monitored: false, failed: true, observed: true, want: TargetStateDisabled},
		{name: "ok", enabled: true, monitored: true, observed: true, want: TargetStateOK},
		{name: "failed", enabled: true, monitored: true, failed: true, observed: true, want: TargetStateFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WatchState(tt.enabled, tt.monitored, tt.failed, tt.observed); got != tt.want {
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
