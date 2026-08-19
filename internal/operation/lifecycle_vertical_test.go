package operation

import (
	"context"
	"reflect"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/locks"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

func TestResolvedLifecycleDrivesInitBackendMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		backend   servicemgr.Backend
		auxiliary string
	}{
		{backend: servicemgr.BackendSystemd, auxiliary: "demo.socket"},
		{backend: servicemgr.BackendOpenRC, auxiliary: "demo-helper"},
	}
	for _, tt := range tests {
		t.Run(string(tt.backend), func(t *testing.T) {
			t.Parallel()
			tree := map[string]any{
				config.ServiceKeyAlsoService: map[string]any{
					string(servicemgr.BackendSystemd): []any{"demo.socket"},
					string(servicemgr.BackendOpenRC):  []any{"demo-helper"},
				},
				config.SectionProcesses: map[string]any{},
			}
			engine, manager := lifecycleMatrixEngine(t, tt.backend, tree)
			if engine.Lifecycle.ProcessMode != config.ServiceProcessNone {
				t.Fatalf("process mode = %q, want none", engine.Lifecycle.ProcessMode)
			}
			if !reflect.DeepEqual(engine.Lifecycle.AuxiliaryUnits, []string{tt.auxiliary}) {
				t.Fatalf("auxiliary units = %v, want [%s]", engine.Lifecycle.AuxiliaryUnits, tt.auxiliary)
			}

			result := engine.Start(context.Background())
			if !result.OK() {
				t.Fatalf("start result = %+v, want ok", result)
			}
			want := []string{"start " + tt.auxiliary, "start demo"}
			if !reflect.DeepEqual(manager.calls, want) {
				t.Fatalf("manager calls = %v, want %v", manager.calls, want)
			}
		})
	}
}

func lifecycleMatrixEngine(t *testing.T, backend servicemgr.Backend, tree map[string]any) (Engine, *fakeManager) {
	t.Helper()
	runtimeDir := t.TempDir()
	locker := locks.NewOperationLocker(locks.RuntimeOpsDir(runtimeDir))
	manager := &fakeManager{status: servicemgr.StatusActive}
	engine := New(Config{
		Service:    "demo",
		Unit:       "demo",
		Backend:    string(backend),
		Tree:       tree,
		Manager:    manager,
		Locker:     &locker,
		Scanner:    locks.NewScanner(locks.RuntimeLocksDir(runtimeDir)),
		Discoverer: process.NewDiscovererWithUserLookup(nil),
		Sleep:      func(time.Duration) {},
	})
	return engine, manager
}
