package virt

import (
	"testing"
)

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
