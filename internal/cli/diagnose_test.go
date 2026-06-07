package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnoseReportsFindings(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "sermo.yml"), `
engine: { backend: auto, interval: 30s }
paths:
  profiles: [ `+root+`/profiles ]
  enabled: [ `+root+`/enabled ]
  state: `+root+`/state
defaults: { policy: { cooldown: 5m } }
`)
	mustWrite(t, filepath.Join(root, "enabled", "web.yml"), `
kind: service
name: web
service: { name: nginx }
policy: { cooldown: 5m }
checks:
  api: { type: tcp, host: 127.0.0.1, port: 80, interval: 40s }
`)
	global := filepath.Join(root, "sermo.yml")

	var stdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{}}
	code := app.Run(context.Background(), []string{"--config", global, "diagnose"})

	// only a warning (interval misalignment) -> exit 0
	if code != exitSuccess {
		t.Fatalf("diagnose exit = %d, want %d; output:\n%s", code, exitSuccess, stdout.String())
	}
	if !strings.Contains(stdout.String(), "not a multiple of the 30s resolution") {
		t.Fatalf("expected interval warning, got:\n%s", stdout.String())
	}
}

func TestDiagnoseInvalidConfigExitsNonZero(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "sermo.yml"), `
engine: { backend: auto, interval: 30s }
paths: { enabled: [ `+root+`/enabled ], state: `+root+`/state }
defaults: { policy: { cooldown: 5m } }
`)
	// a watch missing its required hook/notify -> config error finding
	mustWrite(t, filepath.Join(root, "enabled", "bad.yml"), `
kind: service
name: web
service: { name: nginx }
policy: { cooldown: 5m }
checks:
  api: { type: http }
`)
	global := filepath.Join(root, "sermo.yml")

	var stdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{}}
	code := app.Run(context.Background(), []string{"--config", global, "diagnose"})
	if code != exitConfigInvalid {
		t.Fatalf("diagnose exit = %d, want %d; output:\n%s", code, exitConfigInvalid, stdout.String())
	}
	if !strings.Contains(stdout.String(), "error") {
		t.Fatalf("expected an error finding, got:\n%s", stdout.String())
	}
}
