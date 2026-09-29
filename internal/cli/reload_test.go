package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"sermo/internal/config"
	"sermo/internal/process"
)

const (
	reloadTestDaemonPID = 4242
	reloadTestOtherPID  = 5151
)

// reloadTestApp builds a hermetic `daemon reload` app: pidfiles come from the
// temp runtime, identities from the table, and signals are recorded instead of
// delivered.
func reloadTestApp(t *testing.T, pidfile string, scan []int, ids map[int]process.Identity) (App, string, *[]process.Process, *bytes.Buffer) {
	t.Helper()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	minCfg := []byte("paths:\n  runtime: " + tmp + "\ndefaults:\n  policy:\n    cooldown: 5m\n")
	if err := os.WriteFile(cfgPath, minCfg, 0o644); err != nil {
		t.Fatal(err)
	}
	if pidfile != "" {
		if err := os.WriteFile(filepath.Join(tmp, daemonPIDFilename), []byte(pidfile+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var signalled []process.Process
	var stderr bytes.Buffer
	app := App{
		LoadConfig:       config.Load,
		Stderr:           &stderr,
		Stdout:           &bytes.Buffer{},
		pidfileFallbacks: []string{},
		FindPID: func(name string) ([]int, error) {
			if name != daemonProcessName {
				t.Fatalf("FindPID probed %q, want %q", name, daemonProcessName)
			}
			return scan, nil
		},
		daemonIdentity: func(pid int) (process.Identity, bool) {
			id, ok := ids[pid]
			return id, ok
		},
		signalDaemon: func(_ context.Context, target process.Process, sig syscall.Signal) error {
			if sig != syscall.SIGHUP {
				t.Fatalf("signal = %v, want SIGHUP", sig)
			}
			signalled = append(signalled, target)
			return nil
		},
	}
	return app, cfgPath, &signalled, &stderr
}

func sermodIdentity(pid int) process.Identity {
	return process.Identity{PID: pid, StartTicks: 77, StartTicksOK: true, Exe: "/usr/sbin/sermod", ExeOK: true}
}

func TestDaemonReloadSignalsVerifiedTarget(t *testing.T) {
	sleeper := process.Identity{PID: reloadTestOtherPID, StartTicks: 9, StartTicksOK: true, Exe: "/usr/bin/sleep", ExeOK: true}
	tests := []struct {
		name    string
		pidfile string
		scan    []int
		ids     map[int]process.Identity
		want    int // 0: no signal and exit error
	}{
		{
			name:    "pidfile names sermod",
			pidfile: strconv.Itoa(reloadTestDaemonPID),
			ids:     map[int]process.Identity{reloadTestDaemonPID: sermodIdentity(reloadTestDaemonPID)},
			want:    reloadTestDaemonPID,
		},
		{
			name:    "stale pidfile with recycled pid falls back to scan",
			pidfile: strconv.Itoa(reloadTestOtherPID),
			scan:    []int{reloadTestDaemonPID},
			ids: map[int]process.Identity{
				reloadTestOtherPID:  sleeper,
				reloadTestDaemonPID: sermodIdentity(reloadTestDaemonPID),
			},
			want: reloadTestDaemonPID,
		},
		{
			name:    "stale pidfile with dead pid falls back to scan",
			pidfile: strconv.Itoa(reloadTestOtherPID),
			scan:    []int{reloadTestDaemonPID},
			ids:     map[int]process.Identity{reloadTestDaemonPID: sermodIdentity(reloadTestDaemonPID)},
			want:    reloadTestDaemonPID,
		},
		{
			name:    "stale pidfile and no sermod is never signalled",
			pidfile: strconv.Itoa(reloadTestOtherPID),
			ids:     map[int]process.Identity{reloadTestOtherPID: sleeper},
		},
		{
			name: "scan result renamed through prctl is rejected",
			scan: []int{reloadTestOtherPID},
			ids:  map[int]process.Identity{reloadTestOtherPID: sleeper},
		},
		{
			name:    "identity without start time is rejected",
			pidfile: strconv.Itoa(reloadTestDaemonPID),
			ids:     map[int]process.Identity{reloadTestDaemonPID: {PID: reloadTestDaemonPID, Exe: "/usr/sbin/sermod", ExeOK: true}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, cfgPath, signalled, stderr := reloadTestApp(t, tt.pidfile, tt.scan, tt.ids)
			code := app.Run(context.Background(), []string{"--config", cfgPath, "daemon", "reload"})
			if tt.want == 0 {
				if code != exitRuntimeError || len(*signalled) != 0 {
					t.Fatalf("exit=%d signalled=%v, want %d and no signal", code, *signalled, exitRuntimeError)
				}
				if !strings.Contains(stderr.String(), "could not find running sermod") {
					t.Fatalf("stderr = %q", stderr.String())
				}
				return
			}
			if code != exitSuccess || len(*signalled) != 1 || (*signalled)[0].PID != tt.want {
				t.Fatalf("exit=%d signalled=%v stderr=%q, want pid %d", code, *signalled, stderr.String(), tt.want)
			}
			if got := (*signalled)[0]; got.StartTicks != 77 || got.Exe != "/usr/sbin/sermod" {
				t.Fatalf("signal target %+v lost the verified identity", got)
			}
		})
	}
}

func TestDaemonReloadReportsSignalFailure(t *testing.T) {
	app, cfgPath, _, stderr := reloadTestApp(t, "", []int{reloadTestDaemonPID},
		map[int]process.Identity{reloadTestDaemonPID: sermodIdentity(reloadTestDaemonPID)})
	app.signalDaemon = func(context.Context, process.Process, syscall.Signal) error { return syscall.EPERM }
	code := app.Run(context.Background(), []string{"--config", cfgPath, "daemon", "reload"})
	if code != exitRuntimeError || !strings.Contains(stderr.String(), "failed to signal pid 4242") {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
}
