package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/servicemgr"
)

// staleBinaryBackend builds the minimal WebBackend the reason lookup needs: the
// injected check's published snapshot plus the name->type map the resolver
// fills from the service tree.
func staleBinaryBackend(t *testing.T, ok bool) (*WebBackend, *webEntry) {
	t.Helper()
	at := time.Date(2026, 8, 3, 21, 0, 0, 0, time.UTC)
	snaps := NewSnapshots()
	snaps.now = func() time.Time { return at }
	snaps.PublishWithCheckTypes("web",
		map[string]checks.Result{"stale-binary": {Check: "stale-binary", OK: ok, Reports: checks.ReportsState}},
		map[string]bool{"stale-binary": true},
		map[string]string{"stale-binary": checks.CheckTypeStaleBinary})

	entry := &webEntry{
		checkNames:     []string{"stale-binary"},
		checkTypes:     map[string]string{"stale-binary": checks.CheckTypeStaleBinary},
		checkReports:   map[string]string{"stale-binary": checks.ReportsState},
		checkIntervals: map[string]time.Duration{"stale-binary": time.Minute},
		interval:       time.Minute,
		status:         func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
	}
	return &WebBackend{
		order:     []string{"web"},
		entries:   map[string]*webEntry{"web": entry},
		snapshots: snaps,
		now:       func() time.Time { return at },
	}, entry
}

// The reason must come from the check the worker published. This is the whole
// point of the rewrite: the render path no longer discovers processes.
func TestServiceStateReasonFromPublishedCheck(t *testing.T) {
	b, entry := staleBinaryBackend(t, false)
	if got := b.serviceStateReason("web", entry); got != stateReasonStaleBinary {
		t.Fatalf("want %q, got %q", stateReasonStaleBinary, got)
	}
}

func TestServiceStateReasonEmptyWhenCheckPasses(t *testing.T) {
	b, entry := staleBinaryBackend(t, true)
	if got := b.serviceStateReason("web", entry); got != "" {
		t.Fatalf("a passing check must report no reason, got %q", got)
	}
}

// Before the check has ever run there is no snapshot, and the dashboard falls
// back to the generic wording rather than claiming a cause it cannot support.
func TestServiceStateReasonEmptyWithoutSnapshot(t *testing.T) {
	b, entry := staleBinaryBackend(t, false)
	b.snapshots = NewSnapshots() // nothing published yet
	if got := b.serviceStateReason("web", entry); got != "" {
		t.Fatalf("want no reason without a snapshot, got %q", got)
	}
}

// A service with no stale-binary check (no process selectors) must not be
// mistaken for one whose check passed.
func TestServiceStateReasonIgnoresOtherCheckTypes(t *testing.T) {
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

	if got := b.serviceStateReason("web", entry); got != "" {
		t.Fatalf("a failing unrelated check must not report a stale binary, got %q", got)
	}
}

func TestServiceStateReasonNilSafe(t *testing.T) {
	b, _ := staleBinaryBackend(t, false)
	if got := b.serviceStateReason("web", nil); got != "" {
		t.Fatalf("want no reason for a nil entry, got %q", got)
	}
}

func TestWebBackendStaleBinaryRequiresRestartWithoutFailingHealth(t *testing.T) {
	b, entry := staleBinaryBackend(t, false)
	svc := b.view(context.Background(), "web", entry)
	if svc.State != TargetStateRestartRequired || svc.CheckHealth != checkHealthWarning || svc.ChecksFailing != 0 || svc.StateReason != stateReasonStaleBinary {
		t.Fatalf("stale binary service = %+v, want restart required without failed health", svc)
	}

	b, entry = staleBinaryBackend(t, true)
	svc = b.view(context.Background(), "web", entry)
	if svc.State != TargetStateMonitored || svc.CheckHealth != TargetStateOK || svc.StateReason != "" {
		t.Fatalf("fresh binary service = %+v, want monitored healthy", svc)
	}
}

// dmeventd declares no process selectors, so it gets no injected stale-binary
// check; its exact-exe process check is what notices the replaced binary. The
// service must then read restart_required like any other stale binary instead
// of failed, which sent operators after a daemon that was serving all along.
func TestWebBackendProcessCheckReplacedBinaryRequiresRestart(t *testing.T) {
	at := time.Date(2026, 8, 3, 21, 0, 0, 0, time.UTC)
	types := map[string]string{"process": checks.CheckTypeProcess}
	snaps := NewSnapshots()
	snaps.now = func() time.Time { return at }
	snaps.PublishWithCheckTypes("dmeventd", map[string]checks.Result{
		"process": {Check: "process", OK: false, Reports: checks.ReportsState,
			Data: map[string]any{checks.DataKeyReplacedBinaries: "/usr/bin/dmeventd"}},
	}, map[string]bool{"process": true}, types)
	entry := &webEntry{
		checkNames:     []string{"process"},
		checkTypes:     types,
		checkIntervals: map[string]time.Duration{"process": time.Minute},
		interval:       time.Minute,
		status:         func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
	}
	b := &WebBackend{
		order:     []string{"dmeventd"},
		entries:   map[string]*webEntry{"dmeventd": entry},
		snapshots: snaps,
		now:       func() time.Time { return at },
	}

	svc := b.view(context.Background(), "dmeventd", entry)
	if svc.State != TargetStateRestartRequired || svc.StateReason != stateReasonStaleBinary || svc.ChecksFailing != 0 {
		t.Fatalf("replaced binary seen by the process check = %+v, want restart required", svc)
	}
	// The detail row must not read "ok" beside a message that says the process
	// is absent and its binary replaced: the reading renders as the state
	// sensor it is, like the injected stale-binary check does.
	ch := b.checkView("process", entry, snaps.Get("dmeventd"))
	if ch.OK || ch.Reports != checks.ReportsState {
		t.Fatalf("process check view = %+v, want an inactive state reading", ch)
	}
}

func TestWebBackendConfigurationWarningOutranksRestartRequired(t *testing.T) {
	at := time.Date(2026, 8, 3, 21, 0, 0, 0, time.UTC)
	names := []string{config.ConfigurationCheckName, "stale-binary"}
	types := map[string]string{
		config.ConfigurationCheckName: checks.CheckTypeCommand,
		"stale-binary":                checks.CheckTypeStaleBinary,
	}
	snaps := NewSnapshots()
	snaps.now = func() time.Time { return at }
	snaps.PublishWithCheckTypes("web", map[string]checks.Result{
		config.ConfigurationCheckName: {Check: config.ConfigurationCheckName, OK: false, Severity: checks.SeverityWarning},
		"stale-binary":                {Check: "stale-binary", OK: false, Reports: checks.ReportsState},
	}, map[string]bool{config.ConfigurationCheckName: true, "stale-binary": true}, types)
	entry := &webEntry{
		checkNames:      names,
		checkTypes:      types,
		checkReports:    map[string]string{"stale-binary": checks.ReportsState},
		checkSeverities: map[string]string{config.ConfigurationCheckName: checks.SeverityWarning},
		checkIntervals:  map[string]time.Duration{config.ConfigurationCheckName: time.Minute, "stale-binary": time.Minute},
		interval:        time.Minute,
		status:          func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
	}
	b := &WebBackend{
		order:     []string{"web"},
		entries:   map[string]*webEntry{"web": entry},
		snapshots: snaps,
		now:       func() time.Time { return at },
	}

	svc := b.view(context.Background(), "web", entry)
	if svc.State != TargetStateWarning || svc.StateReason != stateReasonConfigurationInvalid || svc.ChecksFailing != 0 {
		t.Fatalf("invalid configuration with stale binary = %+v, want configuration warning precedence", svc)
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
