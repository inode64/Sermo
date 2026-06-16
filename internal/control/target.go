// Package control resolves the concrete control target for one service.
package control

import (
	"context"
	"fmt"

	"sermo/internal/config"
	"sermo/internal/servicemgr"
	"sermo/internal/virt"
)

// Target is the manager/unit pair the operation path should use for a service.
type Target struct {
	Unit    string
	Backend servicemgr.Backend
	Manager servicemgr.Manager
}

// Resolve returns the operation target for tree. A service without `control`
// keeps the normal init backend resolution. `control.type: libvirt` swaps only
// this service to a libvirt manager, leaving the global init backend unchanged.
func Resolve(ctx context.Context, name string, tree map[string]any, backend servicemgr.Backend, manager servicemgr.Manager, resolver servicemgr.UnitResolver) (Target, error) {
	if spec, ok, err := virt.SpecFromTree(tree); ok || err != nil {
		if err != nil {
			return Target{}, err
		}
		return Target{
			Unit:    spec.Domain,
			Backend: servicemgr.BackendLibvirt,
			Manager: virt.NewManager(spec),
		}, nil
	}
	candidates, trust := config.ServiceCandidates(tree, string(backend), name)
	unit, err := resolver.Resolve(ctx, backend, candidates, trust)
	if err != nil {
		return Target{}, err
	}
	return Target{Unit: unit, Backend: backend, Manager: manager}, nil
}

// ResolveWithFallback mirrors the historic init-service behavior: if probing the
// init backend cannot resolve a unit, it falls back to the configured service
// name. Explicit control errors are still returned because there is no safe
// fallback target for a VM.
func ResolveWithFallback(ctx context.Context, name string, tree map[string]any, backend servicemgr.Backend, manager servicemgr.Manager, resolver servicemgr.UnitResolver) (Target, string) {
	target, err := Resolve(ctx, name, tree, backend, manager, resolver)
	if err == nil {
		return target, ""
	}
	if _, controlled, specErr := virt.SpecFromTree(tree); controlled || specErr != nil {
		return Target{}, err.Error()
	}
	unit := config.ServiceUnit(tree, name)
	return Target{Unit: unit, Backend: backend, Manager: manager}, fmt.Sprintf("%s (using %s)", err.Error(), unit)
}
