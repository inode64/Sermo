package servicemgr

import (
	"testing"
	"time"
)

// UnitResolver.timeout falls back to the detect default only for a non-positive
// configured timeout.
func TestUnitResolverTimeout(t *testing.T) {
	if got := (UnitResolver{Timeout: 5 * time.Second}).timeout(); got != 5*time.Second {
		t.Errorf("configured = %v, want 5s", got)
	}
	if got := (UnitResolver{}).timeout(); got != defaultDetectTimeout {
		t.Errorf("zero = %v, want default %v", got, defaultDetectTimeout)
	}
	if got := (UnitResolver{Timeout: -time.Second}).timeout(); got != defaultDetectTimeout {
		t.Errorf("negative = %v, want default %v", got, defaultDetectTimeout)
	}
}

// shellWord strips a single matched pair of surrounding quotes (single or
// double) and trims; an unbalanced or unquoted value is returned as-is.
func TestShellWord(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`"hello"`, "hello"},
		{`'world'`, "world"},
		{`plain`, "plain"},
		{`"unbalanced`, `"unbalanced`}, // only one quote: unchanged
		{`x`, "x"},                     // too short to be quoted
		{`""`, ""},
		{`  "spaced"  `, "spaced"},
	} {
		if got := shellWord(c.in); got != c.want {
			t.Errorf("shellWord(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
