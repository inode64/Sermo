package utmp

import "testing"

// cString reads a NUL-terminated, space-padded C string. A leading NUL (index 0)
// must yield "" — the boundary the >=0 check guards.
func TestCString(t *testing.T) {
	cases := []struct {
		in   []byte
		want string
	}{
		{[]byte("hello\x00\x00"), "hello"}, // NUL-terminated
		{[]byte{0, 'a', 'b'}, ""},          // leading NUL -> empty (i==0)
		{[]byte("  spaced  "), "spaced"},   // no NUL, trimmed
		{[]byte("plain"), "plain"},
		{[]byte{}, ""},
	}
	for _, c := range cases {
		if got := cString(c.in); got != c.want {
			t.Errorf("cString(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
