package checks

import (
	"strings"
	"testing"
	"time"
)

// Every advertised single-shot type must be handled by buildCheck: a bare
// `{type: X}` entry may fail its own field validation, but it must never come
// back as "unsupported type" — that would mean the exported list (which config
// validation trusts) and the builder dispatch drifted apart.
func TestSingleShotCheckTypesAreBuildable(t *testing.T) {
	for _, typ := range SingleShotCheckTypes {
		_, warns := Build(map[string]any{"probe": map[string]any{"type": typ}}, Deps{DefaultTimeout: time.Second})
		for _, w := range warns {
			if strings.Contains(w, "unsupported type") {
				t.Errorf("%s: not handled by buildCheck: %s", typ, w)
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
		{typ: "storage", wantKnown: true, wantWatchable: true},
		{typ: "disk", wantKnown: true, wantWatchable: true},
		{typ: "metric", wantKnown: true, wantScoped: true},
		{typ: "process", wantKnown: true, wantHealth: true, wantScoped: true},
		{typ: "file", wantKnown: false},
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
