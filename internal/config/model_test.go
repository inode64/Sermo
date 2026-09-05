package config

import (
	"slices"
	"testing"
)

func TestCatalogAliasResolvesUsesAndCatalogLookup(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/smb.yml": `
name: smb
aliases: [samba]
service: smb
`,
		"services/files.yml": `
name: files
uses: samba
`,
	})

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	catalog, errs := cfg.ResolveCatalog(CategoryService, "samba")
	if len(errs) != 0 {
		t.Fatalf("ResolveCatalog() errors = %v", errs)
	}
	if catalog.Name != "smb" {
		t.Fatalf("ResolveCatalog() name = %q, want smb", catalog.Name)
	}

	resolved, errs := cfg.Resolve("files")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	if got := ServiceUnit(resolved.Tree, resolved.Name); got != "smb" {
		t.Fatalf("service unit = %q, want smb", got)
	}
}

func TestServiceAliasesResolveToCanonicalName(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/smb.yml": `
name: smb
aliases: [samba]
service: smb
`,
		"services/smb.yml": `
name: smb
aliases: [fileshare]
uses: smb
`,
	})

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, alias := range []string{"fileshare", "samba"} {
		canonical, ok := cfg.CanonicalServiceName(alias)
		if !ok {
			t.Fatalf("CanonicalServiceName(%q) was not found", alias)
		}
		if canonical != "smb" {
			t.Fatalf("CanonicalServiceName(%q) = %q, want smb", alias, canonical)
		}
		resolved, errs := cfg.Resolve(alias)
		if len(errs) != 0 {
			t.Fatalf("Resolve(%q) errors = %v", alias, errs)
		}
		if resolved.Name != "smb" {
			t.Fatalf("Resolve(%q) name = %q, want smb", alias, resolved.Name)
		}
	}
}

func TestCatalogAliasDoesNotResolveNonCanonicalServiceInstance(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/smb.yml": `
name: smb
aliases: [samba]
service: smb
`,
		"services/files.yml": `
name: files
uses: smb
`,
	})

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if canonical, ok := cfg.CanonicalServiceName("samba"); ok {
		t.Fatalf("CanonicalServiceName(samba) = %q, want no match", canonical)
	}
	if _, errs := cfg.Resolve("samba"); len(errs) == 0 {
		t.Fatalf("Resolve(samba) succeeded, want unknown service")
	}
}

func TestStopInvariants(t *testing.T) {
	tree := map[string]any{
		"pidfile": "/run/svc.pid",
		"pidfiles": map[string]any{
			"helper": "/run/svc-helper.pid",
			"worker": []any{"/run/svc-worker.pid", "/run/svc-worker-legacy.pid"},
		},
		"stop_policy": map[string]any{
			"pidfile_absent":   true,
			"files_absent":     []any{"/run/svc/*.sock"},
			"clean_after_stop": true,
		},
	}
	pp, ff, cleanEnabled, _ := StopInvariants(tree)
	wantPidfiles := []string{"/run/svc.pid", "/run/svc-helper.pid", "/run/svc-worker.pid", "/run/svc-worker-legacy.pid"}
	if !slices.Equal(pp, wantPidfiles) {
		t.Fatalf("pidfile paths = %v, want %v", pp, wantPidfiles)
	}
	if len(ff) != 1 || ff[0] != "/run/svc/*.sock" || !cleanEnabled {
		t.Fatalf("files=%v cleanEnabled=%v", ff, cleanEnabled)
	}
	// pidfile_absent omitted -> no pidfile paths even if pidfile is declared.
	pp2, _, _, _ := StopInvariants(map[string]any{
		"pidfile":     tree["pidfile"],
		"stop_policy": map[string]any{"files_absent": []any{"/x"}},
	})
	if len(pp2) != 0 {
		t.Fatalf("pidfile_absent off must yield no pidfile paths, got %v", pp2)
	}
}
