package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/state"
)

func TestDiagnoseReportsFindings(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "sermo.yml"), `
engine: { backend: auto, interval: 30s }
paths:
  catalog: [ `+root+`/daemons ]
  includes: [ `+root+`/enabled ]
  state: `+root+`/state
defaults: { policy: { cooldown: 5m } }
watches:
  load:
    check: { type: load, load5: { op: ">", value: 2 }, per_cpu: true }
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
paths: { includes: [ `+root+`/enabled ], state: `+root+`/state }
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

func TestDiagnoseCleanPrunesOnlyUnconfiguredMonitorState(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "sermo.yml"), `
engine: { backend: auto, interval: 30s }
paths:
  includes: [ `+root+`/enabled ]
  state: `+root+`/state
defaults: { policy: { cooldown: 5m } }
watches:
  load:
    check: { type: load, load5: { op: ">", value: 2 }, per_cpu: true }
`)
	mustWrite(t, filepath.Join(root, "enabled", "web.yml"), `
kind: service
name: web
service: { name: nginx }
policy: { cooldown: 5m }
checks:
  service: { type: service, expect: active }
`)
	global := filepath.Join(root, "sermo.yml")

	store, err := state.Open(filepath.Join(root, "state", state.Filename))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	if err := store.SetActive("web", true, state.SourceCLI); err != nil {
		t.Fatalf("SetActive(web): %v", err)
	}
	if err := store.SetActive("watch:load", true, state.SourceCLI); err != nil {
		t.Fatalf("SetActive(watch:load): %v", err)
	}
	if err := store.SetActive("ghost", false, state.SourceCLI); err != nil {
		t.Fatalf("SetActive(ghost): %v", err)
	}
	if err := store.RecordSLA("ghost", false, now); err != nil {
		t.Fatalf("RecordSLA(ghost): %v", err)
	}
	if err := store.RecordMeasurement("ghost", "http", 42, now); err != nil {
		t.Fatalf("RecordMeasurement(ghost): %v", err)
	}
	if err := store.RecordMetric("ghost", "http", "latency", 42, now); err != nil {
		t.Fatalf("RecordMetric(ghost): %v", err)
	}
	if err := store.RecordServiceMetric("ghost", "cpu", 42, now); err != nil {
		t.Fatalf("RecordServiceMetric(ghost): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close state: %v", err)
	}

	var stdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{}}
	if code := app.Run(context.Background(), []string{"--config", global, "diagnose"}); code != exitSuccess {
		t.Fatalf("diagnose before clean exit=%d output:\n%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), `target "ghost" which is no longer configured`) {
		t.Fatalf("diagnose before clean missing stale warning:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), `service "watch:load" which is no longer configured`) {
		t.Fatalf("diagnose before clean reported configured watch:\n%s", stdout.String())
	}

	stdout.Reset()
	if code := app.Run(context.Background(), []string{"--config", global, "diagnose", "clean"}); code != exitSuccess {
		t.Fatalf("diagnose clean exit=%d output:\n%s", code, stdout.String())
	}
	if got := stdout.String(); !strings.Contains(got, "ghost") || !strings.Contains(got, "1 row(s)") {
		t.Fatalf("diagnose clean output = %q, want ghost and 1 row", got)
	}

	store, err = state.Open(filepath.Join(root, "state", state.Filename))
	if err != nil {
		t.Fatalf("reopen state: %v", err)
	}
	if stat, err := store.ServiceMetricSummary("ghost", "cpu", time.Hour, now.Add(time.Minute)); err != nil || stat.Count != 1 {
		t.Fatalf("diagnose clean must keep runtime metrics, got %+v err=%v", stat, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close state: %v", err)
	}

	stdout.Reset()
	if code := app.Run(context.Background(), []string{"--config", global, "diagnose"}); code != exitSuccess {
		t.Fatalf("diagnose after clean exit=%d output:\n%s", code, stdout.String())
	}
	if strings.Contains(stdout.String(), "no longer configured") {
		t.Fatalf("diagnose after clean still reports stale data:\n%s", stdout.String())
	}
}
