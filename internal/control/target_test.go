package control

import (
	"context"
	"testing"

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
