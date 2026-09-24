package appinspect

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
	"sermo/internal/process"
)

func TestInspectCategorySharesProviderProbes(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "app")
	if err := os.WriteFile(binary, []byte("executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		result      execx.Result
		wantVersion string
	}{
		{name: "success", result: execx.Result{Stdout: "App 1.2.3"}, wantVersion: "App 1.2.3"},
		{name: "failure", result: execx.Result{ExitCode: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Apps: map[string]*config.Document{}}
			names := []string{"first", "provider", "second"}
			for _, name := range names {
				body := map[string]any{"preflight": map[string]any{"binary": map[string]any{"type": "binary", "path": binary}}}
				if name == "provider" {
					body = preflightResolved(binary, "").Tree
				} else {
					body["version_from"] = "provider"
				}
				cfg.Apps[name] = &config.Document{Name: name, Body: body}
			}
			runner := execxtest.Fixed(tc.result, nil)
			lookup := WithUserLookup(process.NewUserLookup(process.UserLookupConfig{Mode: process.UserLookupNumeric}))
			for batch := 1; batch <= 2; batch++ {
				reports := InspectCategory(context.Background(), runner, cfg, config.CategoryApp, names, len(names), lookup)
				for i, report := range reports {
					if report.Name != names[i] || report.Version != tc.wantVersion {
						t.Fatalf("report[%d] = %+v", i, report)
					}
				}
				if got := runner.Count(binary + " --version"); got != batch {
					t.Fatalf("provider probes = %d, want %d", got, batch)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			InspectCategory(ctx, runner, cfg, config.CategoryApp, names, 1, lookup)
			if got := runner.Count(binary + " --version"); got != 2 {
				t.Fatalf("cancelled batch ran a probe: %d", got)
			}
		})
	}
}

func TestInspectCategoryProviderCycleDoesNotDeadlock(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "app")
	if err := os.WriteFile(binary, []byte("executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Apps: map[string]*config.Document{}}
	for name, provider := range map[string]string{"a": "b", "b": "a"} {
		cfg.Apps[name] = &config.Document{Name: name, Body: map[string]any{
			"version_from": provider,
			"preflight":    map[string]any{"binary": map[string]any{"type": "binary", "path": binary}},
		}}
	}
	lookup := WithUserLookup(process.NewUserLookup(process.UserLookupConfig{Mode: process.UserLookupNumeric}))
	done := make(chan []Report, 1)
	go func() {
		done <- InspectCategory(context.Background(), &execxtest.Runner{}, cfg, config.CategoryApp, []string{"a", "b"}, 2, lookup)
	}()
	select {
	case reports := <-done:
		for _, report := range reports {
			if !report.Installed || !report.OK || report.Version != "" {
				t.Fatalf("cyclic report = %+v", report)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("cyclic version providers blocked inspection")
	}
}
