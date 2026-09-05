package control

import (
	"context"
	"fmt"
	"testing"

	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
	"sermo/internal/servicemgr"
)

// resolveInitTarget resolves an init-backed service through the same manager
// path Resolve uses, starting from the process-wide manager the daemon builds
// once. shared is a real backend manager, because the substitution derives from
// it rather than constructing a fresh one.
func resolveInitTarget(t *testing.T, tree map[string]any, shared servicemgr.Manager) Target {
	t.Helper()
	tree[config.ServiceKeyService] = map[string]any{
		string(servicemgr.BackendSystemd): []any{"svc"},
	}
	// ResolveWithFallback takes the same manager path as Resolve but tolerates
	// a host where the unit cannot be probed, which is every test machine.
	target, warning := ResolveWithFallback(context.Background(), "svc", tree,
		servicemgr.BackendSystemd, shared, servicemgr.UnitResolver{Runner: execxtest.Fixed(execx.Result{ExitCode: 1}, nil)})
	if target.Unit == "" {
		t.Fatalf("ResolveWithFallback() resolved no unit (warning=%q)", warning)
	}
	return target
}

// sharedSystemdManager returns a real backend manager. systemd deliberately:
// its manager is a comparable struct, so the tests below can assert on identity.
// The OpenRC one carries a func field and == on it panics — behaviour there is
// covered in internal/servicemgr, where the argv is observable.
func sharedSystemdManager(t *testing.T) servicemgr.Manager {
	t.Helper()
	m, err := servicemgr.NewManager(servicemgr.BackendSystemd)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

// The default hands back the very manager it was given: the shared one already
// isolates operations, so there is nothing to derive and nothing allocated per
// service.
func TestResolveKeepsSharedManagerWhenIsolated(t *testing.T) {
	shared := sharedSystemdManager(t)
	target := resolveInitTarget(t, map[string]any{}, shared)
	if target.Manager != shared {
		t.Fatalf("want the shared manager unchanged, got %#v", target.Manager)
	}
}

// Opting in must produce a manager configured differently, otherwise the flag
// would be silently ignored for that service.
func TestResolveSubstitutesManagerWhenDependenciesAllowed(t *testing.T) {
	shared := sharedSystemdManager(t)
	target := resolveInitTarget(t, map[string]any{"allow_dependencies": true}, shared)
	if target.Manager == shared {
		t.Fatal("a service that allows dependencies must not reuse the isolated shared manager")
	}
	if target.Manager == nil {
		t.Fatal("Resolve() returned a nil manager")
	}
}

// The derived manager must keep the shared one's configuration — the daemon and
// the CLI inject their runner through a single seam, and building a fresh
// manager here would discard it and shell out for real.
func TestDerivedManagerKeepsTheSharedBackend(t *testing.T) {
	shared := sharedSystemdManager(t)
	target := resolveInitTarget(t, map[string]any{"allow_dependencies": true}, shared)
	if got, want := fmt.Sprintf("%T", target.Manager), fmt.Sprintf("%T", shared); got != want {
		t.Fatalf("derived manager type = %s, want the shared one's %s", got, want)
	}
}
