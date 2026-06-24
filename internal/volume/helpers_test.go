package volume

import (
	"testing"
	"time"
)

// Expander.timeout falls back to 30s only when no positive timeout is set.
func TestExpanderTimeout(t *testing.T) {
	if got := (Expander{Timeout: 5 * time.Second}).timeout(); got != 5*time.Second {
		t.Errorf("configured timeout = %v, want 5s", got)
	}
	if got := (Expander{}).timeout(); got != 30*time.Second {
		t.Errorf("zero timeout = %v, want the 30s default", got)
	}
}

// cleanMountpoint trims a trailing slash but normalizes the empty/root result
// back to "/".
func TestCleanMountpoint(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"/data", "/data"},
		{"/data/", "/data"}, // trailing slash trimmed
		{"/a/b/", "/a/b"},
		{"/", "/"}, // root trims to "" then normalizes back to "/"
		{"", "/"},  // empty normalizes to "/"
	} {
		if got := cleanMountpoint(c.in); got != c.want {
			t.Errorf("cleanMountpoint(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
