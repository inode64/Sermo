package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sermo/internal/checks"
)

func TestWatchStatus(t *testing.T) {
	for _, tc := range []struct {
		name     string
		detail   daemonWatchDetail
		detailOK bool
		args     []string
		want     string
	}{
		{
			name: "daemon state", detail: daemonWatchDetail{State: "starting"}, detailOK: true,
			args: []string{"watch", "status", "storage-root"},
			want: "storage-root state=starting",
		},
		{
			name: "json", detail: daemonWatchDetail{State: "failed"}, detailOK: true,
			args: []string{"--json", "watch", "status", "load"},
			want: `{"state":"failed","watch":"load"}`,
		},
		{
			name: "daemon unavailable",
			args: []string{"watch", "status", "load"},
			want: "load state=ok",
		},
		{
			name:     "raid readings",
			detail:   daemonWatchDetail{State: "failed", Readings: []daemonWatchReading{{Field: "raid_progress_pct", Label: "Rebuild progress", Value: "12.6%"}}},
			detailOK: true,
			args:     []string{"watch", "status", "raid-md0"},
			want:     "raid-md0 state=failed\n  Rebuild progress: 12.6%",
		},
		{
			// An advisory reports its reason through Warning rather than Error;
			// printing only Error left the operator an empty labelled row.
			name: "advisory reading",
			detail: daemonWatchDetail{State: "warning", Readings: []daemonWatchReading{
				{Field: "warning", Label: "Warning", Warning: "hdparm /dev/sdd read=0.4 MB/s"},
			}},
			detailOK: true,
			args:     []string{"watch", "status", "hdparm-sdd"},
			want:     "hdparm-sdd state=warning\n  Warning: hdparm /dev/sdd read=0.4 MB/s",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{},
				FetchDaemonWatchDetail: func(context.Context, options, string) (daemonWatchDetail, bool) { return tc.detail, tc.detailOK }}

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

// A probe of a watch graded an advisory is not a failure. The daemon still
// answers 409 — the condition did fire — but announcing that as "FAIL" would
// contradict the amber the same result gets in the dashboard.
func TestWatchProbeRendersAnAdvisoryAsAWarning(t *testing.T) {
	root := t.TempDir()
	watches := filepath.Join(root, "watches")
	if err := os.Mkdir(watches, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	mustWrite(t, global, "paths:\n  watches: ["+watches+"]\ndefaults:\n  policy: { cooldown: 5m }\n")
	mustWrite(t, filepath.Join(watches, "hdparm-sdd.yml"),
		"name: hdparm-sdd\nseverity: warning\ncheck:\n  type: hdparm\n  device: /dev/sdd\n  read: { op: \"<\", value: 20 }\n")

	var stdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{},
		ProbeDaemonWatch: func(context.Context, options, string) (daemonWatchProbe, error) {
			return daemonWatchProbe{
				Message:  "hdparm /dev/sdd read=0.4 MB/s",
				Severity: checks.SeverityWarning,
				Readings: []daemonWatchReading{{Field: "warning", Label: "Warning", Warning: "hdparm /dev/sdd read=0.4 MB/s"}},
			}, errors.New("probe failed (409): hdparm /dev/sdd read=0.4 MB/s")
		}}
	app.Run(context.Background(), []string{"--config", global, "watch", "probe", "hdparm-sdd"})
	out := stdout.String()
	if !strings.HasPrefix(out, cliTextWarn+" watch hdparm-sdd:") {
		t.Fatalf("stdout = %q, want it to lead with %s", out, cliTextWarn)
	}
	if strings.Contains(out, cliTextFail) {
		t.Errorf("stdout = %q, want no %s for an advisory", out, cliTextFail)
	}
	if !strings.Contains(out, "Warning: hdparm /dev/sdd read=0.4 MB/s") {
		t.Errorf("stdout = %q, want the advisory reading", out)
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
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{},
		ProbeDaemonWatch: func(_ context.Context, _ options, watch string) (daemonWatchProbe, error) {
			called = watch == "disk-speed"
			return daemonWatchProbe{OK: true, Message: "hdparm /dev/sda read=166.67 MB/s", Readings: []daemonWatchReading{{Field: "read", Label: "Read", Value: "167 MB/s"}}}, nil
		}}
	if code := app.Run(context.Background(), []string{"--config", global, "watch", "probe", "disk-speed"}); code != exitSuccess {
		t.Fatalf("watch probe exit = %d, stderr=%q", code, app.Stderr)
	}
	if !called || !strings.Contains(stdout.String(), "Read: 167 MB/s") {
		t.Fatalf("daemon probe called=%v stdout=%q", called, stdout.String())
	}
}
