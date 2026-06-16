package app

import "testing"

func TestServiceState(t *testing.T) {
	tests := []struct {
		name          string
		enabled       bool
		monitored     bool
		backendStatus string
		checkHealth   string
		want          string
	}{
		{name: "disabled", enabled: false, monitored: false, backendStatus: "active", want: TargetStateDisabled},
		{name: "running unmonitored", enabled: true, monitored: false, backendStatus: "active", want: TargetStateRunning},
		{name: "paused unmonitored", enabled: true, monitored: false, backendStatus: "paused", want: TargetStatePaused},
		{name: "stopped unmonitored", enabled: true, monitored: false, backendStatus: "inactive", want: TargetStateStopped},
		{name: "monitorized active healthy", enabled: true, monitored: true, backendStatus: "active", checkHealth: "ok", want: TargetStateMonitorized},
		{name: "paused monitored", enabled: true, monitored: true, backendStatus: "paused", checkHealth: "ok", want: TargetStatePaused},
		{name: "monitorized active unknown checks", enabled: true, monitored: true, backendStatus: "active", checkHealth: "unknown", want: TargetStateMonitorized},
		{name: "failed backend", enabled: true, monitored: true, backendStatus: "failed", want: TargetStateFailed},
		{name: "failed checks", enabled: true, monitored: true, backendStatus: "active", checkHealth: "failing", want: TargetStateFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ServiceState(tt.enabled, tt.monitored, tt.backendStatus, tt.checkHealth); got != tt.want {
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
		want      string
	}{
		{name: "disabled", enabled: false, want: TargetStateDisabled},
		{name: "unmonitorized", enabled: true, monitored: false, want: TargetStateUnmonitorized},
		{name: "ok", enabled: true, monitored: true, want: TargetStateOK},
		{name: "failed", enabled: true, monitored: true, failed: true, want: TargetStateFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WatchState(tt.enabled, tt.monitored, tt.failed); got != tt.want {
				t.Fatalf("WatchState() = %q, want %q", got, tt.want)
			}
		})
	}
}
