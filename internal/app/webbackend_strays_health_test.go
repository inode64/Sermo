package app

import (
	"testing"
	"time"

	"sermo/internal/checks"
)

// straysHealthBackend publishes one failing strays snapshot beside a passing
// service check, wired the way the resolver injects it: verdictless.
func straysHealthBackend(t *testing.T, reports string) *WebBackend {
	t.Helper()
	names := []string{"service", "strays"}
	types := map[string]string{"service": checks.CheckTypeService, "strays": checks.CheckTypeStrays}
	snaps := NewSnapshots()
	snaps.PublishWithCheckTypes("web", map[string]checks.Result{
		"service": {Check: "service", OK: true},
		"strays":  {Check: "strays", OK: false, Data: map[string]any{checks.DataKeyType: checks.CheckTypeStrays, checks.DataKeyCount: 3}},
	}, map[string]bool{"service": true, "strays": true}, types)

	b := webBackendWithEntry(snaps, names, types)
	e := b.entries["web"]
	e.interval = time.Minute
	e.checkReports = map[string]string{"strays": reports}
	publishedAt := time.Now()
	b.now = func() time.Time { return publishedAt }
	return b
}

// The check is injected into every init-managed service on every host, so if a
// stray degraded the service the whole fleet would read as failing the moment one
// leftover appeared. A leftover is worth naming, not worth calling the daemon sick:
// `reports: state` is what keeps it out of health, and this pins that wiring rather
// than the mechanism it relies on.
func TestFailingStraysCheckDoesNotDegradeTheService(t *testing.T) {
	b := straysHealthBackend(t, checks.ReportsState)

	failing, health := b.serviceCheckHealth("web", b.entries["web"], true)
	if failing != 0 {
		t.Fatalf("checks failing = %d, want 0: a stray is verdictless", failing)
	}
	if health != TargetStateOK {
		t.Fatalf("check health = %q, want %q", health, TargetStateOK)
	}
	// And it raises no warning either: the warning reason is stale-binary's alone.
	if reason := b.serviceWarningReason("web", b.entries["web"]); reason != "" {
		t.Fatalf("warning reason = %q, want none", reason)
	}
}

// The guard above is only as good as the `reports:` the resolver injects, so prove
// the same snapshot does count when that mode is dropped. Without this the first
// test would keep passing if the injection silently stopped setting it.
func TestFailingStraysCheckWouldCountWithoutTheVerdictlessMode(t *testing.T) {
	b := straysHealthBackend(t, "")

	if failing, _ := b.serviceCheckHealth("web", b.entries["web"], true); failing != 1 {
		t.Fatalf("checks failing = %d, want 1 once the check carries a verdict", failing)
	}
}
