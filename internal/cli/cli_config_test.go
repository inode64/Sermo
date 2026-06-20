package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "sermo.yml"), `
engine:
  backend: auto
paths:
  catalog: [ `+root+`/daemons ]
  services: [ `+root+`/enabled ]
defaults:
  policy:
    cooldown: 5m
`)
	mustWrite(t, filepath.Join(root, "enabled", "redis-main.yml"), `
kind: service
name: redis-main
service: redis
variables:
  port: 6379
checks:
  ping: { type: tcp, port: "${port}" }
`)
	return filepath.Join(root, "sermo.yml")
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConfigValidateOK(t *testing.T) {
	global := writeTempConfig(t)
	var stdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{}}

	code := app.Run(context.Background(), []string{"--config", global, "config", "validate"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	if got := strings.TrimSpace(stdout.String()); got != "OK" {
		t.Fatalf("stdout = %q, want OK", got)
	}
}

func TestConfigRejectsRemovedSubcommands(t *testing.T) {
	global := writeTempConfig(t)
	tests := []struct {
		name string
		args []string
	}{
		{name: "render", args: []string{"config", "render", "redis-main"}},
		{name: "diff", args: []string{"config", "diff", "redis-main", "redis-alt"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			app := App{Env: func(string) string { return "" }, Stdout: &bytes.Buffer{}, Stderr: &stderr}

			code := app.Run(context.Background(), append([]string{"--config", global}, tc.args...))
			if code != exitUsage {
				t.Fatalf("Run() exit = %d, want %d", code, exitUsage)
			}
			if !strings.Contains(stderr.String(), "unknown config subcommand") {
				t.Fatalf("stderr = %q, want unknown config subcommand", stderr.String())
			}
		})
	}
}

func TestConfigValidateReportsErrors(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "sermo.yml"), `
paths:
  services: [ `+root+`/enabled ]
defaults:
  policy:
    cooldown: 5m
`)
	mustWrite(t, filepath.Join(root, "enabled", "bad.yml"), `
kind: service
name: bad
checks:
  http: { type: http, url: "http://${missing}/" }
`)
	global := filepath.Join(root, "sermo.yml")

	var stdout, stderr bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &stderr}

	code := app.Run(context.Background(), []string{"--config", global, "config", "validate"})
	if code != exitConfigInvalid {
		t.Fatalf("Run() exit = %d, want %d", code, exitConfigInvalid)
	}
	if !strings.Contains(stderr.String(), "ERROR bad:") {
		t.Fatalf("stderr = %q, want ERROR bad", stderr.String())
	}
}
