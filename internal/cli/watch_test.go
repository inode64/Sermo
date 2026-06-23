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

	code := app.Run(context.Background(), []string{"--json", "watch", "status", "load"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	want := `{"state":"failed","watch":"load"}`
	if got := strings.TrimSpace(stdout.String()); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}
