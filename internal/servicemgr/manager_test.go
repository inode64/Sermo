package servicemgr

import (
	"context"
	"errors"
	"testing"

	"sermo/internal/execx"
)

func TestSystemdUnitNormalization(t *testing.T) {
	cases := map[string]string{
		"nginx":         "nginx.service",
		"nginx.service": "nginx.service",
		"sshd.socket":   "sshd.socket",
		"backup.timer":  "backup.timer",
	}
	for input, want := range cases {
		if got := systemdUnit(input); got != want {
			t.Errorf("systemdUnit(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSystemdManagerStatus(t *testing.T) {
	cases := []struct {
		name     string
		result   execx.Result
		runErr   error
		want     Status
		wantUnit string
	}{
		{name: "active", result: execx.Result{Stdout: "active\n"}, want: StatusActive, wantUnit: "nginx.service"},
		{name: "inactive", result: execx.Result{Stdout: "inactive\n", ExitCode: 3}, runErr: errors.New("exit 3"), want: StatusInactive, wantUnit: "nginx.service"},
		{name: "failed", result: execx.Result{Stdout: "failed\n", ExitCode: 3}, runErr: errors.New("exit 3"), want: StatusFailed, wantUnit: "nginx.service"},
		{name: "activating", result: execx.Result{Stdout: "activating\n", ExitCode: 3}, runErr: errors.New("exit 3"), want: StatusUnknown, wantUnit: "nginx.service"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := systemdManager{runner: stubRunner{result: tc.result, err: tc.runErr}}
			got, err := m.Status(context.Background(), "nginx")
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if got.Status != tc.want {
				t.Errorf("Status = %q, want %q", got.Status, tc.want)
			}
			if got.Unit != tc.wantUnit {
				t.Errorf("Unit = %q, want %q", got.Unit, tc.wantUnit)
			}
			if got.Backend != BackendSystemd {
				t.Errorf("Backend = %q, want systemd", got.Backend)
			}
		})
	}
}

func TestSystemdManagerStatusLaunchFailure(t *testing.T) {
	m := systemdManager{runner: stubRunner{result: execx.Result{ExitCode: -1}, err: errors.New("not found")}}
	if _, err := m.Status(context.Background(), "nginx"); err == nil {
		t.Fatal("Status() error = nil, want launch failure")
	}
}

func TestOpenRCManagerStatus(t *testing.T) {
	cases := []struct {
		name   string
		result execx.Result
		want   Status
	}{
		{name: "started stdout", result: execx.Result{Stdout: " * status: started\n"}, want: StatusActive},
		{name: "stopped stdout", result: execx.Result{Stdout: " * status: stopped\n", ExitCode: 3}, want: StatusInactive},
		{name: "crashed stdout", result: execx.Result{Stdout: " * status: crashed\n", ExitCode: 1}, want: StatusFailed},
		{name: "exit code fallback active", result: execx.Result{ExitCode: 0}, want: StatusActive},
		{name: "exit code fallback inactive", result: execx.Result{ExitCode: 3}, want: StatusInactive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := openrcManager{runner: stubRunner{result: tc.result}}
			got, err := m.Status(context.Background(), "nginx")
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if got.Status != tc.want {
				t.Errorf("Status = %q, want %q", got.Status, tc.want)
			}
			if got.Unit != "nginx" {
				t.Errorf("Unit = %q, want nginx", got.Unit)
			}
		})
	}
}

func TestNewManagerUnsupportedBackend(t *testing.T) {
	if _, err := newManager(BackendAuto, stubRunner{}); err == nil {
		t.Fatal("newManager(auto) error = nil, want unsupported error")
	}
}

type stubRunner struct {
	result execx.Result
	err    error
}

func (r stubRunner) Run(context.Context, string, ...string) (execx.Result, error) {
	return r.result, r.err
}
