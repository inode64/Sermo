package app

import (
	"testing"

	"sermo/internal/config"
	"sermo/internal/process"
)

// emptyCatalogOption is the load option every app test passes: an empty
// catalog, so a config never pulls in the host's installed catalog or lists
// the host's active units to materialize its version templates.
func emptyCatalogOption(t *testing.T) config.Option {
	t.Helper()
	return config.WithCatalogDirs(t.TempDir())
}

// loadTestConfig loads global against an empty catalog and fails the test on
// error.
func loadTestConfig(t *testing.T, global string) *config.Config {
	t.Helper()
	cfg, err := config.Load(global, emptyCatalogOption(t))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

// hermeticMonitor makes mon's reloads load against an empty catalog and
// rebuild their /proc readers over an empty process table.
func hermeticMonitor(t *testing.T, mon *Monitor) {
	t.Helper()
	mon.LoadOptions = []config.Option{emptyCatalogOption(t)}
	mon.newProcReader = func(process.OSReader) process.Reader { return emptyProcTable{} }
}

// emptyProcTable is a /proc reader with no processes.
type emptyProcTable struct{}

func (emptyProcTable) PIDs() ([]int, error)                  { return nil, nil }
func (emptyProcTable) Identity(int) (process.Identity, bool) { return process.Identity{}, false }
