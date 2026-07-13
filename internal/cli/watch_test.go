package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWatchStatusUsesDaemonStateWhenAvailable(t *testing.T) {
	var stdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{}}
	app.FetchDaemonWatchState = func(context.Context, options, string) (string, bool) {
		return "starting", true
	}
	app.FetchDaemonWatchDetail = func(context.Context, options, string) (daemonWatchDetail, bool) {
		return daemonWatchDetail{}, false
	}

	code := app.Run(context.Background(), []string{"watch", "status", "storage-root"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	if got := strings.TrimSpace(stdout.String()); got != "storage-root state=starting" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestWatchStatusJSON(t *testing.T) {
	var stdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{}}
	app.FetchDaemonWatchState = func(context.Context, options, string) (string, bool) {
		return "failed", true
	}
	app.FetchDaemonWatchDetail = func(context.Context, options, string) (daemonWatchDetail, bool) {
		return daemonWatchDetail{}, false
	}

	code := app.Run(context.Background(), []string{"--json", "watch", "status", "load"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	want := `{"state":"failed","watch":"load"}`
	if got := strings.TrimSpace(stdout.String()); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestWatchStatusShowsDaemonRaidReadings(t *testing.T) {
	var stdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{}}
	app.FetchDaemonWatchDetail = func(context.Context, options, string) (daemonWatchDetail, bool) {
		return daemonWatchDetail{State: "failed", Readings: []daemonWatchReading{{Field: "raid_progress_pct", Label: "Rebuild progress", Value: "12.6%"}}}, true
	}
	app.FetchDaemonWatchState = func(context.Context, options, string) (string, bool) { return "", false }
	code := app.Run(context.Background(), []string{"watch", "status", "raid-md0"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	if got := strings.TrimSpace(stdout.String()); got != "raid-md0 state=failed\n  Rebuild progress: 12.6%" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestWatchProbeUsesDaemonAndSupportsHdparm(t *testing.T) {
	root := t.TempDir()
	watches := filepath.Join(root, "watches")
	if err := os.Mkdir(watches, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	mustWrite(t, global, "paths:\n  watches: ["+watches+"]\ndefaults:\n  policy: { cooldown: 5m }\n")
	mustWrite(t, filepath.Join(watches, "disk-speed.yml"), "name: disk-speed\ncheck:\n  type: hdparm\n  device: /dev/sda\n  read: { op: \">\", value: 100 }\n")

	var stdout bytes.Buffer
	called := false
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{}}
	app.ProbeDaemonWatch = func(_ context.Context, _ options, watch string) (daemonWatchProbe, error) {
		called = watch == "disk-speed"
		return daemonWatchProbe{OK: true, Message: "hdparm /dev/sda read=166.67 MB/s", Readings: []daemonWatchReading{{Field: "read", Label: "Read", Value: "167 MB/s"}}}, nil
	}
	if code := app.Run(context.Background(), []string{"--config", global, "watch", "probe", "disk-speed"}); code != exitSuccess {
		t.Fatalf("watch probe exit = %d, stderr=%q", code, app.Stderr)
	}
	if !called || !strings.Contains(stdout.String(), "Read: 167 MB/s") {
		t.Fatalf("daemon probe called=%v stdout=%q", called, stdout.String())
	}
}
