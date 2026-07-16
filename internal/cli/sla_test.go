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
	root, global := writeCatalogServiceConfig(t)
	catalogDir := filepath.Join(root, "catalog")
	// Seed three recent samples: two up, one down -> ~66.67% across every window.
	seedSLA(t, filepath.Join(root, "state"), "web",
		slaSample{true, 0}, slaSample{false, time.Minute}, slaSample{true, 2 * time.Minute})

	run := slaRunner(t, global, catalogDir)

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
	root, global := writeCatalogServiceConfig(t)
	catalogDir := filepath.Join(root, "catalog")
	seedSLA(t, filepath.Join(root, "state"), "web",
		slaSample{true, 3 * time.Minute}, slaSample{false, 2 * time.Minute})

	run := slaRunner(t, global, catalogDir)

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

// slaSample is one SLA data point: up/down and how long before now it occurred.
type slaSample struct {
	up  bool
	ago time.Duration
}

// seedSLA records samples for service into the state store at stateDir, each at
// its ago offset before now, then closes the store.
func seedSLA(t *testing.T, stateDir, service string, samples ...slaSample) {
	t.Helper()
	store, err := state.Open(filepath.Join(stateDir, state.Filename))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, s := range samples {
		if err := store.RecordSLA(service, s.up, now.Add(-s.ago)); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()
}

// slaRunner returns a helper that runs sla-family commands against global with a
// catalog-aware config loader, reporting combined stdout and the exit code.
func slaRunner(t *testing.T, global, catalogDir string) func(args ...string) (string, int) {
	t.Helper()
	return func(args ...string) (string, int) {
		var out bytes.Buffer
		app := App{Env: func(string) string { return "" }, Stdout: &out, Stderr: &bytes.Buffer{}, LoadConfig: testLoadConfigWithCatalog(catalogDir)}
		code := app.Run(context.Background(), append([]string{"--config", global}, args...))
		return out.String(), code
	}
}

func TestSLACommandUnknownService(t *testing.T) {
	root := t.TempDir()
	servicesDir := filepath.Join(root, "services")
	stateDir := filepath.Join(root, "state")
	for _, d := range []string{servicesDir, stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "sermo.yml"), fmt.Appendf(nil, `
engine: { backend: auto }
paths: { services: [ %s ], state: %s }
defaults: { policy: { cooldown: 5m } }
`, servicesDir, stateDir), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &out, Stderr: &bytes.Buffer{}}
	code := app.Run(context.Background(), []string{"--config", filepath.Join(root, "sermo.yml"), "sla", "ghost"})
	if code == exitSuccess {
		t.Fatal("sla of unknown service should fail")
	}
}
