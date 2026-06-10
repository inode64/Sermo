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
	"time"

	"sermo/internal/state"
)

func TestSLACommandReportsWindows(t *testing.T) {
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

	// Seed three recent samples: two up, one down -> ~66.67% across every window.
	store, err := state.Open(filepath.Join(stateDir, state.Filename))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i, up := range []bool{true, false, true} {
		if err := store.RecordSLA("web", up, now.Add(-time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()

	run := func(args ...string) (string, int) {
		var out bytes.Buffer
		app := App{Env: func(string) string { return "" }, Stdout: &out, Stderr: &bytes.Buffer{}}
		code := app.Run(context.Background(), append([]string{"--config", global}, args...))
		return out.String(), code
	}

	// Text table for one service.
	out, code := run("sla", "web")
	if code != exitSuccess {
		t.Fatalf("sla exit = %d, output: %s", code, out)
	}
	if !strings.Contains(out, "HOUR") || !strings.Contains(out, "YEAR") {
		t.Fatalf("table missing window headers: %s", out)
	}
	if !strings.Contains(out, "66.67%") {
		t.Fatalf("expected 66.67%% availability, got: %s", out)
	}

	// JSON for all services.
	jsonOut, code := run("--json", "sla")
	if code != exitSuccess {
		t.Fatalf("sla --json exit = %d, output: %s", code, jsonOut)
	}
	var payload struct {
		SLA []struct {
			Service string `json:"service"`
			Windows map[string]struct {
				Up    int64    `json:"up"`
				Total int64    `json:"total"`
				Ratio *float64 `json:"ratio"`
			} `json:"windows"`
		} `json:"sla"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &payload); err != nil {
		t.Fatalf("decode json: %v\n%s", err, jsonOut)
	}
	if len(payload.SLA) != 1 || payload.SLA[0].Service != "web" {
		t.Fatalf("unexpected services in report: %+v", payload.SLA)
	}
	hour := payload.SLA[0].Windows["hour"]
	if hour.Up != 2 || hour.Total != 3 || hour.Ratio == nil {
		t.Fatalf("hour window = %+v, want up=2 total=3 ratio!=nil", hour)
	}
}

func TestSLASeriesCommand(t *testing.T) {
	root := t.TempDir()
	profilesDir := filepath.Join(root, "profiles")
	enabledDir := filepath.Join(root, "enabled")
	stateDir := filepath.Join(root, "state")
	for _, d := range []string{profilesDir, enabledDir, stateDir} {
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
paths: { profiles: [ %s ], includes: [ %s ], state: %s }
defaults: { policy: { cooldown: 5m } }
`, profilesDir, enabledDir, stateDir))
	global := filepath.Join(root, "sermo.yml")

	store, err := state.Open(filepath.Join(stateDir, state.Filename))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := store.RecordSLA("web", true, now.Add(-3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSLA("web", false, now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	store.Close()

	run := func(args ...string) (string, int) {
		var out bytes.Buffer
		app := App{Env: func(string) string { return "" }, Stdout: &out, Stderr: &bytes.Buffer{}}
		code := app.Run(context.Background(), append([]string{"--config", global}, args...))
		return out.String(), code
	}

	jsonOut, code := run("--json", "sla", "--series", "web", "--since", "1h")
	if code != exitSuccess {
		t.Fatalf("sla --series exit = %d, output: %s", code, jsonOut)
	}
	var payload struct {
		Service string `json:"service"`
		Since   string `json:"since"`
		Series  []struct {
			Start string   `json:"start"`
			Up    int64    `json:"up"`
			Total int64    `json:"total"`
			Ratio *float64 `json:"ratio"`
		} `json:"series"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &payload); err != nil {
		t.Fatalf("decode json: %v\n%s", err, jsonOut)
	}
	if payload.Service != "web" || payload.Since != "1h0m0s" {
		t.Fatalf("unexpected header: service=%q since=%q", payload.Service, payload.Since)
	}
	if len(payload.Series) != 2 {
		t.Fatalf("series has %d points, want 2", len(payload.Series))
	}
	if payload.Series[0].Total != 1 || payload.Series[0].Ratio == nil || *payload.Series[0].Ratio != 1 {
		t.Fatalf("first point = %+v, want up sample ratio 1.0", payload.Series[0])
	}

	// --series with no service is a usage error.
	if _, code := run("sla", "--series"); code != exitUsage {
		t.Fatalf("sla --series without service exit = %d, want %d", code, exitUsage)
	}
}

func TestSLACommandUnknownService(t *testing.T) {
	root := t.TempDir()
	enabledDir := filepath.Join(root, "enabled")
	stateDir := filepath.Join(root, "state")
	for _, d := range []string{enabledDir, stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "sermo.yml"), []byte(fmt.Sprintf(`
engine: { backend: auto }
paths: { includes: [ %s ], state: %s }
defaults: { policy: { cooldown: 5m } }
`, enabledDir, stateDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &out, Stderr: &bytes.Buffer{}}
	code := app.Run(context.Background(), []string{"--config", filepath.Join(root, "sermo.yml"), "sla", "ghost"})
	if code == exitSuccess {
		t.Fatal("sla of unknown service should fail")
	}
}
