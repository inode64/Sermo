package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/app"
	"sermo/internal/config"
	"sermo/internal/severity"
	"sermo/internal/web"
)

func TestManualOperationTimeout(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		config.SectionEngine: map[string]any{config.EngineKeyOperationTimeout: "19s"},
	}}}
	for _, tc := range []struct {
		name string
		opts options
		want time.Duration
	}{
		{name: "engine timeout", want: 19 * time.Second},
		{name: "explicit CLI override", opts: options{timeout: 7 * time.Second, timeoutSet: true}, want: 7 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := manualOperationTimeout(cfg, tc.opts); got != tc.want {
				t.Fatalf("manualOperationTimeout() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestWatchSilenceUsesDisplayedRevision(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status, code int
	}{
		{"current", http.StatusOK, exitSuccess},
		{"stale", http.StatusConflict, exitBlocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set(web.HeaderGeneration, "7")
				if r.Method != http.MethodPost {
					_, _ = w.Write([]byte(`{"generation":7}`))
					return
				}
				requests <- r.URL.Path + "?" + r.URL.Query().Get(web.APIQueryRevision) + ":" + r.Header.Get(web.HeaderGeneration) + ":" + r.Header.Get(web.HeaderCSRF)
				w.WriteHeader(tc.status)
				if tc.status == http.StatusOK {
					_, _ = w.Write([]byte(`{"ok":true,"message":"alarm silenced"}`))
				} else {
					_, _ = w.Write([]byte(`{"ok":false,"message":"watch alarm changed; refresh and try again"}`))
				}
			}))
			defer srv.Close()
			_, global, _ := daemonAPITestConfig(t, srv.URL, "web:\n  address: HOST\n  port: PORT\nwatches:\n  load: { check: { type: oom } }\n")
			var stdout, stderr bytes.Buffer
			a := hermeticApp(t, App{Stdout: &stdout, Stderr: &stderr,
				FetchDaemonWatchDetail: func(context.Context, *config.Config, string) (web.Watch, bool) {
					return web.Watch{Alarm: web.WatchAlarm{Active: true, Revision: "observed-revision"}}, true
				},
			})
			code := a.Run(t.Context(), []string{"--config", global, "watch", "silence", "load"})
			if code != tc.code {
				t.Fatalf("exit=%d stdout=%s stderr=%s", code, &stdout, &stderr)
			}
			select {
			case got := <-requests:
				if got != "/api/watches/load/silence?observed-revision:7:1" {
					t.Fatalf("request=%s", got)
				}
			default:
				t.Fatal("silence request missing")
			}
		})
	}
}

func TestWatchStatus(t *testing.T) {
	global := filepath.Join(t.TempDir(), "sermo.yml")
	mustWrite(t, global, `defaults:
  policy: { cooldown: 5m }
watches:
  storage-root: { check: { type: oom } }
  load: { check: { type: oom } }
  raid-md0: { check: { type: oom } }
  hdparm-sdd: { check: { type: oom } }
`)
	for _, tc := range []struct {
		name     string
		detail   web.Watch
		detailOK bool
		args     []string
		want     string
		wantCode int
		wantErr  string
	}{
		{
			name: "daemon state", detail: web.Watch{State: "starting"}, detailOK: true,
			args: []string{"watch", "status", "storage-root"},
			want: "storage-root state=starting",
		},
		{
			name: "json", detail: web.Watch{State: "failed"}, detailOK: true,
			args: []string{"--json", "watch", "status", "load"},
			want: `{"state":"failed","watch":"load"}`,
		},
		{
			name:     "check times",
			detail:   web.Watch{State: "ok", LastCheckedAt: "2026-11-02T10:00:00Z", NextCheckAt: "2026-11-02T12:00:00Z"},
			detailOK: true,
			args:     []string{"watch", "status", "load"},
			want:     "load state=ok\n  Last checked: 2026-11-02T10:00:00Z\n  Next check: 2026-11-02T12:00:00Z",
		},
		{
			name:     "check times json",
			detail:   web.Watch{State: "ok", LastCheckedAt: "2026-11-02T10:00:00Z", NextCheckAt: "2026-11-02T12:00:00Z"},
			detailOK: true,
			args:     []string{"--json", "watch", "status", "load"},
			want:     `{"last_checked_at":"2026-11-02T10:00:00Z","next_check_at":"2026-11-02T12:00:00Z","state":"ok","watch":"load"}`,
		},
		{
			// Only the daemon observes watches: without it the answer is
			// unknown and non-zero, never a false ok.
			name:     "daemon unavailable",
			args:     []string{"watch", "status", "load"},
			want:     "load state=unknown",
			wantCode: exitRuntimeError,
			wantErr:  "sermod did not report its state",
		},
		{
			name:     "daemon unavailable json",
			args:     []string{"--json", "watch", "status", "load"},
			want:     `{"state":"unknown","watch":"load"}`,
			wantCode: exitRuntimeError,
		},
		{
			name:     "unknown watch",
			detail:   web.Watch{State: "ok"},
			detailOK: true,
			args:     []string{"watch", "status", "nosuch"},
			wantCode: exitRuntimeError,
			wantErr:  `unknown watch "nosuch"`,
		},
		{
			name:     "raid readings",
			detail:   web.Watch{State: "failed", Readings: []web.WatchReading{{Field: "raid_progress_pct", Label: "Rebuild progress", Value: "12.6%"}}},
			detailOK: true,
			args:     []string{"watch", "status", "raid-md0"},
			want:     "raid-md0 state=failed\n  Rebuild progress: 12.6%",
		},
		{
			// An advisory reports its reason through Warning rather than Error;
			// printing only Error left the operator an empty labelled row.
			name: "advisory reading",
			detail: web.Watch{State: "warning", Readings: []web.WatchReading{
				{Field: "warning", Label: "Warning", Warning: "hdparm /dev/sdd read=0.4 MB/s"},
			}},
			detailOK: true,
			args:     []string{"watch", "status", "hdparm-sdd"},
			want:     "hdparm-sdd state=warning\n  Warning: hdparm /dev/sdd read=0.4 MB/s",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			app := hermeticApp(t, App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &stderr,
				FetchDaemonWatchDetail: func(context.Context, *config.Config, string) (web.Watch, bool) { return tc.detail, tc.detailOK }})

			code := app.Run(context.Background(), append([]string{"--config", global}, tc.args...))
			if code != tc.wantCode {
				t.Fatalf("Run() exit = %d, want %d (stderr %q)", code, tc.wantCode, stderr.String())
			}
			if got := strings.TrimSpace(stdout.String()); got != tc.want {
				t.Fatalf("stdout = %q, want %q", got, tc.want)
			}
			if !strings.Contains(stderr.String(), tc.wantErr) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tc.wantErr)
			}
		})
	}
}

func TestWatchStatusRejectsMissingConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := hermeticApp(t, App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &stderr,
		FetchDaemonWatchDetail: func(context.Context, *config.Config, string) (web.Watch, bool) {
			t.Fatal("daemon queried without a config")
			return web.Watch{}, false
		}})
	code := app.Run(context.Background(), []string{"--config", filepath.Join(t.TempDir(), "missing.yml"), "watch", "status", "x"})
	if code != exitRuntimeError || stdout.Len() != 0 || !strings.Contains(stderr.String(), "load config failed") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

// A probe of a watch graded an advisory is not a failure. The daemon still
// answers 409 — the condition did fire — but announcing that as "FAIL" would
// contradict the amber the same result gets in the dashboard.
func TestWatchProbeRendersAnAdvisoryAsAWarning(t *testing.T) {
	global, watches := watchTestConfig(t)
	mustWrite(t, filepath.Join(watches, "hdparm-sdd.yml"),
		"name: hdparm-sdd\nseverity: warning\ncheck:\n  type: hdparm\n  device: /dev/sdd\n  read: { op: \"<\", value: 20 }\n")

	var stdout bytes.Buffer
	app := hermeticApp(t, App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{},
		ProbeDaemonWatch: func(context.Context, *config.Config, string) (web.ActionResult, error) {
			return web.ActionResult{
				Message:  "hdparm /dev/sdd read=0.4 MB/s",
				Severity: string(severity.Warning),
				Readings: []web.WatchReading{{Field: "warning", Label: "Warning", Warning: "hdparm /dev/sdd read=0.4 MB/s"}},
			}, errors.New("probe failed (409): hdparm /dev/sdd read=0.4 MB/s")
		}})
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
	global, watches := watchTestConfig(t)
	mustWrite(t, filepath.Join(watches, "disk-speed.yml"), "name: disk-speed\ncheck:\n  type: hdparm\n  device: /dev/sda\n  read: { op: \">\", value: 100 }\n")

	var stdout bytes.Buffer
	called := false
	app := hermeticApp(t, App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{},
		ProbeDaemonWatch: func(_ context.Context, _ *config.Config, watch string) (web.ActionResult, error) {
			called = watch == "disk-speed"
			return web.ActionResult{OK: true, Message: "hdparm /dev/sda read=166.67 MB/s", Readings: []web.WatchReading{{Field: "read", Label: "Read", Value: "167 MB/s"}}}, nil
		}})
	if code := app.Run(context.Background(), []string{"--config", global, "watch", "probe", "disk-speed"}); code != exitSuccess {
		t.Fatalf("watch probe exit = %d, stderr=%q", code, app.Stderr)
	}
	if !called || !strings.Contains(stdout.String(), "Read: 167 MB/s") {
		t.Fatalf("daemon probe called=%v stdout=%q", called, stdout.String())
	}
}

// A probe runs under the check's own budget in the daemon, which can far exceed
// the web client bound, so sermoctl waits that long; --timeout replaces the wait.
func TestWatchProbeWaitsForTheCheckBudget(t *testing.T) {
	const hdparm = "name: disk-speed\ncheck:\n  type: hdparm\n  device: /dev/sda\n  timeout: 30s\n  read: { op: \">\", value: 100 }\n"
	const hdparmDefault = "name: disk-speed\ncheck:\n  type: hdparm\n  device: /dev/sda\n  read: { op: \">\", value: 100 }\n"
	const diskio = "name: disk-speed\ncheck:\n  type: diskio\n  device: sda\n  timeout: 30s\n"
	for _, tc := range []struct {
		name  string
		watch string
		args  []string
		want  time.Duration
	}{
		{name: "the check's timeout", watch: hdparm, want: 30*time.Second + daemonActionAnswerMargin},
		{name: "the engine default", watch: hdparmDefault, want: app.DefaultEngineCheckTimeout + daemonActionAnswerMargin},
		{name: "both disk I/O samples and their pause", watch: diskio, want: 62*time.Second + daemonActionAnswerMargin},
		{name: "an explicit --timeout", watch: hdparm, args: []string{"--timeout", "3s"}, want: 3 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			global, watches := watchTestConfig(t)
			mustWrite(t, filepath.Join(watches, "disk-speed.yml"), tc.watch)

			var left time.Duration
			application := hermeticApp(t, App{Env: func(string) string { return "" }, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
				ProbeDaemonWatch: func(ctx context.Context, _ *config.Config, _ string) (web.ActionResult, error) {
					if deadline, ok := ctx.Deadline(); ok {
						left = time.Until(deadline)
					}
					return web.ActionResult{OK: true}, nil
				}})
			args := append([]string{"--config", global, "watch", "probe", "disk-speed"}, tc.args...)
			if code := application.Run(context.Background(), args); code != exitSuccess {
				t.Fatalf("watch probe exit = %d", code)
			}
			if left <= max(tc.want-10*time.Second, 0) || left > tc.want {
				t.Fatalf("probe deadline in %s, want %s", left, tc.want)
			}
		})
	}
}

// --timeout must really bound the wait on a daemon that does not answer.
func TestWatchProbeTimeoutBoundsTheRequest(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			<-release
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	defer close(release)
	root, global, _ := daemonAPITestConfig(t, srv.URL, `
web:
  address: HOST
  port: PORT
paths:
  watches: [WATCHES]
`)
	watches := filepath.Join(root, "watches")
	if err := os.MkdirAll(watches, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(watches, "disk-speed.yml"), "name: disk-speed\ncheck:\n  type: hdparm\n  device: /dev/sda\n  read: { op: \">\", value: 100 }\n")

	var stderr bytes.Buffer
	application := hermeticApp(t, App{Env: func(string) string { return "" }, LoadConfig: testConfigLoader(t), Stdout: &bytes.Buffer{}, Stderr: &stderr})
	started := time.Now()
	code := application.Run(context.Background(), []string{"--config", global, "--timeout", "50ms", "watch", "probe", "disk-speed"})
	if code != exitRuntimeError || !strings.Contains(stderr.String(), "deadline exceeded") {
		t.Fatalf("exit = %d, stderr = %q, want a deadline failure", code, stderr.String())
	}
	if took := time.Since(started); took >= daemonWebClientTimeout {
		t.Fatalf("watch probe took %s under --timeout 50ms", took)
	}
}

// A failing sample prints at its grade — FAIL for an outage, CRIT for a
// critical one — with its readings, and exits 1; only a probe that returned no
// result is reported as a command failure.
func TestWatchProbeRendersTheGradeOfAFailingSample(t *testing.T) {
	global, watches := watchTestConfig(t)
	mustWrite(t, filepath.Join(watches, "hdparm-sdd.yml"),
		"name: hdparm-sdd\ncheck:\n  type: hdparm\n  device: /dev/sdd\n  read: { op: \"<\", value: 20 }\n")
	for level, label := range map[severity.Level]string{severity.Error: cliTextFail, severity.Critical: cliTextCrit} {
		var stdout bytes.Buffer
		app := hermeticApp(t, App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{},
			ProbeDaemonWatch: func(context.Context, *config.Config, string) (web.ActionResult, error) {
				return web.ActionResult{Message: "hdparm /dev/sdd read=0.4 MB/s", Severity: string(level)},
					errors.New("probe failed (409): hdparm /dev/sdd read=0.4 MB/s")
			}})
		code := app.Run(context.Background(), []string{"--config", global, "watch", "probe", "hdparm-sdd"})
		if code != exitNotActive || !strings.HasPrefix(stdout.String(), label+" watch hdparm-sdd:") {
			t.Fatalf("%s: exit=%d stdout=%q, want exit %d leading with %s", level, code, stdout.String(), exitNotActive, label)
		}
	}
	var stderr bytes.Buffer
	app := hermeticApp(t, App{Env: func(string) string { return "" }, Stdout: &bytes.Buffer{}, Stderr: &stderr,
		ProbeDaemonWatch: func(context.Context, *config.Config, string) (web.ActionResult, error) {
			return web.ActionResult{}, errors.New("talking to daemon web UI: connection refused")
		}})
	if code := app.Run(context.Background(), []string{"--config", global, "watch", "probe", "hdparm-sdd"}); code == exitSuccess || !strings.Contains(stderr.String(), "connection refused") {
		t.Fatalf("transport failure exit=%d stderr=%q, want a command failure", code, stderr.String())
	}
}

// watchTestConfig writes a minimal global config whose watches directory the
// test fills, and returns both paths.
func watchTestConfig(t *testing.T) (global, watches string) {
	t.Helper()
	root := t.TempDir()
	watches = filepath.Join(root, "watches")
	if err := os.Mkdir(watches, 0o755); err != nil {
		t.Fatal(err)
	}
	global = filepath.Join(root, "sermo.yml")
	mustWrite(t, global, "paths:\n  watches: ["+watches+"]\ndefaults:\n  policy: { cooldown: 5m }\n")
	return global, watches
}
