package app

import "testing"

func TestServiceState(t *testing.T) {
	tests := []struct {
		name          string
		enabled       bool
		monitored     bool
		backendStatus string
		checkHealth   string
		observed      bool
		want          string
	}{
		{name: "disabled", enabled: false, monitored: false, backendStatus: "active", observed: true, want: TargetStateDisabled},
		{name: "starting monitored", enabled: true, monitored: true, backendStatus: "inactive", observed: false, want: TargetStateStarting},
		{name: "running unmonitored", enabled: true, monitored: false, backendStatus: "active", observed: true, want: TargetStateRunning},
		{name: "paused unmonitored", enabled: true, monitored: false, backendStatus: "paused", observed: true, want: TargetStatePaused},
		{name: "stopped unmonitored", enabled: true, monitored: false, backendStatus: "inactive", observed: true, want: TargetStateStopped},
		{name: "failed unmonitored", enabled: true, monitored: false, backendStatus: "failed", observed: true, want: TargetStateFailed},
		{name: "running active healthy", enabled: true, monitored: true, backendStatus: "active", checkHealth: "ok", observed: true, want: TargetStateRunning},
		{name: "paused monitored", enabled: true, monitored: true, backendStatus: "paused", checkHealth: "ok", observed: true, want: TargetStatePaused},
		{name: "running active unknown checks", enabled: true, monitored: true, backendStatus: "active", checkHealth: "unknown", observed: true, want: TargetStateRunning},
		{name: "failed backend", enabled: true, monitored: true, backendStatus: "failed", observed: true, want: TargetStateFailed},
		{name: "failed checks", enabled: true, monitored: true, backendStatus: "active", checkHealth: "failing", observed: true, want: TargetStateFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ServiceState(tt.enabled, tt.monitored, tt.backendStatus, tt.checkHealth, tt.observed); got != tt.want {
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
		{name: "unmonitorized ok", enabled: true, monitored: false, observed: true, want: TargetStateOK},
		{name: "unmonitorized failed", enabled: true, monitored: false, failed: true, observed: true, want: TargetStateFailed},
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
		{kind: "firing", want: true},
		{kind: "hook-failed", want: true},
		{kind: "notify-failed", want: true},
		{kind: "expand-failed", want: true},
		{kind: "recovered"},
		{kind: "hook"},
		{kind: "notify"},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			if got := WatchActivityFailed(tt.kind); got != tt.want {
				t.Fatalf("WatchActivityFailed(%q) = %v, want %v", tt.kind, got, tt.want)
			}
		})
	}
}
