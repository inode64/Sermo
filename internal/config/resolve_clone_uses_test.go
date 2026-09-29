package config

import (
	"fmt"
	"strings"
	"testing"
)

// TestResolveRejectsCloneWithUses pins that a service declaring both clone and
// uses is rejected. The clone branch ignores uses entirely, so without this the
// catalog service the author asked to inherit would be silently dropped.
func TestResolveRejectsCloneWithUses(t *testing.T) {
	cfg := loadServiceConfig(t, `
name: svc
clone: other
uses: someservice
service: x
`)
	_, errs := cfg.Resolve("svc")
	if len(errs) == 0 {
		t.Fatal("expected a resolve error for clone+uses, got none")
	}
	if msg := fmt.Sprint(errs); !strings.Contains(msg, "both clone and uses") {
		t.Fatalf("errors = %v, want a clone/uses mutual-exclusion error", errs)
	}
}

// TestCatalogUsesIsOnlyFollowedFromTemplates pins where catalog `uses` works:
// a template inherits its base one level deep, while a non-template catalog
// service's `uses` is never followed, so it must be reported instead of
// silently dropping the checks the author meant to inherit. A template base
// that does not exist or is itself a template is reported too.
func TestCatalogUsesIsOnlyFollowedFromTemplates(t *testing.T) {
	cfg := loadCatalog(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/base.yml": `
name: base
service: base
watches:
  port: { check: { type: tcp, host: 127.0.0.1, port: 80 } }
`,
		"catalog/services/child.yml":  "name: child\nservice: child\nuses: base\n",
		"catalog/services/orphan.yml": "name: orphan-%i\nuses: missing\nservice: { systemd: [\"orphan@${instance}\"] }\n",
		"services/svc.yml":            "name: svc\nuses: child\n",
	})
	issues := Validate(cfg)
	mustHave(t, issues, "uses is only supported on catalog service templates")
	mustHave(t, issues, `uses "missing" must name an existing catalog service that is not a template`)
}
