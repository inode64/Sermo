package config

import (
	"strings"
	"testing"
)

// TestGlobalBuiltinsBakedInDefaults pins that ${arch}/${os} are substituted in
// the global document (defaults.variables, watches, …), not only in catalog/
// service docs. Before the fix these tokens survived in Global.Raw and tripped
// the no-nested-variables validation, so the same token worked in a catalog doc
// but failed in defaults.
func TestGlobalBuiltinsBakedInDefaults(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  services: [ @ROOT@/services ]
defaults:
  policy: { cooldown: 5m }
  variables:
    plugindir: /usr/lib/${arch}
    osdir: /etc/${os}
`,
		"services/svc.yml": `
name: svc
service: svc
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	vars, _ := cfg.Global.Defaults["variables"].(map[string]any)
	if got := vars["plugindir"]; got != "/usr/lib/"+detectedArch {
		t.Fatalf("plugindir = %v, want baked %q", got, "/usr/lib/"+detectedArch)
	}
	if got := vars["osdir"]; got != "/etc/"+detectedOS {
		t.Fatalf("osdir = %v, want baked %q", got, "/etc/"+detectedOS)
	}
	for _, is := range Validate(cfg) {
		if strings.Contains(is.Msg, "nested variable") || strings.Contains(is.Msg, "references another variable") {
			t.Fatalf("unexpected nested-variable issue: %v", is)
		}
	}
}
