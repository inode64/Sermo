package config

import (
	"testing"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
)

// alertAction is the action of a resolved rule's then.
func alertAction(t *testing.T, tree map[string]any, rule string) string {
	t.Helper()
	then := nested(t, tree, "rules", rule, "then")
	return cfgval.String(then["action"])
}

// Saturation is an alert, never a restart: every profile whose server can
// refuse clients ships a graded connection-usage alert, and the CPU-saturation
// restarts of the caches are alerts too.
func TestSaturationCatalogWatchesAlertOnly(t *testing.T) {
	for profile, check := range map[string]string{
		"mariadb": checks.CheckTypeSQL,
		"mysql":   checks.CheckTypeSQL,
		"redis":   "redis",
		"keydb":   "redis",
	} {
		t.Run(profile, func(t *testing.T) {
			tree := resolveCatalogService(t, profile, "openrc").Tree
			entry := nested(t, tree, "checks", "alert-if-connections-near-limit")
			if cfgval.String(entry[checks.CheckKeyType]) != check || cfgval.String(entry[checks.CheckKeySeverity]) != "warning" {
				t.Fatalf("connection alert check = %v", entry)
			}
			if _, ok := entry[checks.CheckKeyLevels].(map[string]any)["error"]; !ok {
				t.Fatalf("connection alert must escalate to error: %v", entry[checks.CheckKeyLevels])
			}
			if got := alertAction(t, tree, "alert-if-connections-near-limit"); got != "alert" {
				t.Fatalf("connection usage must alert, not %q", got)
			}
		})
	}
	// postgres-%v materializes per installed version, so read its document.
	pg := nested(t, catalogDocByName(t, repoRoot(t), "services", "postgres-%v"), "watches", "alert-if-connections-near-limit")
	if check := nested(t, pg, "check"); cfgval.String(check[checks.CheckKeyType]) != checks.CheckTypeSQL ||
		cfgval.String(check[checks.CheckKeySeverity]) != "warning" || check[checks.CheckKeyLevels] == nil {
		t.Fatalf("postgres connection alert check = %v", check)
	}
	if got := cfgval.String(nested(t, pg, "then")["action"]); got != "alert" {
		t.Fatalf("postgres connection usage must alert, not %q", got)
	}
	tree := resolveCatalogService(t, "memcached", "openrc").Tree
	if got := alertAction(t, tree, "alert-if-connections-rejected"); got != "alert" {
		t.Fatalf("memcached rejected connections must alert, not %q", got)
	}
	for _, profile := range []string{"redis", "keydb", "memcached"} {
		tree := resolveCatalogService(t, profile, "openrc").Tree
		if _, ok := nested(t, tree, "rules")["restart-if-worker-thread-hot"]; ok {
			t.Fatalf("%s must not restart a busy worker thread", profile)
		}
		if got := alertAction(t, tree, "alert-if-cpu-thread-high"); got != "alert" {
			t.Fatalf("%s cpu thread saturation must alert, not %q", profile, got)
		}
	}
}
