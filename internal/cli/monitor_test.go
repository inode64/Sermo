package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

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
paths: { profiles: [ %s ], enabled: [ %s ], runtime: %s, state: %s }
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
