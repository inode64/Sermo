package app

import (
	"context"
	"testing"

	"sermo/internal/checks"
	"sermo/internal/servicemgr"
)

// staleBinaryBackend builds the minimal WebBackend the reason lookup needs: the
// injected check's published snapshot plus the name->type map the resolver
// fills from the service tree.
func staleBinaryBackend(t *testing.T, ok bool) (*WebBackend, *webEntry) {
	t.Helper()
	snaps := NewSnapshots()
	snaps.PublishWithCheckTypes("web",
		map[string]checks.Result{"stale-binary": {Check: "stale-binary", OK: ok}},
		map[string]bool{"stale-binary": true},
		map[string]string{"stale-binary": checks.CheckTypeStaleBinary})

	entry := &webEntry{
		checkNames: []string{"stale-binary"},
		checkTypes: map[string]string{"stale-binary": checks.CheckTypeStaleBinary},
		status:     func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
	}
	return &WebBackend{
		order:     []string{"web"},
		entries:   map[string]*webEntry{"web": entry},
		snapshots: snaps,
	}, entry
}

// The reason must come from the check the worker published. This is the whole
// point of the rewrite: the render path no longer discovers processes.
func TestServiceWarningReasonFromPublishedCheck(t *testing.T) {
	b, entry := staleBinaryBackend(t, false)
	if got := b.serviceWarningReason("web", entry); got != warningReasonStaleBinary {
		t.Fatalf("want %q, got %q", warningReasonStaleBinary, got)
	}
}

func TestServiceWarningReasonEmptyWhenCheckPasses(t *testing.T) {
	b, entry := staleBinaryBackend(t, true)
	if got := b.serviceWarningReason("web", entry); got != "" {
		t.Fatalf("a passing check must report no reason, got %q", got)
	}
}

// Before the check has ever run there is no snapshot, and the dashboard falls
// back to the generic wording rather than claiming a cause it cannot support.
func TestServiceWarningReasonEmptyWithoutSnapshot(t *testing.T) {
	b, entry := staleBinaryBackend(t, false)
	b.snapshots = NewSnapshots() // nothing published yet
	if got := b.serviceWarningReason("web", entry); got != "" {
		t.Fatalf("want no reason without a snapshot, got %q", got)
	}
}

// A service with no stale-binary check (no process selectors) must not be
// mistaken for one whose check passed.
func TestServiceWarningReasonIgnoresOtherCheckTypes(t *testing.T) {
	snaps := NewSnapshots()
	snaps.PublishWithCheckTypes("web",
		map[string]checks.Result{"probe": {Check: "probe", OK: false}},
		map[string]bool{"probe": true},
		map[string]string{"probe": checks.CheckTypeTCP})
	entry := &webEntry{
		checkNames: []string{"probe"},
		checkTypes: map[string]string{"probe": checks.CheckTypeTCP},
	}
	b := &WebBackend{order: []string{"web"}, entries: map[string]*webEntry{"web": entry}, snapshots: snaps}

	if got := b.serviceWarningReason("web", entry); got != "" {
		t.Fatalf("a failing unrelated check must not report a stale binary, got %q", got)
	}
}

func TestServiceWarningReasonNilSafe(t *testing.T) {
	b, _ := staleBinaryBackend(t, false)
	if got := b.serviceWarningReason("web", nil); got != "" {
		t.Fatalf("want no reason for a nil entry, got %q", got)
	}
}

// The readings renderer must surface what the check computed; without the
// dispatch entry the paths and PIDs are silently dropped.
func TestStaleBinaryCheckReadingsSurfacePathAndPIDs(t *testing.T) {
	readings := checkReadings(checks.CheckTypeStaleBinary, map[string]any{
		checks.DataKeyPath: "/usr/bin/ovs-vswitchd",
		checks.DataKeyPIDs: "1928,1898",
	})
	var gotPath, gotPIDs bool
	for _, r := range readings {
		switch r.Value {
		case "/usr/bin/ovs-vswitchd":
			gotPath = true
		case "1928,1898":
			gotPIDs = true
		}
	}
	if !gotPath || !gotPIDs {
		t.Fatalf("readings must carry the path and the pids, got %+v", readings)
	}
}
