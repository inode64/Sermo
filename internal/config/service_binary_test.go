package config

import (
	"slices"
	"strings"
	"testing"

	"sermo/internal/cfgval"
)

func TestPreflightPath(t *testing.T) {
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
			if got := PreflightPath(tt.preflight, tt.checkName); got != tt.want {
				t.Fatalf("PreflightPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ServiceBinary resolves a service's binary by precedence: preflight binary.path,
// then a namespaced preflight binary, then variables.binary. Each "p != ”"
// guard is a fall-through point; pin them so a weakened guard is caught.
func TestServiceBinary(t *testing.T) {
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
		if got := ServiceBinary(c.tree); got != c.want {
			t.Errorf("%s: ServiceBinary = %q, want %q", c.name, got, c.want)
		}
	}
}

// A libraries check that names no binary takes the service's own executable,
// here the one its linked app declares, so a fleet policy does not pin the
// versioned file a package upgrade deletes. One that names a binary keeps it.
func TestLibrariesCheckDefaultsToTheServiceBinary(t *testing.T) {
	resolved := resolveValidInstance(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/rspamd.yml": `
name: rspamd
variables:
  binary: /usr/bin/rspamd
preflight:
  binary: { type: binary, path: "${binary}" }
`,
		"catalog/services/rspamd.yml": `
name: rspamd
service: rspamd
apps: [rspamd]
checks:
  service: { type: service, expect: active }
`,
		"services/rspamd.yml": `
name: rspamd
uses: rspamd
preflight:
  update-libraries: { type: libraries }
  pinned: { type: libraries, binary: /usr/bin/rspamadm }
checks:
  libraries: { type: libraries }
`,
	}, "rspamd")
	preflight := nested(t, resolved.Tree, "preflight")
	for name, want := range map[string]string{"update-libraries": "/usr/bin/rspamd", "pinned": "/usr/bin/rspamadm"} {
		if got := cfgval.AsString(nested(t, preflight, name)["binary"]); got != want {
			t.Fatalf("preflight %s binary = %q, want %q", name, got, want)
		}
	}
	if got := cfgval.AsString(nested(t, nested(t, resolved.Tree, "checks"), "libraries")["binary"]); got != "/usr/bin/rspamd" {
		t.Fatalf("check binary = %q, want the service binary", got)
	}
}

func TestLibrariesCheckWithoutAServiceBinaryIsAnError(t *testing.T) {
	cfg := loadCatalog(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/tool.yml": `
name: tool
service: tool
preflight:
  update-libraries: { type: libraries }
`,
	})
	_, errs := cfg.Resolve("tool")
	if !slices.ContainsFunc(errs, func(e string) bool {
		return strings.Contains(e, "preflight.update-libraries: a libraries check without binary needs a service that declares one")
	}) {
		t.Fatalf("Resolve() errors = %v, want the missing service binary", errs)
	}
}
