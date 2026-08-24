package app

import (
	"context"
	"slices"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/servicemgr"
	"sermo/internal/web"
)

func TestCheckHealthSummary(t *testing.T) {
	snap := map[string]CheckSnapshot{
		"http": {Observation: checks.ObservationHealthy, OK: true},
		"tcp":  {Observation: checks.ObservationFailing, OK: false},
		"warn": {Observation: checks.ObservationFailing, OK: false, Optional: true},
		"gate": {Observation: checks.ObservationSkipped, OK: true, Skipped: true},
	}
	failing, health := checkHealthSummaryCurrent(snap, []string{"http", "tcp", "warn", "gate"}, nil, true, nil)
	if failing != 1 || health != "failing" {
		t.Fatalf("got failing=%d health=%q, want 1 failing", failing, health)
	}

	// A failing advisory no longer vanishes: with no real failure beside it the
	// service reads warning, which is the difference between a quiet degradation
	// and a clean bill of health.
	failing, health = checkHealthSummaryCurrent(snap, []string{"http", "warn", "gate"}, nil, true, nil)
	if failing != 0 || health != checkHealthWarning {
		t.Fatalf("without tcp: failing=%d health=%q, want warning", failing, health)
	}

	// A declared severity grades the same way the legacy optional flag does.
	declared := map[string]CheckSnapshot{"disk": {Observation: checks.ObservationFailing, OK: false}}
	failing, health = checkHealthSummaryCurrent(declared, []string{"disk"}, map[string]string{"disk": checks.SeverityWarning}, true, nil)
	if failing != 0 || health != checkHealthWarning {
		t.Fatalf("severity: warning: failing=%d health=%q, want warning", failing, health)
	}
	failing, health = checkHealthSummaryCurrent(declared, []string{"disk"}, nil, true, nil)
	if failing != 1 || health != checkHealthFailing {
		t.Fatalf("undeclared severity: failing=%d health=%q, want failing", failing, health)
	}

	snap = map[string]CheckSnapshot{
		"cert": {Observation: checks.ObservationHealthy, OK: false, Condition: true},
	}
	failing, health = checkHealthSummaryCurrent(snap, []string{"cert"}, nil, true, nil)
	if failing != 0 || health != "ok" {
		t.Fatalf("healthy condition: failing=%d health=%q, want ok", failing, health)
	}
	snap["cert"] = CheckSnapshot{Observation: checks.ObservationFailing, OK: true, Condition: true}
	failing, health = checkHealthSummaryCurrent(snap, []string{"cert"}, nil, true, nil)
	if failing != 1 || health != "failing" {
		t.Fatalf("firing condition: failing=%d health=%q, want failing", failing, health)
	}

	failing, health = checkHealthSummaryCurrent(nil, []string{"http"}, nil, true, nil)
	if failing != 0 || health != "unknown" {
		t.Fatalf("no snapshot: failing=%d health=%q, want unknown", failing, health)
	}

	failing, health = checkHealthSummaryCurrent(nil, []string{"http"}, nil, false, nil)
	if failing != 0 || health != "paused" {
		t.Fatalf("paused: failing=%d health=%q, want paused", failing, health)
	}

	failing, health = checkHealthSummaryCurrent(map[string]CheckSnapshot{}, []string{"http"}, nil, true, nil)
	if failing != 0 || health != "unknown" {
		t.Fatalf("no observed checks: failing=%d health=%q, want unknown", failing, health)
	}
}

func TestWebBackendViewCheckHealth(t *testing.T) {
	snaps := NewSnapshots()
	snaps.Publish("web", map[string]checks.Result{
		"http": {Check: "http", OK: true},
		"tcp":  {Check: "tcp", OK: false},
	}, map[string]bool{"http": true, "tcp": true})

	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {
				checkNames: []string{"http", "tcp"},
				status:     func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
			},
		},
		snapshots: snaps,
	}

	svc := b.view(context.Background(), "web", b.entries["web"])
	if svc.CheckHealth != "failing" || svc.ChecksFailing != 1 || svc.State != TargetStateFailed {
		t.Fatalf("service = %+v, want failing with 1", svc)
	}
}

func TestWebBackendFailedUnitWithHealthyLiveProcessWarns(t *testing.T) {
	at := time.Date(2026, 8, 10, 11, 20, 0, 0, time.UTC)
	snaps := NewSnapshots()
	snaps.now = func() time.Time { return at }
	snaps.Publish("glusterd", map[string]checks.Result{
		"management": {Check: "management", OK: true},
	}, map[string]bool{"management": true})
	metrics := NewServiceMetricSampler()
	metrics.Record("glusterd", web.ServiceRuntime{
		At:        at.UTC().Format(time.RFC3339),
		StartedAt: at.Add(-time.Minute).UTC().Format(time.RFC3339),
		ProcessTotals: web.ProcessTotals{
			Count: 1,
		},
	})
	entry := &webEntry{
		checkNames: []string{"management"},
		interval:   time.Minute,
		status:     func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusFailed, nil },
	}
	b := &WebBackend{
		order:          []string{"glusterd"},
		entries:        map[string]*webEntry{"glusterd": entry},
		snapshots:      snaps,
		serviceMetrics: metrics,
		now:            func() time.Time { return at },
	}

	svc := b.view(context.Background(), "glusterd", entry)
	if svc.Status != string(servicemgr.StatusFailed) || svc.State != TargetStateWarning || svc.CheckHealth != checkHealthWarning || svc.StateReason != stateReasonFailedUnitLiveProcess {
		t.Fatalf("degraded service = %+v, want failed backend with healthy-live-process warning", svc)
	}
}

func TestWebBackendGlusterClusterReadings(t *testing.T) {
	at := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	snaps := NewSnapshots()
	snaps.now = func() time.Time { return at }
	snaps.PublishWithCheckTypes("glusterd", map[string]checks.Result{
		"cluster": {
			Check: "cluster", OK: false,
			Data: map[string]any{
				checks.DataKeyGlusterPeersConnected: 1,
				checks.DataKeyGlusterPeersExpected:  2,
				checks.DataKeyGlusterBricksOnline:   5,
				checks.DataKeyGlusterBricksExpected: 6,
				checks.DataKeyGlusterIssues:         []string{"peer zeus is disconnected"},
			},
		},
	}, map[string]bool{"cluster": true}, map[string]string{"cluster": checks.CheckTypeGlusterCluster})
	entry := &webEntry{
		checkNames:     []string{"cluster"},
		checkTypes:     map[string]string{"cluster": checks.CheckTypeGlusterCluster},
		checkIntervals: map[string]time.Duration{"cluster": time.Minute},
		status:         func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
	}
	b := &WebBackend{
		order:     []string{"glusterd"},
		entries:   map[string]*webEntry{"glusterd": entry},
		snapshots: snaps,
		now:       func() time.Time { return at.Add(time.Second) },
	}

	detail, ok := b.Detail(context.Background(), "glusterd")
	if !ok || len(detail.Checks) != 1 {
		t.Fatalf("detail = %+v", detail)
	}
	readings := detail.Checks[0].Readings
	if readingByField(readings, checks.DataKeyGlusterPeersConnected).Value != "1" ||
		readingByField(readings, checks.DataKeyGlusterBricksOnline).Value != "5" ||
		readingByField(readings, checks.DataKeyGlusterIssues).Value != "peer zeus is disconnected" {
		t.Fatalf("gluster readings = %#v", readings)
	}
}

func TestWebBackendServiceCheckSnapshotRequiresFreshMatchingType(t *testing.T) {
	at := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	snaps := NewSnapshots()
	snaps.now = func() time.Time { return at }
	snaps.PublishWithCheckTypes("web", map[string]checks.Result{
		"probe": {Check: "probe", OK: false, Message: "connection refused"},
	}, map[string]bool{"probe": true}, map[string]string{"probe": checks.CheckTypeTCP})

	entry := &webEntry{
		checkNames:     []string{"probe"},
		checkTypes:     map[string]string{"probe": checks.CheckTypeHTTP},
		checkIntervals: map[string]time.Duration{"probe": time.Minute},
		status:         func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
	}
	b := &WebBackend{
		order:     []string{"web"},
		entries:   map[string]*webEntry{"web": entry},
		snapshots: snaps,
		now:       func() time.Time { return at.Add(time.Minute) },
	}

	svc := b.view(context.Background(), "web", entry)
	if svc.CheckHealth != checkHealthUnknown || svc.ChecksFailing != 0 || svc.State != TargetStateCollecting {
		t.Fatalf("mismatched snapshot service = %+v, want collecting with unknown health", svc)
	}
	detail, ok := b.Detail(context.Background(), "web")
	if !ok || len(detail.Checks) != 1 || !detail.Checks[0].Stale || detail.Checks[0].Ran || len(detail.Checks[0].Readings) != 0 {
		t.Fatalf("mismatched snapshot detail = %+v, want stale check without readings", detail.Checks)
	}

	entry.checkTypes["probe"] = checks.CheckTypeTCP
	b.now = func() time.Time { return at.Add(2*time.Minute + time.Nanosecond) }
	svc = b.view(context.Background(), "web", entry)
	if svc.CheckHealth != checkHealthUnknown || svc.State != TargetStateCollecting {
		t.Fatalf("expired snapshot service = %+v, want collecting with unknown health", svc)
	}
}

func TestWebBackendViewCheckHealthPaused(t *testing.T) {
	at := time.Date(2026, 6, 7, 14, 0, 0, 0, time.UTC)
	store := newFakeStore()
	store.now = func() time.Time { return at }
	if err := store.SetActive("web", false, "cli"); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	snaps := NewSnapshots()
	snaps.Publish("web", map[string]checks.Result{
		"http": {Check: "http", OK: false},
	}, map[string]bool{"http": true})

	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {checkNames: []string{"http"}},
		},
		store:     store,
		snapshots: snaps,
	}

	svc := b.view(context.Background(), "web", b.entries["web"])
	if svc.CheckHealth != "paused" || svc.ChecksFailing != 0 || svc.State != TargetStateStopped {
		t.Fatalf("paused service = %+v, want check_health=paused", svc)
	}
}

func TestWebBackendServiceStateStartupCollectingMonitored(t *testing.T) {
	settling := NewSettling(nil)
	settling.Reset([]string{SettlingServiceKey("web")})
	observability := NewObservabilityRegistry()
	snaps := NewSnapshots()
	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {
				checkNames:        []string{"http"},
				noResidentProcess: true,
				status:            func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
			},
		},
		snapshots:     snaps,
		settling:      settling,
		observability: observability,
	}

	svc := b.view(context.Background(), "web", b.entries["web"])
	if svc.State != TargetStateStarting || svc.ObservabilityReady || len(svc.ObservabilityMissing) == 0 {
		t.Fatalf("starting service = %+v, want starting with missing observability", svc)
	}

	settling.MarkObserved(SettlingServiceKey("web"))
	svc = b.view(context.Background(), "web", b.entries["web"])
	if svc.State != TargetStateCollecting || svc.ObservabilityReady {
		t.Fatalf("collecting service without snapshots = %+v, want collecting", svc)
	}

	snaps.Publish("web", map[string]checks.Result{
		"http": {Check: "http", OK: true},
	}, map[string]bool{"http": true})
	svc = b.view(context.Background(), "web", b.entries["web"])
	if svc.State != TargetStateCollecting || svc.ObservabilityReady {
		t.Fatalf("collecting service without availability history = %+v, want collecting", svc)
	}

	observability.MarkReady("web", time.Now())
	svc = b.view(context.Background(), "web", b.entries["web"])
	if svc.State != TargetStateMonitored || !svc.ObservabilityReady || len(svc.ObservabilityMissing) != 0 {
		t.Fatalf("monitored service = %+v, want monitored with observability ready", svc)
	}
}

// An active service whose process selectors match nothing used to sit in
// "collecting" for as long as it ran, because the runtime indicator never
// arrived and nothing distinguished "late" from "never".
func TestWebBackendServiceStateEmptyProcessTreeWarnsInsteadOfCollectingForever(t *testing.T) {
	now := time.Now()
	settling := NewSettling(nil)
	settling.Reset([]string{SettlingServiceKey("rpcbind")})
	settling.MarkObserved(SettlingServiceKey("rpcbind"))
	observability := NewObservabilityRegistry()
	observability.MarkReady("rpcbind", now)
	snaps := NewSnapshots()
	snaps.Publish("rpcbind", map[string]checks.Result{
		"service": {Check: "service", OK: true},
	}, map[string]bool{"service": true})
	metrics := NewServiceMetricSampler()
	b := &WebBackend{
		order: []string{"rpcbind"},
		entries: map[string]*webEntry{
			"rpcbind": {
				checkNames: []string{"service"},
				interval:   30 * time.Second,
				status:     func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
			},
		},
		snapshots:      snaps,
		settling:       settling,
		observability:  observability,
		serviceMetrics: metrics,
		now:            func() time.Time { return now },
	}

	// No sample published yet: the runtime numbers really are still on the way.
	svc := b.view(context.Background(), "rpcbind", b.entries["rpcbind"])
	if svc.State != TargetStateCollecting || !slices.Contains(svc.ObservabilityMissing, observabilityMissingRuntime) {
		t.Fatalf("service before the first sample = %+v, want collecting on runtime metrics", svc)
	}

	// A completed cycle that attributed no process is a definite answer.
	metrics.Record("rpcbind", web.ServiceRuntime{At: now.UTC().Format(time.RFC3339)})
	svc = b.view(context.Background(), "rpcbind", b.entries["rpcbind"])
	if svc.State != TargetStateWarning {
		t.Fatalf("service with an empty process tree = %+v, want %q", svc, TargetStateWarning)
	}
	if !slices.Contains(svc.ObservabilityMissing, observabilityMissingProcesses) {
		t.Fatalf("missing indicators = %v, want %q", svc.ObservabilityMissing, observabilityMissingProcesses)
	}
	if svc.ObservabilityReady {
		t.Fatal("an empty process tree must not report observability ready")
	}

	// Once processes show up again it goes back to waiting for the derived
	// CPU/IO rates rather than warning.
	metrics.Record("rpcbind", web.ServiceRuntime{
		At:            now.UTC().Format(time.RFC3339),
		ProcessTotals: web.ProcessTotals{Count: 1, HasCPU: true},
	})
	svc = b.view(context.Background(), "rpcbind", b.entries["rpcbind"])
	if svc.State == TargetStateWarning || !slices.Contains(svc.ObservabilityMissing, observabilityMissingRuntime) {
		t.Fatalf("service with a visible process = %+v, want collecting on runtime metrics", svc)
	}
}

// A neutral observation asserts nothing, so it must not drag the service into a
// failing state. An invalid observation fails closed instead of being
// reinterpreted from raw snapshot fields or configuration.
func TestCheckHealthSummaryUsesCanonicalObservation(t *testing.T) {
	snap := map[string]CheckSnapshot{
		"backup": {Observation: checks.ObservationNeutral, OK: false},
		"http":   {Observation: checks.ObservationHealthy, OK: true},
	}

	failing, health := checkHealthSummaryCurrent(snap, []string{"backup", "http"}, nil, true, nil)
	if failing != 0 || health != TargetStateOK {
		t.Fatalf("failing=%d health=%q, want a healthy service: a state sensor is not a verdict", failing, health)
	}

	snap["backup"] = CheckSnapshot{OK: true, Skipped: true}
	failing, health = checkHealthSummaryCurrent(snap, []string{"backup", "http"}, nil, true, nil)
	if failing != 1 || health != checkHealthFailing {
		t.Fatalf("failing=%d health=%q, want invalid observation to fail closed", failing, health)
	}
}
