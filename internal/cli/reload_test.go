package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
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
	reloadTestDaemonExe = "/usr/sbin/sermod"
	reloadTestCallerUID = 0
)

// reloadTestApp builds a hermetic `daemon reload` app: pidfiles come from the
// temp runtime, identities from the table, and signals are recorded instead of
// delivered.
func reloadTestApp(t *testing.T, pidfile string, scan []int, ids map[int]process.Identity) (App, string, *[]process.Process, *bytes.Buffer) {
	t.Helper()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	minCfg := []byte("engine:\n  runtime: " + tmp + "\ndefaults:\n  policy:\n    cooldown: 5m\n")
	if err := os.WriteFile(cfgPath, minCfg, 0o644); err != nil {
		t.Fatal(err)
	}
	if pidfile != "" {
		if err := os.WriteFile(filepath.Join(tmp, config.DaemonPIDFilename), []byte(pidfile+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var signalled []process.Process
	var stderr bytes.Buffer
	app := hermeticApp(t, App{
		LoadConfig:       testConfigLoader(t),
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
		expectedDaemon: func() (daemonExpectation, error) {
			return daemonExpectation{exes: []string{reloadTestDaemonExe}, uid: reloadTestCallerUID}, nil
		},
		signalDaemon: func(_ context.Context, target process.Process, sig syscall.Signal) error {
			if sig != syscall.SIGHUP {
				t.Fatalf("signal = %v, want SIGHUP", sig)
			}
			signalled = append(signalled, target)
			return nil
		},
	})
	return app, cfgPath, &signalled, &stderr
}

func sermodIdentity(pid int) process.Identity {
	return process.Identity{PID: pid, StartTicks: 77, StartTicksOK: true, Exe: reloadTestDaemonExe, ExeOK: true, UID: reloadTestCallerUID}
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

// A program merely named sermod, or the right binary under another user, is
// never the daemon: SIGHUP goes only to the sermod installed alongside this
// sermoctl and running as the caller.
func TestDaemonReloadRejectsUnexpectedSermod(t *testing.T) {
	impostor := sermodIdentity(reloadTestOtherPID)
	impostor.Exe = "/tmp/evil/sermod"
	replacedOutside := sermodIdentity(reloadTestOtherPID)
	replacedOutside.Exe, replacedOutside.ExeOK, replacedOutside.ExePrev = "/tmp/evil/sermod (deleted)", false, "/tmp/evil/sermod"
	for _, tc := range []struct {
		name string
		id   process.Identity
		want string
	}{
		{name: "same name, other path", id: impostor, want: "runs /tmp/evil/sermod, not the sermod installed alongside this sermoctl"},

		{name: "replaced binary outside the expected set", id: replacedOutside, want: "runs /tmp/evil/sermod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, cfgPath, signalled, stderr := reloadTestApp(t, strconv.Itoa(reloadTestOtherPID), []int{reloadTestOtherPID},
				map[int]process.Identity{reloadTestOtherPID: tc.id})
			code := app.Run(context.Background(), []string{"--config", cfgPath, "daemon", "reload"})
			if code != exitRuntimeError || len(*signalled) != 0 {
				t.Fatalf("exit=%d signalled=%v, want refusal", code, *signalled)
			}
			if !strings.Contains(stderr.String(), tc.want) || strings.Count(stderr.String(), tc.want) != 1 {
				t.Fatalf("stderr = %q, want one %q", stderr.String(), tc.want)
			}
		})
	}
}

// The installed sermod under another account is the daemon for root, which may
// signal any account; a non-root caller reloads only the sermod running as
// itself.
func TestDaemonReloadUIDMustMatchUnlessRoot(t *testing.T) {
	serviceAccount := sermodIdentity(reloadTestDaemonPID)
	serviceAccount.UID = 1000
	for _, tc := range []struct {
		name   string
		caller uint32
		want   string // "" accepts
	}{
		{name: "root caller", caller: reloadTestCallerUID},
		{name: "same non-root caller", caller: 1000},
		{name: "other non-root caller", caller: 1001, want: "runs as uid 1000, not the caller's uid 1001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, cfgPath, signalled, stderr := reloadTestApp(t, strconv.Itoa(reloadTestDaemonPID), nil,
				map[int]process.Identity{reloadTestDaemonPID: serviceAccount})
			app.expectedDaemon = func() (daemonExpectation, error) {
				return daemonExpectation{exes: []string{reloadTestDaemonExe}, uid: tc.caller}, nil
			}
			code := app.Run(context.Background(), []string{"--config", cfgPath, "daemon", "reload"})
			if tc.want == "" {
				if code != exitSuccess || len(*signalled) != 1 {
					t.Fatalf("exit=%d signalled=%v stderr=%q, want the reload", code, *signalled, stderr.String())
				}
				return
			}
			if code != exitRuntimeError || len(*signalled) != 0 || !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("exit=%d signalled=%v stderr=%q, want refusal %q", code, *signalled, stderr.String(), tc.want)
			}
		})
	}
}

// A daemon whose binary a package upgrade replaced keeps its previous path: it
// is still found, so the pidfd signaler can refuse the deleted executable
// instead of the operator hearing that no sermod runs.
func TestDaemonReloadFindsReplacedExpectedBinary(t *testing.T) {
	replaced := sermodIdentity(reloadTestDaemonPID)
	replaced.Exe, replaced.ExeOK, replaced.ExePrev = reloadTestDaemonExe+" (deleted)", false, reloadTestDaemonExe
	app, cfgPath, signalled, _ := reloadTestApp(t, strconv.Itoa(reloadTestDaemonPID), nil,
		map[int]process.Identity{reloadTestDaemonPID: replaced})
	if code := app.Run(context.Background(), []string{"--config", cfgPath, "daemon", "reload"}); code != exitSuccess {
		t.Fatalf("exit=%d", code)
	}
	if len(*signalled) != 1 || (*signalled)[0].ExeOK {
		t.Fatalf("signalled=%+v, want the replaced daemon with ExeOK=false", *signalled)
	}
}

func TestDaemonReloadFailsClosedWithoutExpectedIdentity(t *testing.T) {
	app, cfgPath, signalled, stderr := reloadTestApp(t, strconv.Itoa(reloadTestDaemonPID), nil,
		map[int]process.Identity{reloadTestDaemonPID: sermodIdentity(reloadTestDaemonPID)})
	app.expectedDaemon = func() (daemonExpectation, error) { return daemonExpectation{}, errors.New("unreadable") }
	code := app.Run(context.Background(), []string{"--config", cfgPath, "daemon", "reload"})
	if code != exitRuntimeError || len(*signalled) != 0 || !strings.Contains(stderr.String(), "cannot identify the sermod to reload") {
		t.Fatalf("exit=%d signalled=%v stderr=%q", code, *signalled, stderr.String())
	}
}

func TestDaemonExecutableCandidates(t *testing.T) {
	writeExe := func(t *testing.T, path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, root string)
		want  []string
	}{
		{
			name: "sibling binary",
			setup: func(t *testing.T, root string) {
				t.Helper()
				writeExe(t, filepath.Join(root, "bin", "sermod"))
			},
			want: []string{"bin/sermod"},
		},
		{
			name: "sbin beside bin",
			setup: func(t *testing.T, root string) {
				t.Helper()
				writeExe(t, filepath.Join(root, "sbin", "sermod"))
			},
			want: []string{"sbin/sermod"},
		},
		{
			name: "both installed",
			setup: func(t *testing.T, root string) {
				t.Helper()
				writeExe(t, filepath.Join(root, "bin", "sermod"))
				writeExe(t, filepath.Join(root, "sbin", "sermod"))
			},
			want: []string{"bin/sermod", "sbin/sermod"},
		},
		{
			name: "merged sbin symlink names one file once",
			setup: func(t *testing.T, root string) {
				t.Helper()
				writeExe(t, filepath.Join(root, "bin", "sermod"))
				if err := os.Symlink("bin", filepath.Join(root, "sbin")); err != nil {
					t.Fatal(err)
				}
			},
			want: []string{"bin/sermod"},
		},
		{name: "none installed", setup: func(*testing.T, string) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.setup(t, root)
			resolvedRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			for _, rel := range tc.want {
				want = append(want, filepath.Join(resolvedRoot, rel))
			}
			got := daemonExecutableCandidates(filepath.Join(root, "bin", "sermoctl"), filepath.EvalSymlinks)
			if !slices.Equal(got, want) {
				t.Fatalf("candidates = %v, want %v", got, want)
			}
		})
	}
}
