package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	appcore "sermo/internal/app"
	"sermo/internal/checks"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

func TestServiceLibrariesFreshReadOnlyInventory(t *testing.T) {
	for _, name := range []string{"complete", "stopped", "active without processes", "partial", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			global := writeServiceConfig(t, servicesDirGlobal, map[string]string{
				"services/web.yml": "name: web\nservice: web\nchecks:\n  libs: {type: stale_libraries}\n",
			})
			var stdout bytes.Buffer
			var actions []string
			status := servicemgr.StatusActive
			if name == "stopped" {
				status = servicemgr.StatusInactive
			}
			cliApp := hermeticApp(t, App{
				NewManager: func(servicemgr.Backend) (servicemgr.Manager, error) {
					return fakeManager{liveStatus: &status, actions: &actions}, nil
				},
				Detector: fakeBackendDetector{detection: servicemgr.BackendSystemd},
				Runner:   statusUnitRunner{known: "web.service"},
				buildServiceRuntime: func(context.Context, appcore.ServiceRuntimeConfig) appcore.ServiceRuntime {
					return appcore.ServiceRuntime{CheckDeps: checks.Deps{StaleLibraries: func(ctx context.Context) (process.LibraryReport, error) {
						if _, ok := ctx.Deadline(); !ok {
							t.Fatal("unbounded inventory")
						}
						report := process.LibraryReport{Processes: 1, Dependencies: []string{"/lib/libone.so"}}
						switch name {
						case "partial":
							return report, errors.New("unreadable maps")
						case "cancelled":
							return process.LibraryReport{}, context.Canceled
						case "stopped", "active without processes":
							return process.LibraryReport{}, nil
						default:
							return report, nil
						}
					}}}
				},
				Stdout: &stdout, Stderr: &bytes.Buffer{},
			})
			if code := cliApp.Run(t.Context(), []string{"--config", global, "services", "libraries", "web", "--json"}); code != exitSuccess {
				t.Fatalf("exit=%d output=%s", code, stdout.String())
			}
			var output struct{ Services []serviceLibraries }
			if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
				t.Fatal(err)
			}
			if len(output.Services) != 1 || output.Services[0].Complete != (name == "complete") || !output.Services[0].CheckEnabled {
				t.Fatalf("wrong completeness: %+v", output)
			}
			if len(actions) != 0 {
				t.Fatal("inventory executed lifecycle actions", actions)
			}
			if name == "active without processes" && len(output.Services[0].Errors) == 0 {
				t.Fatal("active service silently classified as stopped")
			}
			if name == "partial" && len(output.Services[0].Mapped.Dependencies) != 1 {
				t.Fatal("partial evidence lost")
			}
		})
	}
}
