package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcore "sermo/internal/app"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/operation"
	"sermo/internal/servicemgr"
)

// writePreflightConfig builds a service with preflight checks: a binary check at
// binPath and an optional file_exists at a missing path.
func writePreflightConfig(t *testing.T, binPath string) string {
	t.Helper()
	global := writeServiceConfig(t, `
engine:
  default_timeout: 3s`+servicesDirGlobal, map[string]string{
		"services/apache-main.yml": `
name: apache-main
service: apache2
variables:
  binary: ` + binPath + `
preflight:
  binary:
    type: binary
    path: "${binary}"
  optional-flag:
    type: file_exists
    path: /definitely/missing/flag
    optional: true
`,
	})
	return global
}

func TestPreflightPassesWithOptionalWarning(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "apache2")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	global := writePreflightConfig(t, bin)

	var stdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{}}
	code := app.Run(context.Background(), []string{"--config", global, "preflight", "apache-main"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d (required passed); out=%s", code, exitSuccess, stdout.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "preflight apache-main: OK") {
		t.Fatalf("stdout = %q", out)
	}
	if !strings.Contains(out, "WARN optional-flag") {
		t.Fatalf("optional failure should be a warning line: %q", out)
	}
}

func TestPreflightFailsOnRequiredCheck(t *testing.T) {
	// Binary points at a missing path: required failure.
	global := writePreflightConfig(t, "/definitely/missing/apache2")

	var stdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{}}
	code := app.Run(context.Background(), []string{"--config", global, "preflight", "apache-main"})
	if code != exitNotActive {
		t.Fatalf("Run() exit = %d, want %d (required failed)", code, exitNotActive)
	}
	if !strings.Contains(stdout.String(), "preflight apache-main: FAIL") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestPreflightUsesPreparedEngine(t *testing.T) {
	global := writeServiceConfig(t, servicesDirGlobal, map[string]string{
		"services/web.yml": `
name: web
service: web
`,
	})

	var called, hadDeadline bool
	var stdout bytes.Buffer
	cliApp := App{
		Detector: fakeBackendDetector{detection: servicemgr.Detection{Backend: servicemgr.BackendSystemd}},
		NewManager: func(servicemgr.Backend) (servicemgr.Manager, error) {
			return fakeManager{}, nil
		},
		Runner: statusUnitRunner{known: "web.service"},
		buildServiceRuntime: func(context.Context, appcore.ServiceRuntimeConfig) appcore.ServiceRuntime {
			return appcore.ServiceRuntime{
				Engine: operation.Engine{Preflight: func(ctx context.Context) checks.Outcome {
					called = true
					_, hadDeadline = ctx.Deadline()
					return checks.Outcome{OK: true, Results: []checks.Result{{Check: "load", OK: true, Message: "sampled"}}}
				}},
				CheckDeps: checks.Deps{DefaultTimeout: time.Second},
			}
		},
		Env:        func(string) string { return "" },
		LoadConfig: config.Load,
		Stdout:     &stdout,
		Stderr:     &bytes.Buffer{},
	}

	if code := cliApp.Run(t.Context(), []string{"--config", global, "preflight", "web"}); code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d; stdout=%s", code, exitSuccess, stdout.String())
	}
	if !called {
		t.Fatal("prepared engine Preflight was not called")
	}
	if !hadDeadline {
		t.Fatal("prepared engine Preflight context has no deadline")
	}
	if got := stdout.String(); !strings.Contains(got, "OK   load: sampled") {
		t.Fatalf("stdout = %q, want prepared engine result", got)
	}
}

func TestPreflightReportsRequiredBuildIssue(t *testing.T) {
	root := t.TempDir()
	global := filepath.Join(root, "sermo.yml")
	mustWrite(t, global, `
paths:
  services: [ `+root+`/services ]
defaults:
  policy:
    cooldown: 5m
`)
	mustWrite(t, filepath.Join(root, "services", "apache-main.yml"), `
name: apache-main
service: apache2
preflight:
  broken:
    type: unknown
`)

	var stdout, stderr bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &stderr}
	code := app.Run(context.Background(), []string{"--config", global, "preflight", "apache-main"})
	if code != exitNotActive {
		t.Fatalf("Run() exit = %d, want %d; stdout=%s stderr=%s", code, exitNotActive, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "preflight apache-main: FAIL") {
		t.Fatalf("stdout = %q, want FAIL", stdout.String())
	}
	if !strings.Contains(stdout.String(), `unsupported type "unknown"`) {
		t.Fatalf("stdout = %q, want build issue", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want build issue reported once through the canonical outcome", stderr.String())
	}
}

func TestPreflightJSON(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "apache2")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	global := writePreflightConfig(t, bin)

	var stdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{}}
	code := app.Run(context.Background(), []string{"--config", global, "--json", "preflight", "apache-main"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	var got struct {
		Service string `json:"service"`
		OK      bool   `json:"ok"`
		Checks  []struct {
			Check string `json:"check"`
			OK    bool   `json:"ok"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json: %v (out=%s)", err, stdout.String())
	}
	if got.Service != "apache-main" || !got.OK || len(got.Checks) != 2 {
		t.Fatalf("unexpected JSON: %+v", got)
	}
}
