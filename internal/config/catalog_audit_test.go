package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These audits load the real repo artifacts — the packaged catalog, the shipped
// sermo.yml and the examples — and require them to resolve and validate
// cleanly, so a catalog definition that no current service exercises (the way
// kafka's nested variables and rabbitmq's incomplete kill_only_if once shipped
// broken) cannot regress unnoticed.

// repoRoot returns the repository root, skipping the test when the catalog is
// not present (e.g. a vendored build of just this package).
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "catalog")); err != nil {
		t.Skipf("catalog dir not found: %v", err)
	}
	return root
}

// TestRealCatalogAllDaemonsValidate enables every instantiable catalog daemon
// as a service and validates the whole set. Version templates (%v/%n) cannot be
// materialized off-host, so only the concrete daemon names are exercised.
func TestRealCatalogAllDaemonsValidate(t *testing.T) {
	root := repoRoot(t)
	catalogDir := filepath.Join(root, "catalog")

	writeGlobal := func(dir, enabled string) string {
		global := filepath.Join(dir, "sermo.yml")
		body := "engine: { backend: systemd }\n" +
			"paths:\n  catalog: [" + catalogDir + "]\n  includes: [" + enabled + "]\n  runtime: /run/sermo\n" +
			"defaults:\n  policy: { cooldown: 5m }\n"
		if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return global
	}

	// First load with no services, just to enumerate the daemon registry.
	probeDir := t.TempDir()
	emptyEnabled := filepath.Join(probeDir, "enabled")
	if err := os.MkdirAll(emptyEnabled, 0o755); err != nil {
		t.Fatal(err)
	}
	probe, err := Load(writeGlobal(probeDir, emptyEnabled))
	if err != nil {
		t.Fatalf("Load (probe): %v", err)
	}

	dir := t.TempDir()
	enabled := filepath.Join(dir, "enabled")
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, name := range probe.DaemonNames {
		if strings.Contains(name, "%") {
			continue
		}
		svc := "kind: service\nname: " + name + "-audit\nuses: " + name + "\n"
		if err := os.WriteFile(filepath.Join(enabled, name+".yml"), []byte(svc), 0o644); err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count == 0 {
		t.Fatal("no instantiable catalog daemons found")
	}

	cfg, err := Load(writeGlobal(dir, enabled))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, issue := range Validate(cfg) {
		t.Errorf("catalog daemon fails validation: %s", issue)
	}
}

// TestShippedGlobalConfigValidates points the shipped configs/sermo.yml at the
// repo catalog and validates it with its bundled apps services.
func TestShippedGlobalConfigValidates(t *testing.T) {
	root := repoRoot(t)

	src, err := os.ReadFile(filepath.Join(root, "configs", "sermo.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ReplaceAll(string(src), "/usr/share/sermo/catalog", filepath.Join(root, "catalog"))
	body = strings.ReplaceAll(body, "    - /etc/sermo/catalog-available\n", "")
	if body == string(src) {
		t.Fatal("configs/sermo.yml no longer lists the packaged catalog paths; update this rewrite")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sermo.yml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// The shipped config enables no services out of the box; when a bundled
	// "apps" include dir reappears, copy it so its services validate too.
	if bundled := filepath.Join(root, "configs", "apps"); dirExists(bundled) {
		copyYAMLDir(t, bundled, filepath.Join(dir, "apps"))
	}

	cfg, err := Load(filepath.Join(dir, "sermo.yml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, issue := range Validate(cfg) {
		t.Errorf("shipped sermo.yml fails validation: %s", issue)
	}
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// copyYAMLDir copies the top-level *.yml files of src into dst.
func copyYAMLDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
