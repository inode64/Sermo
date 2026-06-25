package config

import (
	"strings"
	"testing"
)

// kindDerivationGlobal wires a services, apps and mounts directory so each
// loader path can be exercised from one config.
const kindDerivationGlobal = `
engine:
  backend: auto
paths:
  catalog: [ @ROOT@/catalog ]
  services: [ @ROOT@/services ]
  apps: [ @ROOT@/apps ]
  mounts: [ @ROOT@/mounts ]
  runtime: /run/sermo
defaults:
  policy:
    cooldown: 5m
`

// TestKindDerivedFromLocation verifies that a document with no top-level `kind:`
// is classified by the directory it loads from.
func TestKindDerivedFromLocation(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml":             kindDerivationGlobal,
		"services/demo.yml":     "name: demo-svc\nuses: nothing\n",
		"apps/demo-app.yml":     "name: demo-app\n",
		"mounts/demo-mount.yml": "name: mount-demo\npath: /mnt/demo\n",
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
		{cfg.Mounts, "mount-demo", kindMount},
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
		"services/demo.yml": "kind: mount\nname: demo-svc\n",
	})
	_, err := Load(global)
	if err == nil || !strings.Contains(err.Error(), "located under a service directory but declares kind: mount") {
		t.Fatalf("Load error = %v, want service/mount conflict error", err)
	}
}
