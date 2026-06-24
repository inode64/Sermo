package appinspect

import "testing"

// binaryPath resolves a daemon's binary by precedence: preflight binary.path,
// then a namespaced preflight binary, then variables.binary. Each "p != ”"
// guard is a fall-through point; pin them so a weakened guard is caught.
func TestBinaryPath(t *testing.T) {
	cases := []struct {
		name string
		tree map[string]any
		want string
	}{
		{
			name: "preflight binary.path takes precedence over variables",
			tree: map[string]any{
				"preflight": map[string]any{"binary": map[string]any{"path": "/usr/sbin/foo"}},
				"variables": map[string]any{"binary": "/var/lib/foo"},
			},
			want: "/usr/sbin/foo",
		},
		{
			name: "namespaced preflight binary when binary.path is empty",
			tree: map[string]any{
				"preflight": map[string]any{
					"binary":     map[string]any{"path": ""},
					"foo-binary": map[string]any{"type": "binary", "path": "/opt/foo/bin"},
				},
				"variables": map[string]any{"binary": "/var/lib/foo"},
			},
			want: "/opt/foo/bin",
		},
		{
			name: "variables.binary when preflight resolves nothing",
			tree: map[string]any{
				"preflight": map[string]any{},
				"variables": map[string]any{"binary": "/usr/bin/bar"},
			},
			want: "/usr/bin/bar",
		},
	}
	for _, c := range cases {
		if got := binaryPath(c.tree); got != c.want {
			t.Errorf("%s: binaryPath = %q, want %q", c.name, got, c.want)
		}
	}
}
