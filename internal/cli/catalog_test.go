package cli

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"testing"

	"sermo/internal/config"
	"sermo/internal/execx/execxtest"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

func testLoadConfigWithCatalog(catalogDir string) func(string, ...config.Option) (*config.Config, error) {
	return func(globalPath string, opts ...config.Option) (*config.Config, error) {
		opts = append([]config.Option{config.WithCatalogDirs(filepath.Clean(catalogDir))}, opts...)
		return config.Load(globalPath, opts...)
	}
}

// testConfigLoader is the LoadConfig every test without its own catalog uses:
// an empty catalog, never the host's installed one.
func testConfigLoader(t *testing.T) func(string, ...config.Option) (*config.Config, error) {
	t.Helper()
	return testLoadConfigWithCatalog(t.TempDir())
}

// loadTestConfig loads global against an empty catalog; an explicit
// WithCatalogDirs in opts still wins.
func loadTestConfig(t *testing.T, global string, opts ...config.Option) (*config.Config, error) {
	t.Helper()
	return testConfigLoader(t)(global, opts...)
}

// errHostSeam is what a hermetic test's host-only seam returns when reached.
var errHostSeam = errors.New("test reached a host seam it did not inject")

// hermeticApp fills every nil seam of a that would otherwise touch this host:
// configs load against an empty catalog, backend detection and service managers
// are fakes, commands go to a scripted runner, and daemon discovery and
// signalling fail the test if reached. A seam the test set is kept. The
// Makefile's catalog pin stays as a second net for a literal left unwrapped.
func hermeticApp(t *testing.T, a App) App {
	t.Helper()
	if a.LoadConfig == nil {
		a.LoadConfig = testConfigLoader(t)
	}
	if a.Detector == nil {
		a.Detector = fakeBackendDetector{detection: servicemgr.BackendSystemd}
	}
	if a.NewManager == nil {
		a.NewManager = func(servicemgr.Backend) (servicemgr.Manager, error) { return fakeManager{}, nil }
	}
	if a.Runner == nil {
		a.Runner = &execxtest.Runner{}
	}
	if a.pidfileFallbacks == nil {
		a.pidfileFallbacks = []string{}
	}
	if a.FindPID == nil {
		a.FindPID = func(string) ([]int, error) {
			t.Error("test scanned the host /proc for sermod")
			return nil, errHostSeam
		}
	}
	if a.daemonIdentity == nil {
		a.daemonIdentity = func(int) (process.Identity, bool) {
			t.Error("test read a host process identity for daemon reload")
			return process.Identity{}, false
		}
	}
	if a.signalDaemon == nil {
		a.signalDaemon = func(context.Context, process.Process, syscall.Signal) error {
			t.Error("test signalled a host process")
			return errHostSeam
		}
	}
	return a
}
