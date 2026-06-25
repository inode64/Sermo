package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"sermo/internal/execx"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

func writeProcessConfig(t *testing.T, pidfile string) string {
	t.Helper()
	root := t.TempDir()
	global := filepath.Join(root, "sermo.yml")
	mustWrite(t, global, `
paths:
  services: [ `+root+`/services ]
defaults:
  policy:
    cooldown: 5m
`)
	mustWrite(t, filepath.Join(root, "services", "mysql-main.yml"), `
name: mysql-main
service: mysql
pidfile: `+pidfile+`
`)
	return global
}

func TestProcessesUsesSelectorsAndReports(t *testing.T) {
	global := writeProcessConfig(t, "/run/mysqld/mysqld.pid")

	var gotSelectors []process.Selector
	var stdout bytes.Buffer
	app := App{
		Env:    func(string) string { return "" },
		Stdout: &stdout,
		Stderr: &bytes.Buffer{},
		Discover: func(sel []process.Selector) ([]process.Process, []string) {
			gotSelectors = sel
			return []process.Process{
				{PID: 100, PPID: 1, User: "mysql", UID: 110, Exe: "/usr/sbin/mysqld", ExeOK: true, Role: "pidfile", Source: "pidfile"},
				{PID: 200, PPID: 100, UID: 110, ExeOK: false, Cmdline: []string{"/usr/sbin/apache2", "-D", "INFO"}, Source: "child"},
			}, nil
		},
	}

	code := app.Run(context.Background(), []string{"--config", global, "processes", "mysql-main"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	if len(gotSelectors) != 1 || gotSelectors[0].Type != process.SelectorPidfile || len(gotSelectors[0].Paths) != 1 || gotSelectors[0].Paths[0] != "/run/mysqld/mysqld.pid" {
		t.Fatalf("selectors passed to Discover = %+v", gotSelectors)
	}
	out := stdout.String()
	if !strings.Contains(out, "pid=100 ppid=1 user=mysql exe=/usr/sbin/mysqld source=pidfile role=pidfile") {
		t.Fatalf("stdout missing main process line:\n%s", out)
	}
	if !strings.Contains(out, `pid=200 ppid=100 user=unknown cmd="/usr/sbin/apache2 -D INFO" source=child`) {
		t.Fatalf("stdout missing child line with command fallback:\n%s", out)
	}
}

func TestProcessesJSON(t *testing.T) {
	global := writeProcessConfig(t, "/run/x.pid")
	var stdout bytes.Buffer
	app := App{
		Env:    func(string) string { return "" },
		Stdout: &stdout,
		Stderr: &bytes.Buffer{},
		Discover: func([]process.Selector) ([]process.Process, []string) {
			return []process.Process{{PID: 100, Source: "pidfile"}}, nil
		},
	}
	code := app.Run(context.Background(), []string{"--config", global, "--json", "processes", "mysql-main"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	var got struct {
		Service   string `json:"service"`
		Processes []struct {
			PID int `json:"pid"`
		} `json:"processes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json: %v (out=%s)", err, stdout.String())
	}
	if got.Service != "mysql-main" || len(got.Processes) != 1 || got.Processes[0].PID != 100 {
		t.Fatalf("unexpected JSON: %+v", got)
	}
}

func TestProcessesNoneFound(t *testing.T) {
	global := writeProcessConfig(t, "/run/x.pid")
	var stdout bytes.Buffer
	app := App{
		Env:      func(string) string { return "" },
		Stdout:   &stdout,
		Stderr:   &bytes.Buffer{},
		Discover: func([]process.Selector) ([]process.Process, []string) { return nil, nil },
	}
	code := app.Run(context.Background(), []string{"--config", global, "processes", "mysql-main"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	if !strings.Contains(stdout.String(), "no processes found for mysql-main") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestProcessesUsesSystemdMainPIDWhenPidfileMissing(t *testing.T) {
	global := writeProcessConfig(t, "/run/missing-node-exporter.pid")
	var stdout, stderr bytes.Buffer
	app := App{
		Detector: fakeBackendDetector{detection: servicemgr.Detection{Backend: servicemgr.BackendSystemd}},
		Runner:   processSystemdRunner{pid: os.Getpid()},
		Env:      func(string) string { return "" },
		Stdout:   &stdout,
		Stderr:   &stderr,
	}

	code := app.Run(context.Background(), []string{"--config", global, "processes", "mysql-main"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d; stderr=%s", code, exitSuccess, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "pid="+strconv.Itoa(os.Getpid())+" ") || !strings.Contains(out, "source=backend") {
		t.Fatalf("stdout = %q, want current pid from backend", out)
	}
	if strings.Contains(stderr.String(), "pidfile") {
		t.Fatalf("stderr = %q, want no pidfile warning", stderr.String())
	}
}

func TestProcessesUnknownService(t *testing.T) {
	global := writeProcessConfig(t, "/run/x.pid")
	var stderr bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &bytes.Buffer{}, Stderr: &stderr,
		Discover: func([]process.Selector) ([]process.Process, []string) { return nil, nil }}

	code := app.Run(context.Background(), []string{"--config", global, "processes", "nope"})
	if code != exitRuntimeError {
		t.Fatalf("Run() exit = %d, want %d", code, exitRuntimeError)
	}
	if !strings.Contains(stderr.String(), "unknown service") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestProcessesRequiresService(t *testing.T) {
	var stderr bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &bytes.Buffer{}, Stderr: &stderr}
	code := app.Run(context.Background(), []string{"processes"})
	if code != exitUsage {
		t.Fatalf("Run() exit = %d, want %d", code, exitUsage)
	}
}

type processSystemdRunner struct {
	pid int
}

func (r processSystemdRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	if name != "systemctl" {
		return execx.Result{}, nil
	}
	joined := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(joined, "cat "):
		return execx.Result{Stdout: "[Service]\nExecStart=/bin/sleep infinity\n"}, nil
	case strings.Contains(joined, "-p ControlGroup"):
		return execx.Result{Stdout: "\n"}, nil
	case strings.Contains(joined, "-p MainPID"):
		return execx.Result{Stdout: strconv.Itoa(r.pid) + "\n"}, nil
	default:
		return execx.Result{Stdout: "\n"}, nil
	}
}

// Exercises the real OS discoverer end to end through a pidfile pointing at the
// running test process.
func TestProcessesRealPidfileSelf(t *testing.T) {
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "self.pid")
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	global := writeProcessConfig(t, pidfile)

	var stdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{}}
	code := app.Run(context.Background(), []string{"--config", global, "processes", "mysql-main"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	if !strings.Contains(stdout.String(), "pid="+strconv.Itoa(os.Getpid())+" ") {
		t.Fatalf("stdout did not include self pid:\n%s", stdout.String())
	}
}
