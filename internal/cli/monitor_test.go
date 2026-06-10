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

	"sermo/internal/servicemgr"
	"sermo/internal/state"
)

func TestMonitorUnmonitorCommand(t *testing.T) {
	root := t.TempDir()
	profilesDir := filepath.Join(root, "profiles")
	enabledDir := filepath.Join(root, "enabled")
	runDir := filepath.Join(root, "run")
	stateDir := filepath.Join(root, "state")
	for _, d := range []string{profilesDir, enabledDir, runDir, stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(profilesDir, "nginx.yml"), "kind: profile\nname: nginx\nservice: { name: nginx }\n")
	write(filepath.Join(enabledDir, "web.yml"), "kind: service\nname: web\nuses: nginx\n")
	write(filepath.Join(root, "sermo.yml"), fmt.Sprintf(`
engine: { backend: auto }
paths: { profiles: [ %s ], includes: [ %s ], runtime: %s, state: %s }
defaults: { policy: { cooldown: 5m } }
`, profilesDir, enabledDir, runDir, stateDir))
	global := filepath.Join(root, "sermo.yml")

	run := func(args ...string) int {
		var out bytes.Buffer
		app := App{Env: func(string) string { return "" }, Stdout: &out, Stderr: &bytes.Buffer{}}
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
	if !strings.Contains(line, "monitoring=paused") || !strings.Contains(line, "source="+state.SourceCLI) {
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
	if !st.Paused || st.MonitorSource != state.SourceCLI || st.MonitorChangedAt == "" {
		t.Fatalf("status json = %+v", st)
	}
}

func monitorTestConfig(t *testing.T) (root, global string) {
	t.Helper()
	root = t.TempDir()
	profilesDir := filepath.Join(root, "profiles")
	enabledDir := filepath.Join(root, "enabled")
	runDir := filepath.Join(root, "run")
	stateDir := filepath.Join(root, "state")
	for _, d := range []string{profilesDir, enabledDir, runDir, stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(profilesDir, "nginx.yml"), "kind: profile\nname: nginx\nservice: { name: nginx }\n")
	write(filepath.Join(enabledDir, "web.yml"), "kind: service\nname: web\nuses: nginx\n")
	write(filepath.Join(root, "sermo.yml"), fmt.Sprintf(`
engine: { backend: auto }
paths: { profiles: [ %s ], includes: [ %s ], runtime: %s, state: %s }
defaults: { policy: { cooldown: 5m } }
`, profilesDir, enabledDir, runDir, stateDir))
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
		Env:    func(string) string { return "" },
		Stdout: stdout,
		Stderr: &bytes.Buffer{},
	}
}
