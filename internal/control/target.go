// Package control resolves the concrete control target for one service.
package control

import (
	"context"
	"errors"
	"fmt"

	"sermo/internal/cfgval"
	"sermo/internal/config"
	"sermo/internal/dockerctl"
	"sermo/internal/servicemgr"
	"sermo/internal/virt"
)

const (
	controlKeyType     = config.EntryKeyType
	controlTypeSummary = virt.ControlType + ", " + dockerctl.ControlType
)

// Target is the manager/unit pair the operation path should use for a service.
type Target struct {
	Unit        string
	Backend     servicemgr.Backend
	Manager     servicemgr.Manager
	BackendPIDs func() []int
}

// Resolve returns the operation target for tree. A service without `control`
// keeps the normal init backend resolution. `control.type: libvirt` swaps only
// this service to a libvirt manager, and `control.type: docker` swaps it to a
// Docker container manager, leaving the global init backend unchanged.
func Resolve(ctx context.Context, name string, tree map[string]any, backend servicemgr.Backend, manager servicemgr.Manager, resolver servicemgr.UnitResolver) (Target, error) {
	typ, controlled, err := controlType(tree)
	if err != nil {
		return Target{}, err
	}
	if controlled {
		return resolveControlledTarget(ctx, typ, tree)
	}
	candidates, trust := config.ServiceCandidates(tree, string(backend), name)
	unit, err := resolver.Resolve(ctx, backend, candidates, trust)
	if err != nil {
		return Target{}, err
	}
	return Target{Unit: unit, Backend: backend, Manager: manager}, nil
}

func resolveControlledTarget(ctx context.Context, typ string, tree map[string]any) (Target, error) {
	switch typ {
	case virt.ControlType:
		spec, _, err := virt.SpecFromTree(tree)
		if err != nil {
			return Target{}, err
		}
		return Target{
			Unit:    spec.Domain,
			Backend: servicemgr.BackendLibvirt,
			Manager: virt.NewManager(spec),
		}, nil
	case dockerctl.ControlType:
		spec, _, err := dockerctl.SpecFromTree(tree)
		if err != nil {
			return Target{}, err
		}
		manager := dockerctl.NewManager(spec)
		return Target{
			Unit:        spec.Container,
			Backend:     servicemgr.BackendDocker,
			Manager:     manager,
			BackendPIDs: manager.BackendPIDs(ctx),
		}, nil
	default:
		return Target{}, fmt.Errorf("control.type %q is not one of %s", typ, controlTypeSummary)
	}
}

// ResolveWithFallback mirrors the historic init-service behavior: if probing the
// init backend cannot resolve configured unit candidates, it falls back to the
// configured service name. Explicit control errors and backend-unavailable
// service maps are still returned because there is no safe fallback target.
func ResolveWithFallback(ctx context.Context, name string, tree map[string]any, backend servicemgr.Backend, manager servicemgr.Manager, resolver servicemgr.UnitResolver) (Target, string) {
	target, err := Resolve(ctx, name, tree, backend, manager, resolver)
	if err == nil {
		return target, ""
	}
	if _, controlled, specErr := controlType(tree); controlled || specErr != nil {
		return Target{}, err.Error()
	}
	candidates, _ := config.ServiceCandidates(tree, string(backend), name)
	if len(candidates) == 0 {
		return Target{}, err.Error()
	}
	unit := candidates[0]
	return Target{Unit: unit, Backend: backend, Manager: manager}, fmt.Sprintf("%s (using %s)", err.Error(), unit)
}

func controlType(tree map[string]any) (string, bool, error) {
	raw, present := tree[config.SectionControl]
	if !present {
		return "", false, nil
	}
	control, ok := raw.(map[string]any)
	if !ok {
		return "", true, errors.New("control must be a mapping")
	}
	return cfgval.String(control[controlKeyType]), true, nil
}
