package config

import (
	"testing"

	"sermo/internal/cfgval"
)

func TestArchVariableBaked(t *testing.T) {
	old := detectedArch
	detectedArch = "aarch64"
	defer func() { detectedArch = old }()

	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/qemu.yml": `
name: qemu
display_name: "QEMU"
variables:
  binary: "/usr/bin/qemu-system-${arch}"
preflight:
  binary: { type: binary, path: "${binary}" }
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// ${arch} is baked into the variable value (so the no-nested-variables rule
	// never sees it) and flows through expansion.
	if got := DocumentBinary(cfg.CatalogServices["qemu"].Body); got != "/usr/bin/qemu-system-aarch64" {
		t.Errorf("baked binary = %q, want /usr/bin/qemu-system-aarch64", got)
	}
	resolved, errs := cfg.ResolveCatalog(CategoryService, "qemu")
	if len(errs) != 0 {
		t.Fatalf("ResolveCatalog() errors = %v", errs)
	}
	bin := nested(t, resolved.Tree, "preflight", "binary")
	if cfgval.String(bin["path"]) != "/usr/bin/qemu-system-aarch64" {
		t.Errorf("resolved binary path = %v, want /usr/bin/qemu-system-aarch64", bin["path"])
	}
}
