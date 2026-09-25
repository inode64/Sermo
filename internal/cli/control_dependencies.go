package cli

import (
	"context"
	"fmt"

	"sermo/internal/servicemgr"
)

type controlDependencies struct {
	backend  servicemgr.Backend
	manager  servicemgr.Manager
	resolver servicemgr.UnitResolver
}

// controlDependenciesFor builds the backend-specific dependencies shared by
// status, process discovery and manual operations.
func (a App) controlDependenciesFor(ctx context.Context, requested servicemgr.Backend) (controlDependencies, error) {
	backend, err := a.Detector.Detect(ctx, requested)
	if err != nil {
		return controlDependencies{}, fmt.Errorf("backend detection failed: %w", err)
	}
	manager, err := a.NewManager(backend)
	if err != nil {
		return controlDependencies{}, fmt.Errorf("service manager unavailable: %w", err)
	}
	resolver := servicemgr.NewUnitResolver()
	resolver.Runner = a.Runner
	resolver.Manager = manager
	return controlDependencies{backend: backend, manager: manager, resolver: resolver}, nil
}
