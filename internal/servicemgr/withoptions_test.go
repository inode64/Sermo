package servicemgr

import (
	"context"
	"testing"
)

// WithOptions must change observable behaviour, not just the struct: a derived
// manager has to actually drop the isolation flag from the argv it runs.
func TestWithOptionsChangesTheArgvNotJustTheStruct(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name     string
		build    func(r *recordRunner) Manager
		isolated string
		allowed  string
	}{
		{"systemd", func(r *recordRunner) Manager { return systemdManager{runner: r} },
			"systemctl restart --job-mode=ignore-dependencies -- nginx.service",
			"systemctl restart -- nginx.service"},
		{"openrc", func(r *recordRunner) Manager { return openrcManager{runner: r} },
			"rc-service --nodeps nginx restart",
			"rc-service nginx restart"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordRunner{}
			shared := tc.build(rec)
			if err := shared.Restart(ctx, "nginx"); err != nil {
				t.Fatalf("shared Restart: %v", err)
			}
			derived := WithOptions(shared, Options{AllowDependencies: true})
			if err := derived.Restart(ctx, "nginx"); err != nil {
				t.Fatalf("derived Restart: %v", err)
			}
			if len(rec.calls) != 2 {
				t.Fatalf("calls = %v", rec.calls)
			}
			if rec.calls[0] != tc.isolated {
				t.Errorf("shared ran %q, want %q", rec.calls[0], tc.isolated)
			}
			if rec.calls[1] != tc.allowed {
				t.Errorf("derived ran %q, want %q", rec.calls[1], tc.allowed)
			}
		})
	}
}

// Deriving must not mutate the manager it derives from: the shared instance is
// reused by every other service in the generation.
func TestWithOptionsLeavesTheSharedManagerAlone(t *testing.T) {
	rec := &recordRunner{}
	shared := systemdManager{runner: rec}
	_ = WithOptions(shared, Options{AllowDependencies: true})
	if err := shared.Restart(context.Background(), "nginx"); err != nil {
		t.Fatal(err)
	}
	if got := rec.calls[0]; got != "systemctl restart --job-mode=ignore-dependencies -- nginx.service" {
		t.Fatalf("deriving mutated the shared manager: %q", got)
	}
}

// The runner must survive derivation — the whole point of deriving rather than
// constructing is that the injected runner is preserved.
func TestWithOptionsPreservesTheRunner(t *testing.T) {
	rec := &recordRunner{}
	derived := WithOptions(openrcManager{runner: rec}, Options{AllowDependencies: true})
	if err := derived.Restart(context.Background(), "nginx"); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("the injected runner was discarded; calls = %v", rec.calls)
	}
}

// WithOptions dispatches on the concrete manager type, so it would silently
// stop applying if construction ever returned a different shape (a pointer, a
// wrapper). Going through the real constructor turns that into a test failure
// instead of services quietly losing their isolation.
func TestWithOptionsAppliesToManagersFromTheRealConstructor(t *testing.T) {
	for _, backend := range []Backend{BackendSystemd, BackendOpenRC} {
		t.Run(string(backend), func(t *testing.T) {
			shared, err := NewManager(backend)
			if err != nil {
				t.Fatalf("NewManager: %v", err)
			}
			// Compare types, not values: openrcManager carries a func field, so
			// it is not comparable and == on it panics at runtime.
			switch shared.(type) {
			case systemdManager, openrcManager:
			default:
				t.Fatalf("NewManager returned %T, which WithOptions does not match", shared)
			}
		})
	}
}

// A backend with no dependency graph of its own is returned untouched rather
// than rejected — docker and libvirt legitimately have nothing to isolate.
func TestWithOptionsPassesThroughBackendsWithoutADependencyGraph(t *testing.T) {
	var m Manager = passthroughManager{}
	if got := WithOptions(m, Options{AllowDependencies: true}); got != m {
		t.Fatal("a backend without a dependency graph must be returned unchanged")
	}
}

// passthroughManager stands in for docker/libvirt: it has no dependency graph,
// and being an empty struct it stays comparable.
type passthroughManager struct{ Manager }
