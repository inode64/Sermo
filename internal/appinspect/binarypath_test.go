package appinspect

import "testing"

func TestPreflightCheckPath(t *testing.T) {
	tests := []struct {
		name      string
		preflight map[string]any
		checkName string
		want      string
	}{
		{name: "missing check", preflight: map[string]any{}, checkName: "binary"},
		{name: "non mapping check", preflight: map[string]any{"binary": "/usr/bin/app"}, checkName: "binary"},
		{name: "empty path", preflight: map[string]any{"binary": map[string]any{"path": ""}}, checkName: "binary"},
		{name: "binary path", preflight: map[string]any{"binary": map[string]any{"path": "/usr/bin/app"}}, checkName: "binary", want: "/usr/bin/app"},
		{name: "file path", preflight: map[string]any{"file": map[string]any{"path": "/usr/lib/libapp.so"}}, checkName: "file", want: "/usr/lib/libapp.so"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := preflightCheckPath(tt.preflight, tt.checkName); got != tt.want {
				t.Fatalf("preflightCheckPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

// binaryPath resolves a service's binary by precedence: preflight binary.path,
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
