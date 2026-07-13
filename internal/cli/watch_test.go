package cli

import (
	"bytes"
	"context"
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
