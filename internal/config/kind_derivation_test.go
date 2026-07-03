package config

import (
	"strings"
	"testing"
)

// kindDerivationGlobal wires a services, apps and storages directory so each
// loader path can be exercised from one config.
const kindDerivationGlobal = `
engine:
  backend: auto
paths:
  catalog: [ @ROOT@/catalog ]
  services: [ @ROOT@/services ]
  apps: [ @ROOT@/apps ]
  storages: [ @ROOT@/storages ]
  runtime: /run/sermo
defaults:
  policy:
    cooldown: 5m
`

// TestKindDerivedFromLocation verifies that a document with no top-level `kind:`
// is classified by the directory it loads from.
func TestKindDerivedFromLocation(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml":         kindDerivationGlobal,
		"services/demo.yml": "name: demo-svc\nuses: nothing\n",
		"apps/demo-app.yml": "name: demo-app\n",
		"storages/demo.yml": "name: mount-demo\npath: /mnt/demo\nmount: {}\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, tc := range []struct {
		reg  map[string]*Document
		name string
		kind string
	}{
		{cfg.Services, "demo-svc", kindService},
		{cfg.Apps, "demo-app", kindApp},
		{cfg.Storages, "mount-demo", kindStorage},
	} {
		doc, ok := tc.reg[tc.name]
		if !ok {
			t.Fatalf("%s not loaded into its registry", tc.name)
		}
		if doc.Kind != tc.kind {
			t.Errorf("%s kind = %q, want %q", tc.name, doc.Kind, tc.kind)
		}
	}
}

// TestKindMatchingDeclarationAccepted keeps backward compatibility: a `kind:`
// that agrees with the location still loads.
func TestKindMatchingDeclarationAccepted(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml":         kindDerivationGlobal,
		"services/demo.yml": "kind: service\nname: demo-svc\nuses: nothing\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Services["demo-svc"]; !ok {
		t.Fatalf("service with matching kind not loaded: %v", cfg.ServiceNames)
	}
}

// TestKindConflictingDeclarationRejected keeps the safety net: a `kind:` that
// disagrees with the location is an error, catching a misplaced file.
func TestKindConflictingDeclarationRejected(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml":         kindDerivationGlobal,
		"services/demo.yml": "kind: storage\nname: demo-svc\n",
	})
	_, err := Load(global)
	if err == nil || !strings.Contains(err.Error(), "located under a service directory but declares kind: storage") {
		t.Fatalf("Load error = %v, want service/storage conflict error", err)
	}
}

// TestCatalogServiceAndConfiguredServiceShareName verifies the daemon→service
// merge: a catalog service template and a configured service that `uses` it may
// share a name. They live in separate registries (catalog/services vs
// paths.services), so loading and validation must not flag a duplicate, and
// `uses:` must resolve the configured service against the catalog template.
func TestCatalogServiceAndConfiguredServiceShareName(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": kindDerivationGlobal,
		// catalog service template named "redis"
		"catalog/services/redis.yml": "name: redis\nvariables:\n  port: 6379\nchecks:\n  tcp:\n    type: tcp\n    host: 127.0.0.1\n    port: \"${port}\"\n",
		// configured service ALSO named "redis" that uses the template
		"services/redis.yml": "name: redis\nuses: redis\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.CatalogServices["redis"]; !ok {
		t.Fatalf("catalog service redis not loaded: %v", cfg.CatalogServiceNames)
	}
	if _, ok := cfg.Services["redis"]; !ok {
		t.Fatalf("configured service redis not loaded: %v", cfg.ServiceNames)
	}
	for _, issue := range Validate(cfg) {
		if strings.Contains(issue.Msg, "duplicate") {
			t.Fatalf("unexpected duplicate issue: %v", issue)
		}
	}
	// uses: must resolve against the catalog template (inherits its checks).
	resolved, errs := cfg.Resolve("redis")
	if len(errs) != 0 {
		t.Fatalf("resolve configured service: %v", errs)
	}
	if _, ok := resolved.Tree["checks"]; !ok {
		t.Fatalf("configured service did not inherit checks from catalog template: %v", resolved.Tree)
	}
}
