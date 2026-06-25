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
  services: [ `+root+`/services ]
defaults:
  policy:
    cooldown: 5m
`)
	mustWrite(t, filepath.Join(root, "services", "redis-main.yml"), `
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

func TestConfigValidateReportsErrors(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "sermo.yml"), `
paths:
  services: [ `+root+`/services ]
defaults:
  policy:
    cooldown: 5m
`)
	mustWrite(t, filepath.Join(root, "services", "bad.yml"), `
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
