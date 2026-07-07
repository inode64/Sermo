package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sermoapp "sermo/internal/app"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
)

func TestMonitorUnmonitorCommand(t *testing.T) {
	root := t.TempDir()
	catalogDir := filepath.Join(root, "catalog")
	catalogServicesDir := filepath.Join(catalogDir, "services")
	servicesDir := filepath.Join(root, "services")
	runDir := filepath.Join(root, "run")
	stateDir := filepath.Join(root, "state")
	for _, d := range []string{catalogServicesDir, servicesDir, runDir, stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(catalogServicesDir, "nginx.yml"), "name: nginx\nservice: nginx\n")
	write(filepath.Join(servicesDir, "web.yml"), "name: web\nuses: nginx\n")
	write(filepath.Join(root, "sermo.yml"), fmt.Sprintf(`
engine: { backend: auto }
paths: { services: [ %s ], runtime: %s, state: %s }
defaults: { policy: { cooldown: 5m } }
`, servicesDir, runDir, stateDir))
	global := filepath.Join(root, "sermo.yml")

	run := func(args ...string) int {
		var out bytes.Buffer
		app := App{Env: func(string) string { return "" }, Stdout: &out, Stderr: &bytes.Buffer{}, LoadConfig: testLoadConfigWithCatalog(catalogDir)}
		return app.Run(context.Background(), append([]string{"--config", global}, args...))
	}

	paused := func(service string) bool {
		store, err := state.Open(filepath.Join(stateDir, state.Filename))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		active, found, err := store.Active(service)
		if err != nil {
			t.Fatal(err)
		}
		return found && !active
	}

	if code := run("unmonitor", "web"); code != exitSuccess {
		t.Fatalf("unmonitor exit = %d", code)
	}
	if !paused("web") {
		t.Error("web should be paused after unmonitor")
	}

	if code := run("monitor", "web"); code != exitSuccess {
		t.Fatalf("monitor exit = %d", code)
	}
	if paused("web") {
		t.Error("web should be resumed after monitor")
	}

	if code := run("unmonitor", "ghost"); code == exitSuccess {
		t.Error("unmonitor of unknown service should fail")
	}
}

func TestWatchMonitorUnmonitorCommand(t *testing.T) {
	root := t.TempDir()
	servicesDir := filepath.Join(root, "services")
	runDir := filepath.Join(root, "run")
	stateDir := filepath.Join(root, "state")
	for _, d := range []string{servicesDir, runDir, stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A service that declares the embedded watch, so the CLI's known-watch check
	// accepts "mail-queue:deferred-backlog".
	if err := os.WriteFile(filepath.Join(servicesDir, "mail-queue.yml"), []byte(
		"name: mail-queue\nservice: mail-queue\npolicy: { cooldown: 5m }\n"+
			"watches:\n  deferred-backlog:\n    check: { type: count, path: /tmp, of: file, count: { op: \">=\", value: 0 } }\n"+
			"    then: { hook: { command: [\"/bin/true\"] } }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sermo.yml"), []byte(fmt.Sprintf(`
engine: { backend: auto }
paths: { services: [ %s ], runtime: %s, state: %s }
defaults: { policy: { cooldown: 5m } }
`, servicesDir, runDir, stateDir)), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")

	run := func(args ...string) int {
		var out bytes.Buffer
		app := App{Env: func(string) string { return "" }, Stdout: &out, Stderr: &bytes.Buffer{}}
		return app.Run(context.Background(), append([]string{"--config", global}, args...))
	}
	watchPaused := func(name string) bool {
		store, err := state.Open(filepath.Join(stateDir, state.Filename))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		active, found, err := store.Active(sermoapp.WatchMonitorKey(name))
		if err != nil {
			t.Fatal(err)
		}
		return found && !active
	}

	// A service-embedded watch name uses the "<service>:<watch>" form.
	const name = "mail-queue:deferred-backlog"
	if code := run("watch", "unmonitor", name); code != exitSuccess {
		t.Fatalf("watch unmonitor exit = %d", code)
	}
	if !watchPaused(name) {
		t.Error("watch should be paused after unmonitor")
	}
	if code := run("watch", "monitor", name); code != exitSuccess {
		t.Fatalf("watch monitor exit = %d", code)
	}
	if watchPaused(name) {
		t.Error("watch should be resumed after monitor")
	}

	// Unmonitoring the watch's service must NOT touch the watch's own state.
	if code := run("watch", "unmonitor", name); code != exitSuccess {
		t.Fatalf("watch unmonitor exit = %d", code)
	}
	// The service key is the bare name, independent of the watch key.
	store, err := state.Open(filepath.Join(stateDir, state.Filename))
	if err != nil {
		t.Fatal(err)
	}
	_, serviceFound, _ := store.Active("mail-queue")
	store.Close()
	if serviceFound {
		t.Error("watch unmonitor must not write the service's monitor state")
	}

	// A typo'd/unknown watch name is rejected (mirrors the web "unknown watch").
	if code := run("watch", "unmonitor", "mail-queue:ghost"); code == exitSuccess {
		t.Error("unmonitor of an unknown watch should fail")
	}
}

func TestMonitorJSONIncludesSource(t *testing.T) {
	root, global := monitorTestConfig(t)
	var out bytes.Buffer
	app := monitorTestApp(root, &out)
	if code := app.Run(context.Background(), []string{"--config", global, "--json", "unmonitor", "web"}); code != exitSuccess {
		t.Fatalf("unmonitor exit = %d", code)
	}
	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["monitor_source"] != state.SourceCLI {
		t.Fatalf("monitor_source = %v", payload["monitor_source"])
	}
	if payload["monitor_changed_at"] == nil || payload["monitor_changed_at"] == "" {
		t.Fatalf("missing monitor_changed_at: %+v", payload)
	}
}

func TestStatusShowsPauseSource(t *testing.T) {
	root, global := monitorTestConfig(t)
	app := monitorTestApp(root, nil)
	if code := app.Run(context.Background(), []string{"--config", global, "unmonitor", "web"}); code != exitSuccess {
		t.Fatalf("unmonitor exit = %d", code)
	}

	var out bytes.Buffer
	app.Stdout = &out
	if code := app.Run(context.Background(), []string{"--config", global, "status", "web"}); code != exitSuccess {
		t.Fatalf("status exit = %d", code)
	}
	line := strings.TrimSpace(out.String())
	if !strings.Contains(line, "state=started") || !strings.Contains(line, "source="+state.SourceCLI) {
		t.Fatalf("status line = %q", line)
	}
	if !strings.Contains(line, "changed=") {
		t.Fatalf("status line missing changed=: %q", line)
	}

	out.Reset()
	if code := app.Run(context.Background(), []string{"--config", global, "--json", "status", "web"}); code != exitSuccess {
		t.Fatalf("status json exit = %d", code)
	}
	var st statusJSON
	if err := json.Unmarshal(out.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.State != sermoapp.TargetStateStarted || !st.Paused || st.MonitorSource != state.SourceCLI || st.MonitorChangedAt == "" {
		t.Fatalf("status json = %+v", st)
	}
}

func TestStatusShowsDisabledState(t *testing.T) {
	root, global := monitorTestConfig(t)
	if err := os.WriteFile(filepath.Join(root, "services", "web.yml"), []byte("name: web\nuses: nginx\nenabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := monitorTestApp(root, nil)

	var out bytes.Buffer
	app.Stdout = &out
	if code := app.Run(context.Background(), []string{"--config", global, "status", "web"}); code != exitSuccess {
		t.Fatalf("status exit = %d", code)
	}
	if line := strings.TrimSpace(out.String()); !strings.Contains(line, "state=disabled") {
		t.Fatalf("status line = %q", line)
	}

	out.Reset()
	if code := app.Run(context.Background(), []string{"--config", global, "--json", "status", "web"}); code != exitSuccess {
		t.Fatalf("status json exit = %d", code)
	}
	var st statusJSON
	if err := json.Unmarshal(out.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.State != sermoapp.TargetStateDisabled {
		t.Fatalf("status json state = %q, want %q", st.State, sermoapp.TargetStateDisabled)
	}
}

func monitorTestConfig(t *testing.T) (root, global string) {
	t.Helper()
	root = t.TempDir()
	catalogDir := filepath.Join(root, "catalog")
	catalogServicesDir := filepath.Join(catalogDir, "services")
	servicesDir := filepath.Join(root, "services")
	runDir := filepath.Join(root, "run")
	stateDir := filepath.Join(root, "state")
	for _, d := range []string{catalogServicesDir, servicesDir, runDir, stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(catalogServicesDir, "nginx.yml"), "name: nginx\nservice: nginx\n")
	write(filepath.Join(servicesDir, "web.yml"), "name: web\nuses: nginx\n")
	write(filepath.Join(root, "sermo.yml"), fmt.Sprintf(`
engine: { backend: auto }
paths: { services: [ %s ], runtime: %s, state: %s }
defaults: { policy: { cooldown: 5m } }
`, servicesDir, runDir, stateDir))
	return root, filepath.Join(root, "sermo.yml")
}

func monitorTestApp(root string, stdout *bytes.Buffer) App {
	if stdout == nil {
		stdout = &bytes.Buffer{}
	}
	status := servicemgr.ServiceStatus{
		Service: "web", Backend: servicemgr.BackendSystemd,
		Unit: "nginx.service", Status: servicemgr.StatusActive,
	}
	return App{
		Detector: fakeBackendDetector{detection: servicemgr.Detection{Backend: servicemgr.BackendSystemd}},
		NewManager: func(servicemgr.Backend) (servicemgr.Manager, error) {
			return fakeManager{status: status}, nil
		},
		Env:        func(string) string { return "" },
		Stdout:     stdout,
		Stderr:     &bytes.Buffer{},
		LoadConfig: testLoadConfigWithCatalog(filepath.Join(root, "catalog")),
	}
}

func TestMetaSuffix(t *testing.T) {
	cases := []struct{ source, changed, want string }{
		{"", "", ""},
		{"cli", "", " source=cli"},
		{"", "2026-01-02T03:04:05Z", " changed=2026-01-02T03:04:05Z"},
		{"web", "2026-01-02T03:04:05Z", " source=web changed=2026-01-02T03:04:05Z"},
	}
	for _, tc := range cases {
		if got := metaSuffix(tc.source, tc.changed); got != tc.want {
			t.Errorf("metaSuffix(%q, %q) = %q, want %q", tc.source, tc.changed, got, tc.want)
		}
	}
}
