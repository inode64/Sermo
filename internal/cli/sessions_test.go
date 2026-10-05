package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/web"
)

const sessionsTestKillPath = "/api/services/mysql/db-queries/long-queries/kill"

func sessionsTestInventory() web.SessionInventory {
	return web.SessionInventory{
		Sources: []web.SessionSource{
			{Kind: web.SessionKindDatabase, Service: "mysql", Check: "long-queries", State: web.SessionSourceAvailable},
			{Kind: web.SessionKindDatabase, Check: "reporting-db", State: web.SessionSourceUnavailable, Message: "dial tcp: connection refused"},
		},
		SSH: []web.SSHSession{{Service: "sshd", User: "root", Terminal: "pts/0", PID: 812, IdleSeconds: 90, CanClose: true}},
		Database: []web.DBQuerySession{
			{
				Service: "mysql", Watch: "long-queries", Engine: "mariadb", ID: 4242, User: "app", Database: "shop",
				ElapsedSeconds: 3723, Query: "SELECT * FROM orders WHERE note LIKE '%" + strings.Repeat("x", 80) + "%'",
				Long: true, Identity: "4242:991", CanKill: true,
			},
			{Service: "mysql", Watch: "long-queries", Engine: "mariadb", ID: 7, User: "report", ElapsedSeconds: 4, Query: "SELECT 1", Identity: "7:3"},
		},
	}
}

// sessionsTestDaemon serves the inventory, the generation HEAD and the kill
// route, recording each kill request's query string.
type sessionsTestDaemon struct {
	mu         sync.Mutex
	kills      []string
	killStatus int
	killResult web.ActionResult
}

func (d *sessionsTestDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodHead && r.URL.Path == web.APIPathWatches:
		w.Header().Set(web.HeaderGeneration, "7")
	case r.Method == http.MethodGet && r.URL.Path == web.APIPathSessions:
		writeDaemonAPITestJSON(w, sessionsTestInventory())
	case r.Method == http.MethodPost && r.URL.Path == sessionsTestKillPath:
		if r.Header.Get(web.HeaderCSRF) == "" || r.Header.Get(web.HeaderGeneration) != "7" {
			http.Error(w, "missing mutation headers", http.StatusPreconditionRequired)
			return
		}
		d.mu.Lock()
		d.kills = append(d.kills, r.URL.RawQuery)
		d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(d.killStatus)
		_ = json.NewEncoder(w).Encode(d.killResult)
	default:
		http.NotFound(w, r)
	}
}

func sessionsTestApp(t *testing.T, daemon http.Handler) (App, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	srv := httptest.NewServer(daemon)
	t.Cleanup(srv.Close)
	root, global, _ := daemonAPITestConfig(t, srv.URL, `
web:
  address: HOST
  port: PORT
paths:
  services: [SERVICES]
defaults:
  policy: { cooldown: 5m }
`)
	servicesDir := filepath.Join(root, "services")
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(servicesDir, "mysql.yml"), "name: mysql\nservice: mysql.service\n")
	mustWrite(t, filepath.Join(servicesDir, "sshd.yml"), "name: sshd\nservice: sshd.service\n")
	var stdout, stderr bytes.Buffer
	app := App{Env: func(string) string { return "" }, LoadConfig: config.Load, Stdout: &stdout, Stderr: &stderr}
	return app, &stdout, &stderr, global
}

func TestSessionsListPrintsEveryKindAndUnavailableSources(t *testing.T) {
	app, stdout, stderr, global := sessionsTestApp(t, &sessionsTestDaemon{})
	if code := app.Run(context.Background(), []string{"--config", global, "sessions"}); code != exitSuccess {
		t.Fatalf("sessions exit = %d, stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"SSH sessions:", "pts/0", "90s", "Database statements:", "4242", "1h 2m 3s", sessionsLongMarker, "shop"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "4242") && !strings.HasSuffix(line, sessionsTruncationEllipsis) {
			t.Errorf("long query not truncated: %q", line)
		}
	}
	if !strings.Contains(stderr.String(), "database reporting-db: unavailable — dial tcp: connection refused") {
		t.Errorf("stderr = %q, want the unavailable source", stderr.String())
	}
}

func TestSessionsListFiltersByServiceInJSON(t *testing.T) {
	app, stdout, stderr, global := sessionsTestApp(t, &sessionsTestDaemon{})
	if code := app.Run(context.Background(), []string{"--config", global, "--json", "sessions", "list", "mysql"}); code != exitSuccess {
		t.Fatalf("sessions exit = %d, stderr=%q", code, stderr.String())
	}
	var got web.SessionInventory
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", stdout.String(), err)
	}
	if len(got.SSH) != 0 || len(got.Database) != 2 || len(got.Sources) != 1 {
		t.Fatalf("filtered inventory = %+v, want only mysql rows and sources", got)
	}
	if !strings.Contains(stdout.String(), `"ssh":[]`) {
		t.Errorf("stdout = %q, want an empty ssh list rather than null", stdout.String())
	}
}

func TestSessionsListRejectsUnknownService(t *testing.T) {
	app, _, stderr, global := sessionsTestApp(t, &sessionsTestDaemon{})
	if code := app.Run(context.Background(), []string{"--config", global, "sessions", "nope"}); code != exitRuntimeError {
		t.Fatalf("sessions exit = %d, want %d", code, exitRuntimeError)
	}
	if !strings.Contains(stderr.String(), `unknown service "nope"`) {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestSessionsKillSendsTheListedIdentity(t *testing.T) {
	daemon := &sessionsTestDaemon{killStatus: http.StatusOK, killResult: web.ActionResult{OK: true, Message: "statement 4242 cancelled"}}
	app, stdout, stderr, global := sessionsTestApp(t, daemon)
	code := app.Run(context.Background(), []string{"--config", global, "sessions", "kill", "mysql", "long-queries", "4242", "--connection"})
	if code != exitSuccess {
		t.Fatalf("sessions kill exit = %d, stderr=%q", code, stderr.String())
	}
	if len(daemon.kills) != 1 || daemon.kills[0] != "id=4242&identity=4242%3A991&mode=connection" {
		t.Fatalf("kill requests = %q, want one with the listed identity and connection mode", daemon.kills)
	}
	if !strings.HasPrefix(stdout.String(), cliTextOK+" kill connection 4242 on mysql:long-queries:") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestSessionsKillRefusalExitsBlocked(t *testing.T) {
	daemon := &sessionsTestDaemon{killStatus: http.StatusConflict, killResult: web.ActionResult{Message: "the statement is no longer running; refresh the list"}}
	app, stdout, _, global := sessionsTestApp(t, daemon)
	code := app.Run(context.Background(), []string{"--config", global, "--json", "sessions", "kill", "mysql", "long-queries", "4242"})
	if code != exitBlocked {
		t.Fatalf("sessions kill exit = %d, want %d", code, exitBlocked)
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", stdout.String(), err)
	}
	if got[cliJSONKeyOK] != false || got[web.APIQueryMode] != checks.DBQueryKillModeQuery || !strings.Contains(got[cliJSONKeyMessage].(string), "no longer running") {
		t.Fatalf("json = %v", got)
	}
}

func TestSessionsKillRequiresAListedKillableStatement(t *testing.T) {
	daemon := &sessionsTestDaemon{killStatus: http.StatusOK, killResult: web.ActionResult{OK: true}}
	app, _, stderr, global := sessionsTestApp(t, daemon)
	for id, want := range map[string]string{"99": "not listed", "7": "cannot be killed"} {
		stderr.Reset()
		code := app.Run(context.Background(), []string{"--config", global, "sessions", "kill", "mysql", "long-queries", id})
		if code != exitRuntimeError || !strings.Contains(stderr.String(), want) {
			t.Errorf("id %s: exit=%d stderr=%q, want %q", id, code, stderr.String(), want)
		}
	}
	if len(daemon.kills) != 0 {
		t.Fatalf("kill requests = %q, want none", daemon.kills)
	}
}

func TestSessionsUsageErrors(t *testing.T) {
	app, _, _, global := sessionsTestApp(t, &sessionsTestDaemon{})
	for _, args := range [][]string{
		{"sessions", "kill", "mysql", "long-queries"},
		{"sessions", "kill", "mysql", "long-queries", "abc"},
		{"sessions", "mysql", "extra"},
		{"sessions", "mysql", "--connection"},
		{"status", "mysql", "--connection"},
	} {
		if code := app.Run(context.Background(), append([]string{"--config", global}, args...)); code != exitUsage {
			t.Errorf("%v exit = %d, want %d", args, code, exitUsage)
		}
	}
}
