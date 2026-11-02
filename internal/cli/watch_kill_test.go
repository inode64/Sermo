package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"sermo/internal/web"
)

const watchKillTestWatch = "watch-unowned-processes"

// watchKillTestDaemon serves the watch listing, the generation HEAD and the
// kill route of one listed process, recording each kill's path and query.
type watchKillTestDaemon struct {
	mu         sync.Mutex
	kills      []string
	killStatus int
	killResult web.ActionResult
}

func (d *watchKillTestDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodHead && r.URL.Path == web.APIPathWatches:
		w.Header().Set(web.HeaderGeneration, "7")
	case r.Method == http.MethodGet && r.URL.Path == web.APIPathWatches:
		writeDaemonAPITestJSON(w, []web.Watch{{
			Name: watchKillTestWatch,
			Processes: []web.WatchProcess{
				{PID: 6593, StartTicks: 10381, User: "root", ExePrevious: "/usr/bin/screen-4.9.1", CanKill: true},
				{PID: 7000, StartTicks: 70, User: "root", KillReason: "executable is unresolved"},
			},
		}})
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, web.APIPathWatches+"/"):
		if r.Header.Get(web.HeaderCSRF) == "" || r.Header.Get(web.HeaderGeneration) != "7" {
			http.Error(w, "missing mutation headers", http.StatusPreconditionRequired)
			return
		}
		d.mu.Lock()
		d.kills = append(d.kills, r.URL.Path+"?"+r.URL.RawQuery)
		d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(d.killStatus)
		_ = json.NewEncoder(w).Encode(d.killResult)
	default:
		http.NotFound(w, r)
	}
}

func watchKillTestApp(t *testing.T, daemon http.Handler) (App, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	srv := httptest.NewServer(daemon)
	t.Cleanup(srv.Close)
	_, global, _ := daemonAPITestConfig(t, srv.URL, `
web:
  address: HOST
  port: PORT
defaults:
  policy: { cooldown: 5m }
`)
	var stdout, stderr bytes.Buffer
	app := hermeticApp(t, App{Env: func(string) string { return "" }, LoadConfig: testConfigLoader(t), Stdout: &stdout, Stderr: &stderr})
	return app, &stdout, &stderr, global
}

func TestWatchKillSendsTheListedIncarnation(t *testing.T) {
	daemon := &watchKillTestDaemon{killStatus: http.StatusOK, killResult: web.ActionResult{OK: true, Message: "sent SIGTERM to pid 6593"}}
	app, stdout, stderr, global := watchKillTestApp(t, daemon)
	if code := app.Run(context.Background(), []string{"--config", global, "watch", "kill", watchKillTestWatch, "6593", "--escalate"}); code != exitSuccess {
		t.Fatalf("watch kill exit = %d, stderr=%q", code, stderr.String())
	}
	want := web.WatchProcessKillPath(watchKillTestWatch, 6593) + "?escalate=true&start_ticks=10381"
	if len(daemon.kills) != 1 || daemon.kills[0] != want {
		t.Fatalf("kill requests = %q, want %q", daemon.kills, want)
	}
	if !strings.HasPrefix(stdout.String(), cliTextOK+" kill pid 6593 on "+watchKillTestWatch+": sent SIGTERM") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestWatchKillRefusalExitsBlocked(t *testing.T) {
	daemon := &watchKillTestDaemon{killStatus: http.StatusConflict, killResult: web.ActionResult{Message: "pid 6593 is now another process; refresh and try again"}}
	app, stdout, _, global := watchKillTestApp(t, daemon)
	code := app.Run(context.Background(), []string{"--config", global, "--json", "watch", "kill", watchKillTestWatch, "6593"})
	if code != exitBlocked {
		t.Fatalf("watch kill exit = %d, want %d", code, exitBlocked)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", stdout.String(), err)
	}
	if got[cliJSONKeyOK] != false || got[cliJSONKeyPID] != float64(6593) || !strings.Contains(got[cliJSONKeyMessage].(string), "another process") {
		t.Fatalf("json = %v", got)
	}
}

// Only a listed, killable row reaches the daemon: anything else is refused with
// the reason the listing gives, and nothing is posted.
func TestWatchKillRequiresAListedKillableProcess(t *testing.T) {
	daemon := &watchKillTestDaemon{killStatus: http.StatusOK, killResult: web.ActionResult{OK: true}}
	app, _, stderr, global := watchKillTestApp(t, daemon)
	for _, tc := range []struct{ watch, pid, want string }{
		{watchKillTestWatch, "99", "not listed"},
		{watchKillTestWatch, "7000", "cannot be killed: executable is unresolved"},
		{"nope", "6593", `lists no watch "nope"`},
	} {
		stderr.Reset()
		code := app.Run(context.Background(), []string{"--config", global, "watch", "kill", tc.watch, tc.pid})
		if code != exitRuntimeError || !strings.Contains(stderr.String(), tc.want) {
			t.Errorf("%s %s: exit=%d stderr=%q, want %q", tc.watch, tc.pid, code, stderr.String(), tc.want)
		}
	}
	if len(daemon.kills) != 0 {
		t.Fatalf("kill requests = %q, want none", daemon.kills)
	}
}

func TestWatchKillUsageErrors(t *testing.T) {
	app, _, _, global := watchKillTestApp(t, &watchKillTestDaemon{})
	for _, args := range [][]string{
		{"watch", "kill", watchKillTestWatch},
		{"watch", "kill", watchKillTestWatch, "abc"},
		{"watch", "kill", watchKillTestWatch, "0"},
		{"status", "x", "--escalate"},
	} {
		if code := app.Run(context.Background(), append([]string{"--config", global}, args...)); code != exitUsage {
			t.Errorf("%v: exit = %d, want %d", args, code, exitUsage)
		}
	}
}
