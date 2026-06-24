package execx

import (
	"os/exec"
	"testing"
)

// numericUserID decides between LookupId and Lookup; pin the digit-range
// boundaries ('/' is just below '0', ':' just above '9') and the empty case.
func TestNumericUserID(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"", false},
		{"0", true}, {"9", true}, {"1234", true},
		{"/", false}, {":", false}, // the chars adjacent to '0' and '9'
		{"12a", false}, {"abc", false}, {"-1", false},
	} {
		if got := numericUserID(c.in); got != c.want {
			t.Errorf("numericUserID(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// An empty (or whitespace-only) command user fails closed with a specific
// message, before any user lookup is attempted.
func TestPrepareCommandUserEmpty(t *testing.T) {
	err := prepareCommandUser(&exec.Cmd{}, "   ")
	if err == nil || err.Error() != "execx: command user is empty" {
		t.Fatalf("prepareCommandUser(blank) = %v, want \"execx: command user is empty\"", err)
	}
}
