package config

import (
	"strings"
	"testing"
)

func TestDetectHostname(t *testing.T) {
	// SERMO_HOSTNAME is taken verbatim (like SERMO_HOST), even an FQDN.
	t.Setenv("SERMO_HOSTNAME", "forced.example.com")
	if got := detectHostname(); got != "forced.example.com" {
		t.Fatalf("SERMO_HOSTNAME should be verbatim, got %q", got)
	}
	// Without the override, os.Hostname() is reduced to its short form, so the
	// result never carries a domain dot.
	t.Setenv("SERMO_HOSTNAME", "")
	if got := detectHostname(); strings.Contains(got, ".") {
		t.Fatalf("hostname should be short (no dot), got %q", got)
	}
}

func TestShortHostnameExportsDetected(t *testing.T) {
	old := detectedHostname
	detectedHostname = "node1"
	defer func() { detectedHostname = old }()
	if got := ShortHostname(); got != "node1" {
		t.Fatalf("ShortHostname() = %q, want node1", got)
	}
}

func TestBuiltinHostnameVar(t *testing.T) {
	old := detectedHostname
	detectedHostname = "node1"
	defer func() { detectedHostname = old }()

	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/mon.yml": `
name: mon
service: "ceph-mon@${hostname}"
checks:
  svc: { type: service, expect: active }
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("mon")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	// ${hostname} fills the instance id from the short hostname.
	if got := ServiceUnit(resolved.Tree, "mon"); got != "ceph-mon@node1" {
		t.Errorf("service unit = %q, want ceph-mon@node1", got)
	}
}

func TestUserHostnameVariableOverridesBuiltin(t *testing.T) {
	old := detectedHostname
	detectedHostname = "node1"
	defer func() { detectedHostname = old }()

	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/mon.yml": `
name: mon
service: "ceph-mon@${hostname}"
variables:
  hostname: custom-id
checks:
  svc: { type: service, expect: active }
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, _ := cfg.Resolve("mon")
	if got := ServiceUnit(resolved.Tree, "mon"); got != "ceph-mon@custom-id" {
		t.Errorf("service unit = %q, want user-defined ceph-mon@custom-id", got)
	}
}
