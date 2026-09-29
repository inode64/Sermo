package config

import (
	"slices"
	"testing"

	"sermo/internal/cfgval"
)

func TestServiceCandidates(t *testing.T) {
	perInit := map[string]any{"service": map[string]any{
		"systemd": []any{"nginx.service"},
		"openrc":  []any{"nginx"},
	}}
	cases := []struct {
		name      string
		tree      map[string]any
		backend   string
		wantCands []string
		wantTrust bool
	}{
		{"scalar is a trusted single candidate", map[string]any{"service": "nginx"}, "systemd", []string{"nginx"}, true},
		{"per-init picks the backend list (not trusted)", perInit, "systemd", []string{"nginx.service"}, false},
		{"per-init for the other backend", perInit, "openrc", []string{"nginx"}, false},
		{"per-init with no entry for the backend is unavailable", map[string]any{"service": map[string]any{"systemd": []any{"x"}}}, "openrc", nil, false},
		{"empty scalar falls back to the name", map[string]any{"service": ""}, "systemd", []string{"fallback"}, true},
		{"no service key falls back to the name", map[string]any{}, "systemd", []string{"fallback"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cands, trust := ServiceCandidates(tc.tree, tc.backend, "fallback")
			if !slices.Equal(cands, tc.wantCands) {
				t.Fatalf("candidates = %v, want %v", cands, tc.wantCands)
			}
			if trust != tc.wantTrust {
				t.Fatalf("trust = %v, want %v", trust, tc.wantTrust)
			}
		})
	}
}

// ${service} and the ${pidfile} fallback built from it must name the unit of
// the active backend; the first systemd candidate on an OpenRC host would make
// a `pidfile: "${pidfile}"` check watch a file that never exists.
func TestServiceBuiltinFollowsActiveBackend(t *testing.T) {
	for _, backend := range []string{backendSystemd, backendOpenRC} {
		t.Run(backend, func(t *testing.T) {
			resolved := resolveInstance(t, map[string]string{
				"sermo.yml": "engine: { backend: " + backend + " }\n" +
					"paths: { services: [ \"@ROOT@/services\" ], runtime: /run/sermo }\n" +
					"defaults: { policy: { cooldown: 5m } }\n",
				"services/foo.yml": `
name: foo
service: { systemd: [foo-sd], openrc: [foo-rc] }
checks:
  unit: { type: command, command: [/bin/echo, "${service}", "${pidfile}"] }
`,
			}, "foo")
			unit := map[string]string{backendSystemd: "foo-sd", backendOpenRC: "foo-rc"}[backend]
			want := []string{"/bin/echo", unit, "/run/" + unit + ".pid"}
			if got := cfgval.StringList(nested(t, resolved.Tree, "checks", "unit")["command"]); !slices.Equal(got, want) {
				t.Fatalf("command = %v, want %v", got, want)
			}
		})
	}
}
