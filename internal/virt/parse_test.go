package virt

import (
	"testing"
)

func TestControlPortParsingConsistently(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		host     string
		port     any
		present  bool
		wantPort int
		wantErr  bool
	}{
		{name: "default", wantPort: DefaultPort},
		{name: "explicit IPv6 host", host: "::1", port: 16509, present: true, wantPort: 16509},
		{name: "non integer", port: "not-a-port", present: true, wantErr: true},
		{name: "out of range", port: 70000, present: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			domain := map[string]any{
				ControlKeyType:   ControlType,
				ControlKeyDomain: "vm01",
				ControlKeyHost:   tt.host,
			}
			network := map[string]any{
				ControlKeyType:    NetworkControlType,
				ControlKeyNetwork: "default",
				ControlKeyHost:    tt.host,
			}
			if tt.present {
				domain[ControlKeyPort] = tt.port
				network[ControlKeyPort] = tt.port
			}

			domainSpec, domainControlled, domainErr := SpecFromTree(map[string]any{sectionControl: domain})
			networkSpec, networkControlled, networkErr := NetworkSpecFromTree(map[string]any{sectionControl: network})
			if !domainControlled || !networkControlled {
				t.Fatalf("controlled = domain %v, network %v; want both true", domainControlled, networkControlled)
			}
			if (domainErr != nil) != tt.wantErr || (networkErr != nil) != tt.wantErr {
				t.Fatalf("errors = domain %v, network %v; want error %v", domainErr, networkErr, tt.wantErr)
			}
			if domainErr != nil {
				if domainErr.Error() != networkErr.Error() {
					t.Fatalf("error mismatch: domain %q, network %q", domainErr, networkErr)
				}
				return
			}
			if domainSpec.Port != tt.wantPort || networkSpec.Port != tt.wantPort {
				t.Errorf("ports = domain %d, network %d; want both %d", domainSpec.Port, networkSpec.Port, tt.wantPort)
			}
		})
	}
}

// ParseUUID accepts hyphenated or compact 32-hex strings and rejects anything
// else (wrong length, or right length but non-hex).
func TestParseUUID(t *testing.T) {
	const compact = "1234567890abcdef1234567890abcdef"
	for _, ok := range []string{
		"12345678-90ab-cdef-1234-567890abcdef",
		compact,
		"  " + compact + "  ",
	} {
		if _, err := ParseUUID(ok); err != nil {
			t.Errorf("ParseUUID(%q) unexpected error %v", ok, err)
		}
	}
	for _, bad := range []string{
		"",
		"too-short",
		compact[:30],
		"zz34567890abcdef1234567890abcdef", // 32 chars but non-hex prefix
	} {
		if _, err := ParseUUID(bad); err == nil {
			t.Errorf("ParseUUID(%q) = nil error, want failure", bad)
		}
	}
}
