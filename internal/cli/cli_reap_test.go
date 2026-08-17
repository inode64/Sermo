package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"sermo/internal/config"
	"sermo/internal/operation"
	"sermo/internal/process"
)

func reapStray(pid int, exe string) process.Process {
	return process.Process{
		PID: pid, PPID: 1, User: "root", UID: 0,
		Exe: exe, ExeOK: true,
		Role: process.RoleMain, Source: process.SourceBackend, Stray: true,
	}
}

// reapApp records the options and action the CLI hands the operation seam, so the
// --apply flag can be followed all the way to the engine call.
func reapApp(t *testing.T, result operation.Result, seen *options, action *string) (App, *bytes.Buffer) {
	t.Helper()
	stdout := &bytes.Buffer{}
	return App{
		LoadConfig: config.Load,
		Operate: func(_ context.Context, opts options, _ *config.Config, _ config.Resolved, _, act string) (operation.Result, error) {
			*seen = opts
			*action = act
			return result, nil
		},
		Env:    func(string) string { return "" },
		Stdout: stdout,
		Stderr: &bytes.Buffer{},
	}, stdout
}

func TestReapPreviewOutputSaysNothingWasSignalled(t *testing.T) {
	global := writeActionConfig(t)
	result := operation.Result{
		Service: "web", Action: actionReap, Status: operation.ResultOK,
		Message:   "preview: 1 of 2 stray process(es) would be signalled",
		Processes: []process.Process{reapStray(300, "/usr/bin/dbus-daemon"), reapStray(400, "/usr/bin/other")},
	}
	var seen options
	var action string
	app, stdout := reapApp(t, result, &seen, &action)

	if code := app.Run(context.Background(), []string{"--config", global, "reap", "web"}); code != exitSuccess {
		t.Fatalf("exit = %d, want %d", code, exitSuccess)
	}
	if action != actionReap {
		t.Fatalf("action = %q, want %q", action, actionReap)
	}
	if seen.apply {
		t.Fatal("a bare reap must not set apply")
	}
	out := stdout.String()
	for _, want := range []string{
		"web reap ok",
		"reason: preview: 1 of 2 stray process(es) would be signalled",
		"stray pid=300 user=root exe=/usr/bin/dbus-daemon",
		"stray pid=400 user=root exe=/usr/bin/other",
		"nothing was signalled; run `sermoctl reap web --apply`",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout = %q, want it to contain %q", out, want)
		}
	}
}

func TestReapApplyPassesTheFlagAndDropsTheHint(t *testing.T) {
	global := writeActionConfig(t)
	result := operation.Result{
		Service: "web", Action: actionReap, Status: operation.ResultOK,
		Message: "reap ok (signalled 1 of 1 stray process(es))",
	}
	var seen options
	var action string
	app, stdout := reapApp(t, result, &seen, &action)

	if code := app.Run(context.Background(), []string{"--config", global, "reap", "web", "--apply"}); code != exitSuccess {
		t.Fatalf("exit = %d, want %d", code, exitSuccess)
	}
	if !seen.apply {
		t.Fatal("--apply must reach the operation seam")
	}
	if out := stdout.String(); strings.Contains(out, "nothing was signalled") {
		t.Fatalf("stdout = %q, want no preview hint after --apply", out)
	}
}

func TestReapBlockedExitsBlocked(t *testing.T) {
	global := writeActionConfig(t)
	result := operation.Result{
		Service: "web", Action: actionReap, Status: operation.ResultBlocked,
		Message:   "reap: 1 stray process(es) reported, none authorized by reap.kill_only_if",
		Processes: []process.Process{reapStray(300, "/usr/bin/dbus-daemon")},
	}
	var seen options
	var action string
	app, stdout := reapApp(t, result, &seen, &action)

	if code := app.Run(context.Background(), []string{"--config", global, "reap", "web", "--apply"}); code != exitBlocked {
		t.Fatalf("exit = %d, want %d", code, exitBlocked)
	}
	if out := stdout.String(); !strings.Contains(out, "BLOCKED web reap") {
		t.Fatalf("stdout = %q, want the blocked header", out)
	}
}

func TestReapOrphanProcessesExitsNotActive(t *testing.T) {
	global := writeActionConfig(t)
	result := operation.Result{
		Service: "web", Action: actionReap, Status: operation.ResultOrphanProcesses,
		Message:   "reap: signalled 1 of 2 stray process(es); 1 remain",
		Processes: []process.Process{reapStray(400, "/usr/bin/other")},
	}
	var seen options
	var action string
	app, _ := reapApp(t, result, &seen, &action)

	if code := app.Run(context.Background(), []string{"--config", global, "reap", "web", "--apply"}); code != exitNotActive {
		t.Fatalf("exit = %d, want %d", code, exitNotActive)
	}
}

// --apply is the only thing that authorizes signalling a stray, so it must be
// rejected wherever it would be quietly ignored.
func TestApplyRejectedOutsideReap(t *testing.T) {
	global := writeActionConfig(t)
	var stderr bytes.Buffer
	app := App{
		LoadConfig: config.Load,
		Operate:    okOperate,
		Env:        func(string) string { return "" },
		Stdout:     &bytes.Buffer{},
		Stderr:     &stderr,
	}

	code := app.Run(context.Background(), []string{"--config", global, "restart", "web", "--apply"})
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if got := stderr.String(); !strings.Contains(got, "--apply is only supported by reap") {
		t.Fatalf("stderr = %q, want the --apply usage error", got)
	}
}

func TestFormatProcessFlagsStray(t *testing.T) {
	line := formatProcess(reapStray(300, "/usr/bin/dbus-daemon"))
	if !strings.Contains(line, "stray=true") {
		t.Fatalf("formatProcess = %q, want it to flag the stray", line)
	}
	claimed := reapStray(300, "/usr/bin/dbus-daemon")
	claimed.Stray = false
	if strings.Contains(formatProcess(claimed), "stray") {
		t.Fatalf("formatProcess = %q, want no stray flag for a claimed process", formatProcess(claimed))
	}
}
