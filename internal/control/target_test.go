package control

import (
	"context"
	"errors"
	"testing"

	"sermo/internal/execx"
	"sermo/internal/servicemgr"
)

func TestResolveDockerControl(t *testing.T) {
	target, err := Resolve(context.Background(), "svc", map[string]any{
		"control": map[string]any{
			"type":      "docker",
			"container": "web",
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
		"control": map[string]any{
			"type": "docker",
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
		"service": map[string]any{
			"systemd": []any{"legacy-svc"},
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
		"service": map[string]any{
			"systemd": []any{"svc@main"},
			"openrc":  []any{"svc.main"},
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
		"service": map[string]any{
			"systemd": []any{"systemd-only"},
		},
	}, servicemgr.BackendOpenRC, nil, servicemgr.UnitResolver{})
	if warning == "" {
		t.Fatal("ResolveWithFallback() warning is empty, want unavailable-backend warning")
	}
	if target.Unit != "" {
		t.Fatalf("ResolveWithFallback() target = %+v, want empty target", target)
	}
}

type noKnownUnitsRunner struct{}

func (noKnownUnitsRunner) Run(context.Context, string, ...string) (execx.Result, error) {
	return execx.Result{ExitCode: 1}, nil
}

type noKnownUnitsProbe struct{}

func (noKnownUnitsProbe) CommandExists(string) bool { return false }

func (noKnownUnitsProbe) PathExists(string) bool { return false }

func (noKnownUnitsProbe) ReadFile(string) ([]byte, error) { return nil, errors.New("not found") }
