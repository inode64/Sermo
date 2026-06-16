// Package control resolves the concrete control target for one service.
package control

import (
	"context"
	"fmt"

	"sermo/internal/cfgval"
	"sermo/internal/config"
	"sermo/internal/dockerctl"
	"sermo/internal/servicemgr"
	"sermo/internal/virt"
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
		switch typ {
		case "libvirt":
			spec, _, err := virt.SpecFromTree(tree)
			if err != nil {
				return Target{}, err
			}
			return Target{
				Unit:    spec.Domain,
				Backend: servicemgr.BackendLibvirt,
				Manager: virt.NewManager(spec),
			}, nil
		case "docker":
			spec, _, err := dockerctl.SpecFromTree(tree)
			if err != nil {
				return Target{}, err
			}
			manager := dockerctl.NewManager(spec)
			return Target{
				Unit:        spec.Container,
				Backend:     servicemgr.BackendDocker,
				Manager:     manager,
				BackendPIDs: manager.BackendPIDs(),
			}, nil
		default:
			return Target{}, fmt.Errorf("control.type %q is not one of libvirt, docker", typ)
		}
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
// fallback target for a VM/container.
func ResolveWithFallback(ctx context.Context, name string, tree map[string]any, backend servicemgr.Backend, manager servicemgr.Manager, resolver servicemgr.UnitResolver) (Target, string) {
	target, err := Resolve(ctx, name, tree, backend, manager, resolver)
	if err == nil {
		return target, ""
	}
	if _, controlled, specErr := controlType(tree); controlled || specErr != nil {
		return Target{}, err.Error()
	}
	unit := config.ServiceUnit(tree, name)
	return Target{Unit: unit, Backend: backend, Manager: manager}, fmt.Sprintf("%s (using %s)", err.Error(), unit)
}

func controlType(tree map[string]any) (string, bool, error) {
	raw, present := tree["control"]
	if !present {
		return "", false, nil
	}
	control, ok := raw.(map[string]any)
	if !ok {
		return "", true, fmt.Errorf("control must be a mapping")
	}
	return cfgval.String(control["type"]), true, nil
}
