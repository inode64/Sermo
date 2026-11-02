package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/web"
)

// refreshCall records what one RefreshDaemon call was asked.
type refreshCall struct {
	path     string
	deadline time.Duration
}

func refreshTestApp(t *testing.T, stdout, stderr *bytes.Buffer, calls *[]refreshCall, result web.ActionResult, err error) App {
	t.Helper()
	return hermeticApp(t, App{Env: func(string) string { return "" }, Stdout: stdout, Stderr: stderr,
		RefreshDaemon: func(ctx context.Context, _ *config.Config, path string) (web.ActionResult, error) {
			deadline, _ := ctx.Deadline()
			*calls = append(*calls, refreshCall{path: path, deadline: time.Until(deadline)})
			return result, err
		}})
}

func TestRefreshCommandsAskTheDaemonForTheirListing(t *testing.T) {
	global, _ := watchTestConfig(t)
	for _, tc := range []struct {
		args       []string
		wantPath   string
		wantPrefix string
	}{
		{[]string{"apps", "refresh"}, "/api/applications/refresh", "OK apps refresh: "},
		{[]string{"libs", "refresh"}, "/api/libraries/refresh", "OK libs refresh: "},
		{[]string{"watch", "refresh"}, "/api/watches/refresh", "OK watch refresh: "},
		{[]string{"watch", "refresh", "load", "svc:queue"}, "/api/watches/refresh?name=load&name=svc%3Aqueue", "OK watch refresh: "},
	} {
		var stdout, stderr bytes.Buffer
		var calls []refreshCall
		app := refreshTestApp(t, &stdout, &stderr, &calls, web.ActionResult{OK: true, Message: "refreshed: 2"}, nil)
		if code := app.Run(context.Background(), append([]string{"--config", global}, tc.args...)); code != exitSuccess {
			t.Fatalf("%v exit = %d stderr=%q", tc.args, code, stderr.String())
		}
		if len(calls) != 1 || calls[0].path != tc.wantPath {
			t.Fatalf("%v called %+v, want %s", tc.args, calls, tc.wantPath)
		}
		if got := stdout.String(); got != tc.wantPrefix+"refreshed: 2\n" {
			t.Fatalf("%v stdout = %q", tc.args, got)
		}
	}
}

// The daemon waits for the refreshed cycles within engine.operation_timeout,
// so sermoctl waits that long; --timeout replaces the wait.
func TestRefreshWaitsForTheOperationBudget(t *testing.T) {
	global, _ := watchTestConfig(t)
	for _, tc := range []struct {
		args     []string
		min, max time.Duration
	}{
		{[]string{"apps", "refresh"}, 90 * time.Second, 90*time.Second + daemonActionAnswerMargin},
		{[]string{"--timeout", "5s", "watch", "refresh"}, 4 * time.Second, 5 * time.Second},
	} {
		var stdout, stderr bytes.Buffer
		var calls []refreshCall
		app := refreshTestApp(t, &stdout, &stderr, &calls, web.ActionResult{OK: true}, nil)
		app.Run(context.Background(), append([]string{"--config", global}, tc.args...))
		if len(calls) != 1 || calls[0].deadline < tc.min || calls[0].deadline > tc.max {
			t.Fatalf("%v waited %+v, want %s..%s", tc.args, calls, tc.min, tc.max)
		}
	}
}

func TestRefreshPrintsAnIncompleteOutcomeAndFails(t *testing.T) {
	global, _ := watchTestConfig(t)
	const message = "watches refreshed: 3; not refreshed by the deadline: 1 (hdparm-sda)"
	var stdout, stderr bytes.Buffer
	var calls []refreshCall
	app := refreshTestApp(t, &stdout, &stderr, &calls, web.ActionResult{Message: message}, nil)
	if code := app.Run(context.Background(), []string{"--config", global, "watch", "refresh"}); code != exitNotActive {
		t.Fatalf("exit = %d, want %d", code, exitNotActive)
	}
	if got := stdout.String(); got != cliTextFail+" watch refresh: "+message+"\n" {
		t.Fatalf("stdout = %q", got)
	}

	stdout.Reset()
	app.Run(context.Background(), []string{"--config", global, "--json", "watch", "refresh"})
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json %q: %v", stdout.String(), err)
	}
	if got[cliJSONKeyOK] != false || got[cliJSONKeyMessage] != message {
		t.Fatalf("json = %v", got)
	}
}

func TestRefreshFailsWhenTheDaemonGivesNoOutcome(t *testing.T) {
	global, _ := watchTestConfig(t)
	var stdout, stderr bytes.Buffer
	var calls []refreshCall
	app := refreshTestApp(t, &stdout, &stderr, &calls, web.ActionResult{}, errors.New("talking to daemon web UI: connection refused"))
	if code := app.Run(context.Background(), []string{"--config", global, "libs", "refresh"}); code != exitRuntimeError {
		t.Fatalf("exit = %d, want %d", code, exitRuntimeError)
	}
	if !strings.Contains(stderr.String(), "libs refresh: talking to daemon web UI") || stdout.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// A refresh prints no listing, so a listing argument or flag it would ignore
// is a usage error rather than a silent no-op.
func TestListingRefreshTakesNoArguments(t *testing.T) {
	global, _ := watchTestConfig(t)
	for _, args := range [][]string{
		{"apps", "refresh", "all"},
		{"libs", "refresh", "--notify", "ops"},
		{"apps", "refresh", "--long"},
	} {
		var stdout, stderr bytes.Buffer
		var calls []refreshCall
		app := refreshTestApp(t, &stdout, &stderr, &calls, web.ActionResult{OK: true}, nil)
		if code := app.Run(context.Background(), append([]string{"--config", global}, args...)); code != exitUsage {
			t.Fatalf("%v exit = %d, want usage error", args, code)
		}
		if len(calls) != 0 {
			t.Fatalf("%v: a usage error asked the daemon: %+v", args, calls)
		}
	}
}

// A 409 is the daemon's answer to the refresh; any other rejection (here a
// rejected credential) is an error the command reports with its hint, and so
// is a 409 whose body is not an outcome at all.
func TestRefreshDaemonHTTPTellsAnOutcomeFromARejection(t *testing.T) {
	status := http.StatusConflict
	body := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/watches/refresh" || r.Header.Get(web.HeaderCSRF) != web.CSRFHeaderValue {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if body != "" {
			_, _ = w.Write([]byte(body))
			return
		}
		_ = json.NewEncoder(w).Encode(web.ActionResult{Message: "watches refreshed: 0; not scheduled: 1 (svc:queue)"})
	}))
	defer srv.Close()
	_, _, cfg := daemonAPITestConfig(t, srv.URL, `
web:
  address: HOST
  port: PORT
paths:
  watches: [WATCHES]
`)
	app := hermeticApp(t, App{Env: os.Getenv, LoadConfig: func(string, ...config.Option) (*config.Config, error) { return cfg, nil }})

	result, err := app.refreshDaemon(context.Background(), cfg, web.RefreshPath(web.APIPathWatches))
	if err != nil || result.OK || !strings.Contains(result.Message, "not scheduled") {
		t.Fatalf("409: result=%+v err=%v, want the outcome without an error", result, err)
	}
	status = http.StatusUnauthorized
	if _, err := app.refreshDaemon(context.Background(), cfg, web.RefreshPath(web.APIPathWatches)); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("401: err=%v, want a rejection error", err)
	}
	status, body = http.StatusConflict, "<html>proxy</html>"
	if _, err := app.refreshDaemon(context.Background(), cfg, web.RefreshPath(web.APIPathWatches)); err == nil || !strings.Contains(err.Error(), "decode refresh response") {
		t.Fatalf("undecodable 409: err=%v, want a decode error", err)
	}
}
