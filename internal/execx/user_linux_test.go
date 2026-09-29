package execx

import (
	"os/exec"
	osuser "os/user"
	"slices"
	"strings"
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

// A command run as another user gets that user's HOME, USER and LOGNAME
// instead of the daemon's (HOME=/root is unreadable to it), and keeps every
// other variable.
func TestWithUserIdentityEnv(t *testing.T) {
	daemonEnv := []string{"HOME=/root", "USER=root", "LOGNAME=root", "PATH=/usr/bin", "HOMEDIR_HINT=x"}
	got := withUserIdentityEnv(daemonEnv, &osuser.User{Username: "mysql", HomeDir: "/var/lib/mysql"})
	want := []string{"PATH=/usr/bin", "HOMEDIR_HINT=x", "HOME=/var/lib/mysql", "USER=mysql", "LOGNAME=mysql"}
	if !slices.Equal(got, want) {
		t.Fatalf("withUserIdentityEnv = %q, want %q", got, want)
	}
	// A user without a home directory must not inherit the daemon's.
	noHome := withUserIdentityEnv(daemonEnv, &osuser.User{Username: "nobody"})
	if slices.ContainsFunc(noHome, func(e string) bool { return strings.HasPrefix(e, "HOME=") }) {
		t.Fatalf("user without a home kept HOME: %q", noHome)
	}
	// A nil env is the inherited environment, overridden the same way.
	t.Setenv("USER", "root")
	if inherited := withUserIdentityEnv(nil, &osuser.User{Username: "mysql"}); !slices.Contains(inherited, "USER=mysql") || slices.Contains(inherited, "USER=root") {
		t.Fatalf("inherited environment not overridden: USER entries in %q", inherited)
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
