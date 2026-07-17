// Package control resolves the concrete control target for one service.
package control

import (
	"context"
	"errors"
	"fmt"
	"sync"

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
		return Target{}, fmt.Errorf("resolve service unit for %s: %w", name, err)
	}
	return Target{Unit: unit, Backend: backend, Manager: manager}, nil
}

func resolveControlledTarget(ctx context.Context, typ string, tree map[string]any) (Target, error) {
	switch typ {
	case virt.ControlType:
		spec, _, err := virt.SpecFromTree(tree)
		if err != nil {
			return Target{}, fmt.Errorf("resolve libvirt control: %w", err)
		}
		return Target{
			Unit:    spec.Domain,
			Backend: servicemgr.BackendLibvirt,
			Manager: virt.NewManager(spec),
		}, nil
	case dockerctl.ControlType:
		spec, _, err := dockerctl.SpecFromTree(tree)
		if err != nil {
			return Target{}, fmt.Errorf("resolve docker control: %w", err)
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

// UnsupportedOnBackend reports whether tree's explicit per-init service map
// declares no unit for backend. Resolution then fails by construction — the
// profile deliberately does not support this init system — so the skip is
// expected behavior of the configuration, not a degradation: callers report it
// as an informational notice instead of a warning. A scalar `service:` (trusted
// fallback) and controlled services (docker/libvirt) never match.
func UnsupportedOnBackend(tree map[string]any, backend servicemgr.Backend, name string) bool {
	if _, controlled, err := controlType(tree); controlled || err != nil {
		return false
	}
	candidates, trust := config.ServiceCandidates(tree, string(backend), name)
	return !trust && len(candidates) == 0
}

// TargetCache memoizes ResolveWithFallback per service name for one build
// generation. The workers build and the web backend build iterate the same
// configured services back-to-back on every daemon start and reload; without
// the cache each service's unit is probed twice (systemctl cat / init-script
// stat) and every resolution warning is logged twice. Create a fresh cache per
// config generation — entries never expire, and the warning is returned only
// to the first resolver so it reaches the log once.
type TargetCache struct {
	mu      sync.Mutex
	entries map[string]cachedTarget
}

type cachedTarget struct {
	target Target
}

// NewTargetCache returns an empty per-generation resolution cache.
func NewTargetCache() *TargetCache {
	return &TargetCache{entries: map[string]cachedTarget{}}
}

// ResolveWithFallback resolves through the cache: the first call for a service
// resolves and reports its warning; later calls return the cached target with
// an empty warning and no backend probes.
func (c *TargetCache) ResolveWithFallback(ctx context.Context, name string, tree map[string]any, backend servicemgr.Backend, manager servicemgr.Manager, resolver servicemgr.UnitResolver) (Target, string) {
	c.mu.Lock()
	cached, ok := c.entries[name]
	c.mu.Unlock()
	if ok {
		return cached.target, ""
	}
	target, warn := ResolveWithFallback(ctx, name, tree, backend, manager, resolver)
	c.mu.Lock()
	c.entries[name] = cachedTarget{target: target}
	c.mu.Unlock()
	return target, warn
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
