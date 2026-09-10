package cli

import (
	"context"

	"sermo/internal/servicemgr"
)

type controlDependencies struct {
	backend  servicemgr.Backend
	manager  servicemgr.Manager
	resolver servicemgr.UnitResolver
}

type controlDependencyStage uint8

const (
	controlDependencyDetection controlDependencyStage = iota
	controlDependencyManager
)

// controlDependenciesFor builds the backend-specific dependencies shared by
// status, process discovery and manual operations.
func (a App) controlDependenciesFor(ctx context.Context, requested servicemgr.Backend) (controlDependencies, controlDependencyStage, error) {
	detection, err := a.Detector.Detect(ctx, requested)
	if err != nil {
		return controlDependencies{}, controlDependencyDetection, err
	}
	manager, err := a.NewManager(detection.Backend)
	if err != nil {
		return controlDependencies{}, controlDependencyManager, err
	}
	resolver := servicemgr.NewUnitResolver()
	resolver.Runner = a.Runner
	resolver.Manager = manager
	return controlDependencies{backend: detection.Backend, manager: manager, resolver: resolver}, controlDependencyDetection, nil
}
