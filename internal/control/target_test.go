package control

import (
	"context"
	"errors"
	"testing"

	"sermo/internal/config"
	"sermo/internal/dockerctl"
	"sermo/internal/execx"
	"sermo/internal/servicemgr"
)

func TestResolveDockerControl(t *testing.T) {
	target, err := Resolve(context.Background(), "svc", map[string]any{
		config.SectionControl: map[string]any{
			dockerctl.ControlKeyType:      dockerctl.ControlType,
			dockerctl.ControlKeyContainer: "web",
		},
	}, servicemgr.BackendSystemd, nil, servicemgr.UnitResolver{})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if target.Unit != "web" || target.Backend != servicemgr.BackendDocker || target.Manager == nil {
		t.Fatalf("Resolve() = %+v, want docker/web target", target)
	}
	if target.BackendPIDs == nil {
		t.Fatal("Resolve() BackendPIDs is nil")
	}
}

func TestResolveWithFallbackDoesNotFallbackForControlledService(t *testing.T) {
	target, warning := ResolveWithFallback(context.Background(), "svc", map[string]any{
		config.SectionControl: map[string]any{
			dockerctl.ControlKeyType: dockerctl.ControlType,
		},
	}, servicemgr.BackendSystemd, nil, servicemgr.UnitResolver{})
	if warning == "" {
		t.Fatal("ResolveWithFallback() warning is empty, want control error")
	}
	if target.Unit != "" {
		t.Fatalf("ResolveWithFallback() target = %+v, want empty target", target)
	}
}

func TestResolveWithFallbackUsesConfiguredInitUnit(t *testing.T) {
	target, warning := ResolveWithFallback(context.Background(), "svc", map[string]any{
		config.ServiceKeyService: map[string]any{
			string(servicemgr.BackendSystemd): []any{"legacy-svc"},
		},
	}, servicemgr.BackendSystemd, nil, servicemgr.UnitResolver{Runner: noKnownUnitsRunner{}})
	if target.Unit != "legacy-svc" || target.Backend != servicemgr.BackendSystemd {
		t.Fatalf("ResolveWithFallback() target = %+v, want legacy-svc/systemd", target)
	}
	if warning == "" {
		t.Fatal("ResolveWithFallback() warning is empty, want failed resolution warning")
	}
}

func TestResolveWithFallbackUsesActiveBackendCandidate(t *testing.T) {
	target, warning := ResolveWithFallback(context.Background(), "svc", map[string]any{
		config.ServiceKeyService: map[string]any{
			string(servicemgr.BackendSystemd): []any{"svc@main"},
			string(servicemgr.BackendOpenRC):  []any{"svc.main"},
		},
	}, servicemgr.BackendOpenRC, nil, servicemgr.UnitResolver{Probe: noKnownUnitsProbe{}})
	if target.Unit != "svc.main" || target.Backend != servicemgr.BackendOpenRC {
		t.Fatalf("ResolveWithFallback() target = %+v, want svc.main/openrc", target)
	}
	if warning == "" {
		t.Fatal("ResolveWithFallback() warning is empty, want failed resolution warning")
	}
}

func TestResolveWithFallbackDoesNotFallbackWhenBackendHasNoCandidates(t *testing.T) {
	target, warning := ResolveWithFallback(context.Background(), "svc", map[string]any{
		config.ServiceKeyService: map[string]any{
			string(servicemgr.BackendSystemd): []any{"systemd-only"},
		},
	}, servicemgr.BackendOpenRC, nil, servicemgr.UnitResolver{})
	if warning == "" {
		t.Fatal("ResolveWithFallback() warning is empty, want unavailable-backend warning")
	}
	if target.Unit != "" {
		t.Fatalf("ResolveWithFallback() target = %+v, want empty target", target)
	}
}

func TestTargetCacheResolvesOnceAndWarnsOnce(t *testing.T) {
	probe := &countingProbe{}
	tree := map[string]any{
		config.ServiceKeyService: map[string]any{
			string(servicemgr.BackendOpenRC): []any{"svc.main"},
		},
	}
	cache := NewTargetCache()
	resolver := servicemgr.UnitResolver{Probe: probe}

	target, warning := cache.ResolveWithFallback(context.Background(), "svc", tree, servicemgr.BackendOpenRC, nil, resolver)
	if target.Unit != "svc.main" || warning == "" {
		t.Fatalf("first resolve = %+v warning=%q, want svc.main with a warning", target, warning)
	}
	probes := probe.calls
	if probes == 0 {
		t.Fatal("first resolve must probe the backend")
	}

	// The second caller (the web backend build) reuses the cached target: no
	// new probes, and the warning is reported only once per generation.
	target, warning = cache.ResolveWithFallback(context.Background(), "svc", tree, servicemgr.BackendOpenRC, nil, resolver)
	if target.Unit != "svc.main" {
		t.Fatalf("cached resolve target = %+v, want svc.main", target)
	}
	if warning != "" {
		t.Fatalf("cached resolve warning = %q, want empty (already reported)", warning)
	}
	if probe.calls != probes {
		t.Fatalf("cached resolve probed the backend again: %d -> %d calls", probes, probe.calls)
	}

	// A different service resolves independently.
	if target, _ = cache.ResolveWithFallback(context.Background(), "other", tree, servicemgr.BackendOpenRC, nil, resolver); target.Unit != "svc.main" {
		t.Fatalf("second service target = %+v", target)
	}
	if probe.calls == probes {
		t.Fatal("a different service must resolve on its own")
	}
}

func TestUnsupportedOnBackend(t *testing.T) {
	cases := []struct {
		name    string
		tree    map[string]any
		backend servicemgr.Backend
		want    bool
	}{
		{
			name: "explicit map without this backend",
			tree: map[string]any{config.ServiceKeyService: map[string]any{
				string(servicemgr.BackendSystemd): []any{"only-systemd"},
			}},
			backend: servicemgr.BackendOpenRC,
			want:    true,
		},
		{
			name: "explicit map with this backend",
			tree: map[string]any{config.ServiceKeyService: map[string]any{
				string(servicemgr.BackendOpenRC): []any{"svc"},
			}},
			backend: servicemgr.BackendOpenRC,
			want:    false,
		},
		{
			name:    "scalar service trusts the backend",
			tree:    map[string]any{config.ServiceKeyService: "svc"},
			backend: servicemgr.BackendOpenRC,
			want:    false,
		},
		{
			name:    "controlled service is never backend-unsupported",
			tree:    map[string]any{config.SectionControl: map[string]any{"type": "docker"}},
			backend: servicemgr.BackendOpenRC,
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := UnsupportedOnBackend(tc.tree, tc.backend, "svc"); got != tc.want {
				t.Fatalf("UnsupportedOnBackend() = %v, want %v", got, tc.want)
			}
		})
	}
}

type countingProbe struct{ calls int }

func (*countingProbe) CommandExists(string) bool { return false }

func (p *countingProbe) PathExists(string) bool {
	p.calls++
	return false
}

func (*countingProbe) ReadFile(string) ([]byte, error) { return nil, errors.New("not found") }

type noKnownUnitsRunner struct{}

func (noKnownUnitsRunner) Run(context.Context, string, ...string) (execx.Result, error) {
	return execx.Result{ExitCode: 1}, nil
}

type noKnownUnitsProbe struct{}

func (noKnownUnitsProbe) CommandExists(string) bool { return false }

func (noKnownUnitsProbe) PathExists(string) bool { return false }

func (noKnownUnitsProbe) ReadFile(string) ([]byte, error) { return nil, errors.New("not found") }
