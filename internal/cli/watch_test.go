package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWatchStatus(t *testing.T) {
	for _, tc := range []struct {
		name     string
		state    string
		stateOK  bool
		detail   daemonWatchDetail
		detailOK bool
		args     []string
		want     string
	}{
		{
			name: "daemon state", state: "starting", stateOK: true,
			args: []string{"watch", "status", "storage-root"},
			want: "storage-root state=starting",
		},
		{
			name: "json", state: "failed", stateOK: true,
			args: []string{"--json", "watch", "status", "load"},
			want: `{"state":"failed","watch":"load"}`,
		},
		{
			name:     "raid readings",
			detail:   daemonWatchDetail{State: "failed", Readings: []daemonWatchReading{{Field: "raid_progress_pct", Label: "Rebuild progress", Value: "12.6%"}}},
			detailOK: true,
			args:     []string{"watch", "status", "raid-md0"},
			want:     "raid-md0 state=failed\n  Rebuild progress: 12.6%",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{}}
			app.FetchDaemonWatchState = func(context.Context, options, string) (string, bool) { return tc.state, tc.stateOK }
			app.FetchDaemonWatchDetail = func(context.Context, options, string) (daemonWatchDetail, bool) { return tc.detail, tc.detailOK }

			code := app.Run(context.Background(), tc.args)
			if code != exitSuccess {
				t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
			}
			if got := strings.TrimSpace(stdout.String()); got != tc.want {
				t.Fatalf("stdout = %q, want %q", got, tc.want)
			}
		})
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
