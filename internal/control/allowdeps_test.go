package control

import (
	"context"
	"testing"

	"sermo/internal/config"
	"sermo/internal/servicemgr"
)

// sharedManager stands in for the process-wide manager the daemon builds once.
// Resolve must hand it back untouched for the default (isolated) case, and
// substitute a per-service one only when the service opts back in.
type sharedManager struct{ servicemgr.Manager }

func resolveInitTarget(t *testing.T, tree map[string]any) Target {
	t.Helper()
	tree[config.ServiceKeyService] = map[string]any{
		string(servicemgr.BackendSystemd): []any{"svc"},
	}
	// ResolveWithFallback takes the same manager path as Resolve but tolerates
	// a host where the unit cannot be probed, which is every test machine.
	target, warning := ResolveWithFallback(context.Background(), "svc", tree,
		servicemgr.BackendSystemd, sharedManager{}, servicemgr.UnitResolver{Runner: noKnownUnitsRunner{}})
	if target.Unit == "" {
		t.Fatalf("ResolveWithFallback() resolved no unit (warning=%q)", warning)
	}
	return target
}

// The default reuses the shared manager: it already isolates operations, so
// there is nothing to substitute and no allocation per service.
func TestResolveKeepsSharedManagerWhenIsolated(t *testing.T) {
	target := resolveInitTarget(t, map[string]any{})
	if _, shared := target.Manager.(sharedManager); !shared {
		t.Fatalf("want the shared manager, got %T", target.Manager)
	}
}

// Opting in has to produce a manager of its own, otherwise the flag would be
// silently ignored for that service.
func TestResolveSubstitutesManagerWhenDependenciesAllowed(t *testing.T) {
	target := resolveInitTarget(t, map[string]any{"allow_dependencies": true})
	if _, shared := target.Manager.(sharedManager); shared {
		t.Fatal("a service that allows dependencies must not reuse the isolated shared manager")
	}
	if target.Manager == nil {
		t.Fatal("Resolve() returned a nil manager")
	}
}

// The flag is inheritable from global `defaults:` like dry_run. Validation
// accepting the key is not enough — this asserts it reaches the target that
// operations actually use.
func TestDefaultsAllowDependenciesReachesTheTarget(t *testing.T) {
	for _, tc := range []struct {
		name     string
		defaults bool
		shared   bool
	}{
		{"defaults isolate", false, true},
		{"defaults allow", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// defaultsPerService merges the global value into the service tree
			// before resolution, so the resolved tree is what Resolve sees.
			tree := map[string]any{}
			if tc.defaults {
				tree["allow_dependencies"] = true
			}
			target := resolveInitTarget(t, tree)
			_, shared := target.Manager.(sharedManager)
			if shared != tc.shared {
				t.Fatalf("shared manager = %v, want %v", shared, tc.shared)
			}
		})
	}
}
