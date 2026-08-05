package checks

import (
	"strings"
	"testing"
	"time"
)

// Every built-in spec must be unique and handled by buildCheck. A bare
// `{type: X}` entry may fail its own field validation, but it must never come
// back as "unsupported type" — that would mean the central registry drifted.
func TestBuiltinCheckSpecsAreUniqueAndBuildable(t *testing.T) {
	seen := make(map[string]struct{}, len(builtinCheckSpecs))
	for _, spec := range builtinCheckSpecs {
		if spec.info.Name == "" {
			t.Fatal("built-in check spec has no type name")
		}
		if _, duplicate := seen[spec.info.Name]; duplicate {
			t.Errorf("duplicate built-in check type %q", spec.info.Name)
		}
		seen[spec.info.Name] = struct{}{}
		if spec.build == nil {
			t.Errorf("%q has no builder", spec.info.Name)
		}
		_, warns := Build(map[string]any{"probe": map[string]any{"type": spec.info.Name}}, Deps{DefaultTimeout: time.Second})
		for _, warning := range warns {
			if strings.Contains(warning, "unsupported type") {
				t.Errorf("%s: not handled by buildCheck: %s", spec.info.Name, warning)
			}
		}
	}
}

func TestTypeInfoCapabilities(t *testing.T) {
	tests := []struct {
		typ           string
		wantKnown     bool
		wantHealth    bool
		wantScoped    bool
		wantWatchable bool
	}{
		{typ: "tcp", wantKnown: true, wantHealth: true, wantWatchable: true},
		{typ: "tcp_connections", wantKnown: true, wantWatchable: true},
		{typ: "ssh_idle", wantKnown: true, wantWatchable: true},
		{typ: "storage", wantKnown: true, wantWatchable: true},
		{typ: "metric", wantKnown: true, wantScoped: true},
		{typ: "process", wantKnown: true, wantHealth: true, wantScoped: true},
		// cert returns OK=true when the certificate is healthy (health-style),
		// like the http cert path; it must fire on failure, not on success.
		{typ: "cert", wantKnown: true, wantHealth: true, wantWatchable: true},
		{typ: "file", wantKnown: true, wantHealth: true, wantWatchable: true},
		{typ: "lockfile", wantKnown: true, wantHealth: true, wantWatchable: true},
	}
	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			if got := IsSingleShotType(tt.typ); got != tt.wantKnown {
				t.Fatalf("IsSingleShotType(%q) = %v, want %v", tt.typ, got, tt.wantKnown)
			}
			if got := IsHealthType(tt.typ); got != tt.wantHealth {
				t.Fatalf("IsHealthType(%q) = %v, want %v", tt.typ, got, tt.wantHealth)
			}
			if got := IsServiceScopedType(tt.typ); got != tt.wantScoped {
				t.Fatalf("IsServiceScopedType(%q) = %v, want %v", tt.typ, got, tt.wantScoped)
			}
			if tt.wantWatchable && IsServiceScopedType(tt.typ) {
				t.Fatalf("%q should be watchable but is marked service-scoped", tt.typ)
			}
		})
	}
}
