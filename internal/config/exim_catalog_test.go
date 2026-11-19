package config

import (
	"testing"

	"sermo/internal/cfgval"
)

// TestCatalogEximHintsCleanupReadsAbsentDatabaseAsEmpty pins that the hints
// database cleanup watches read a database Exim has not created yet as empty.
// An instance that never ran a callout, or never deferred a message, has no such
// file, and the watch showed failed on the dashboard although nothing was wrong.
func TestCatalogEximHintsCleanupReadsAbsentDatabaseAsEmpty(t *testing.T) {
	exim := catalogDocByName(t, repoRoot(t), "services", "exim")
	for _, watch := range []string{"tidy-callout-db-if-large", "tidy-retry-db-if-large"} {
		check := nested(t, exim, "watches", watch, "check")
		if got := cfgval.String(check["absent_value"]); got != "0" {
			t.Errorf("%s absent_value = %q, want 0", watch, got)
		}
	}
}
