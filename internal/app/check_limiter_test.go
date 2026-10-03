package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/servicemgr"
)

func TestSharedCheckBudgetIncludesWorkersWatchesAndOperations(t *testing.T) {
	limiter := checks.NewLimiter(1)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	blocker, err := checks.BuildInline("blocker", map[string]any{"type": "service", "expect": "active"}, checks.Deps{
		Limiter: limiter, DefaultTimeout: time.Second,
		Status: func(context.Context) (servicemgr.Status, error) {
			close(started)
			<-release
			return servicemgr.StatusActive, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(done)
		checks.Execute(t.Context(), blocker)
	}()
	<-started
	defer func() { close(release); <-done }()
	deps, collector := parallelStartupDeps(t, procInfoRunner{})
	deps.CheckLimiter, deps.DefaultTimeout = limiter, 5*time.Millisecond
	cfg := writeParallelServicesConfig(t, 2)
	workers, _, warnings := BuildWorkers(t.Context(), cfg, deps, collector)
	if len(warnings) != 0 || len(workers) != 2 {
		t.Fatalf("workers=%d warnings=%v", len(workers), warnings)
	}
	for _, worker := range workers {
		for _, res := range worker.Checks(t.Context(), worker.CheckDeps) {
			if !res.Unavailable {
				t.Fatalf("worker bypassed budget: %+v", res)
			}
		}
	}
	for _, d := range []checks.Deps{watchInlineDeps(deps), monitorDeps(deps)} {
		check, err := checks.BuildInline("watch", map[string]any{"type": "file_exists", "path": t.TempDir()}, d)
		if err != nil {
			t.Fatal(err)
		}
		if res := checks.Execute(t.Context(), check); !res.Unavailable {
			t.Fatalf("watch bypassed budget: %+v", res)
		}
	}
	runtime := BuildServiceRuntime(t.Context(), ServiceRuntimeConfig{Service: "demo", Unit: "demo", Deps: deps, Tree: map[string]any{
		config.SectionPreflight: map[string]any{"ready": map[string]any{"type": "service", "expect": "active"}},
		config.SectionChecks:    map[string]any{"ready": map[string]any{"type": "service", "expect": "active", "verify": true}},
	}})
	for _, run := range []func(context.Context) checks.Outcome{runtime.Engine.Preflight, runtime.Engine.Postflight} {
		out := run(t.Context())
		if out.OK || len(out.Results) == 0 || !out.Results[0].Unavailable {
			t.Fatalf("operation probe bypassed budget: %+v", out)
		}
	}
}
