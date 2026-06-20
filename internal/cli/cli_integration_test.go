package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sermo/internal/config"
	"sermo/internal/servicemgr"
)

// fakeSystemctl records every invocation to $SERMO_FAKE_LOG and answers
// is-active with "active"; everything else succeeds.
const fakeSystemctl = `#!/bin/sh
echo "$*" >> "$SERMO_FAKE_LOG"
if [ "$1" = "is-active" ]; then echo active; fi
exit 0
`

// withFakeSystemctl installs a fake systemctl on PATH and returns the call-log
// path. Tests run the real servicemgr manager and operation engine against it.
func withFakeSystemctl(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(fakeSystemctl), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	logPath := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("SERMO_FAKE_LOG", logPath)
	return logPath
}

// systemdApp builds an App that uses the real servicemgr manager (backed by the
// fake systemctl) but a forced systemd backend, plus the real operation engine.
func systemdApp(stdout, stderr *bytes.Buffer) App {
	return App{
		Detector:   fakeBackendDetector{detection: servicemgr.Detection{Backend: servicemgr.BackendSystemd}},
		NewManager: servicemgr.NewManager,
		LoadConfig: nil, // defaults to config.Load
		Env:        func(string) string { return "" },
		Stdout:     stdout,
		Stderr:     stderr,
	}
}

func calls(t *testing.T, logPath string) string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(data)
}

// fakeRCService answers `rc-service <svc> status` as started; other verbs
// succeed. Used to drive the real OpenRC manager hermetically.
const fakeRCService = `#!/bin/sh
echo "$*" >> "$SERMO_FAKE_LOG"
if [ "$2" = "status" ]; then echo " * status: started"; fi
exit 0
`

func TestIntegrationStatusViaFakeRCService(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "rc-service"), []byte(fakeRCService), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SERMO_FAKE_LOG", filepath.Join(t.TempDir(), "calls.log"))

	var stdout bytes.Buffer
	app := App{
		Detector:   fakeBackendDetector{detection: servicemgr.Detection{Backend: servicemgr.BackendOpenRC}},
		NewManager: servicemgr.NewManager,
		LoadConfig: func(string, ...config.Option) (*config.Config, error) {
			return nil, errNoConfigForInvalidTest
		},
		Env:    func(string) string { return "" },
		Stdout: &stdout,
		Stderr: &bytes.Buffer{},
	}
	code := app.Run(context.Background(), []string{"status", "nginx"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	if !strings.Contains(stdout.String(), "nginx state=running backend=openrc service=nginx") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestIntegrationStatusViaFakeSystemctl(t *testing.T) {
	withFakeSystemctl(t)
	var stdout bytes.Buffer
	app := systemdApp(&stdout, &bytes.Buffer{})
	app.LoadConfig = func(string, ...config.Option) (*config.Config, error) {
		return nil, errNoConfigForInvalidTest
	}

	code := app.Run(context.Background(), []string{"status", "nginx"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	if !strings.Contains(stdout.String(), "nginx state=running backend=systemd") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestIntegrationRestartViaFakeSystemctl(t *testing.T) {
	logPath := withFakeSystemctl(t)
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "sermo.yml"), `
paths:
  services: [ `+root+`/enabled ]
  runtime: `+root+`/run
defaults:
  policy: { cooldown: 5m }
`)
	mustWrite(t, filepath.Join(root, "enabled", "svc.yml"), `
kind: service
name: svc
service: svc
`)

	var stdout bytes.Buffer
	app := systemdApp(&stdout, &bytes.Buffer{})
	code := app.Run(context.Background(), []string{"--config", filepath.Join(root, "sermo.yml"), "restart", "svc"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d; out=%s", code, exitSuccess, stdout.String())
	}
	if !strings.Contains(stdout.String(), "svc restart ok") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	log := calls(t, logPath)
	if !strings.Contains(log, "stop -- svc.service") || !strings.Contains(log, "start -- svc.service") {
		t.Fatalf("expected stop then start of svc.service, calls=\n%s", log)
	}
}

func TestIntegrationRestartBlockedByGuard(t *testing.T) {
	logPath := withFakeSystemctl(t)
	root := t.TempDir()
	flag := filepath.Join(root, "backup.flag")
	if err := os.WriteFile(flag, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "sermo.yml"), `
paths:
  services: [ `+root+`/enabled ]
  runtime: `+root+`/run
defaults:
  policy: { cooldown: 5m }
`)
	mustWrite(t, filepath.Join(root, "enabled", "svc.yml"), `
kind: service
name: svc
service: svc
checks:
  busy: { type: file_exists, path: `+flag+` }
rules:
  block-during-backup:
    type: guard
    blocks: [restart]
    if: { active: { check: busy } }
    then: { action: block, message: "backup running" }
`)

	var stdout bytes.Buffer
	app := systemdApp(&stdout, &bytes.Buffer{})
	code := app.Run(context.Background(), []string{"--config", filepath.Join(root, "sermo.yml"), "restart", "svc"})
	if code != exitBlocked {
		t.Fatalf("Run() exit = %d, want %d (blocked); out=%s", code, exitBlocked, stdout.String())
	}
	if !strings.Contains(stdout.String(), "BLOCKED svc restart") || !strings.Contains(stdout.String(), "backup running") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	// The guard blocks before any backend action: no stop/start must occur.
	if log := calls(t, logPath); strings.Contains(log, "stop -- svc.service") || strings.Contains(log, "start -- svc.service") {
		t.Fatalf("guard-blocked restart must not touch the backend, calls=\n%s", log)
	}
}
