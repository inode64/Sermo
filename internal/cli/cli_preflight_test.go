package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePreflightConfig builds a service with preflight checks: a binary check at
// binPath and an optional file_exists at a missing path.
func writePreflightConfig(t *testing.T, binPath string) string {
	t.Helper()
	root := t.TempDir()
	global := filepath.Join(root, "sermo.yml")
	mustWrite(t, global, `
engine:
  default_timeout: 3s
paths:
  services: [ `+root+`/enabled ]
defaults:
  policy:
    cooldown: 5m
`)
	mustWrite(t, filepath.Join(root, "enabled", "apache-main.yml"), `
kind: service
name: apache-main
service: { name: apache2 }
variables:
  binary: `+binPath+`
preflight:
  binary:
    type: binary
    path: "${binary}"
  optional-flag:
    type: file_exists
    path: /definitely/missing/flag
    optional: true
`)
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

func TestPreflightFailsOnRequiredBuildWarning(t *testing.T) {
	root := t.TempDir()
	global := filepath.Join(root, "sermo.yml")
	mustWrite(t, global, `
paths:
  services: [ `+root+`/enabled ]
defaults:
  policy:
    cooldown: 5m
`)
	mustWrite(t, filepath.Join(root, "enabled", "apache-main.yml"), `
kind: service
name: apache-main
service: { name: apache2 }
preflight:
  cpu:
    type: metric
    name: cpu
    op: ">"
    value: "90"
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
	if !strings.Contains(stderr.String(), "metric check needs a metric source") {
		t.Fatalf("stderr = %q, want metric source warning", stderr.String())
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

func TestPreflightRequiresService(t *testing.T) {
	var stderr bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &bytes.Buffer{}, Stderr: &stderr}
	code := app.Run(context.Background(), []string{"preflight"})
	if code != exitUsage {
		t.Fatalf("Run() exit = %d, want %d", code, exitUsage)
	}
}
