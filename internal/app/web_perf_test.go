package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
	"sermo/internal/web"
)

func TestServiceMetricSamplerLatestWithAt(t *testing.T) {
	s := NewServiceMetricSampler()
	t0 := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	s.Record("web", web.ServiceRuntime{
		At:            t0.UTC().Format(time.RFC3339),
		ProcessTotals: web.ProcessTotals{Count: 2, RSS: 4096},
	})
	if _, _, ok := s.LatestWithAt("missing"); ok {
		t.Fatal("LatestWithAt on missing service should be false")
	}
	cur, at, ok := s.LatestWithAt("web")
	if !ok || at != t0 || cur.Count != 2 || cur.RSS != 4096 {
		t.Fatalf("LatestWithAt = %+v at=%v ok=%v", cur, at, ok)
	}
}

func TestWebBackendListRuntimeUsesPublishedSample(t *testing.T) {
	discoverCalls := 0
	statusCalls := 0
	t0 := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	now := t0.Add(5 * time.Second)
	metrics := NewServiceMetricSampler()
	metrics.Record("web", web.ServiceRuntime{
		At:            t0.UTC().Format(time.RFC3339),
		StartedAt:     t0.Add(-time.Hour).UTC().Format(time.RFC3339),
		Uptime:        "1h",
		UptimeSeconds: 3600,
		ProcessTotals: web.ProcessTotals{Count: 3, RSS: 8192, CPU: 12.5, HasCPU: true},
	})
	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {
				interval: 30 * time.Second,
				status: func(context.Context) (servicemgr.Status, error) {
					statusCalls++
					return servicemgr.StatusActive, nil
				},
				discoverer: process.Discoverer{Reader: countingProcReader{calls: &discoverCalls}},
				selectors: []process.Selector{{
					Name: "main", Type: process.SelectorCommandMatch, Exe: "/usr/sbin/nope",
				}},
			},
		},
		serviceMetrics: metrics,
		now:            func() time.Time { return now },
	}
	svc := b.viewWithRuntime(context.Background(), "web", b.entries["web"], nil, nil, true)
	if discoverCalls != 0 {
		t.Fatalf("discover called %d times, want 0", discoverCalls)
	}
	if statusCalls != 1 {
		t.Fatalf("status called %d times, want 1", statusCalls)
	}
	if svc.RSS != 8192 || !svc.CPUReady || svc.CPU != 12.5 {
		t.Fatalf("runtime fields = %+v", svc)
	}
	if svc.Uptime != "1h" || svc.UptimeSeconds != 3600 {
		t.Fatalf("uptime = %q (%d), want 1h (3600)", svc.Uptime, svc.UptimeSeconds)
	}

	// Second view within status TTL should not re-query backend status.
	_ = b.viewWithRuntime(context.Background(), "web", b.entries["web"], nil, nil, true)
	if statusCalls != 1 {
		t.Fatalf("status called %d times after cache, want 1", statusCalls)
	}
}

func TestWebBackendListRuntimeHiddenWhenServiceStopped(t *testing.T) {
	t0 := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	now := t0.Add(5 * time.Second)
	metrics := NewServiceMetricSampler()
	metrics.Record("lldpd", web.ServiceRuntime{
		At:            t0.UTC().Format(time.RFC3339),
		StartedAt:     t0.Add(-time.Hour).UTC().Format(time.RFC3339),
		Uptime:        "1h",
		UptimeSeconds: 3600,
		ProcessTotals: web.ProcessTotals{Count: 2, RSS: 8192, CPU: 12.5, HasCPU: true},
	})
	b := &WebBackend{
		order: []string{"lldpd"},
		entries: map[string]*webEntry{
			"lldpd": {
				interval: 30 * time.Second,
				status: func(context.Context) (servicemgr.Status, error) {
					return servicemgr.StatusInactive, nil
				},
			},
		},
		serviceMetrics: metrics,
		now:            func() time.Time { return now },
	}

	svc := b.viewWithRuntime(context.Background(), "lldpd", b.entries["lldpd"], nil, nil, true)
	if svc.State != TargetStateFailed || svc.Status != "inactive" {
		t.Fatalf("state = %q status=%q, want failed/inactive", svc.State, svc.Status)
	}
	if svc.RSS != 0 || svc.CPUReady || svc.CPU != 0 {
		t.Fatalf("stopped service should not expose runtime fields: %+v", svc)
	}
}

func TestWebBackendBackendStatusCacheTTL(t *testing.T) {
	if serviceStatusCacheTTL < time.Minute {
		t.Fatalf("serviceStatusCacheTTL = %s, want at least 1m to cover normal web refreshes", serviceStatusCacheTTL)
	}

	var calls atomic.Int32
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	e := &webEntry{
		status: func(context.Context) (servicemgr.Status, error) {
			calls.Add(1)
			return servicemgr.StatusActive, nil
		},
	}
	if got := e.backendStatus(context.Background(), now); got != "active" || calls.Load() != 1 {
		t.Fatalf("first status = %q calls=%d", got, calls.Load())
	}
	if got := e.backendStatus(context.Background(), now.Add(5*time.Second)); got != "active" || calls.Load() != 1 {
		t.Fatalf("cached status = %q calls=%d", got, calls.Load())
	}
	if status, observedAt := e.backendStatusSnapshot(context.Background(), now.Add(5*time.Second)); status != "active" || !observedAt.Equal(now) {
		t.Fatalf("cached status snapshot = %q at %v, want active at %v", status, observedAt, now)
	}
	refreshedAt := now.Add(serviceStatusCacheTTL)
	if got := e.backendStatus(context.Background(), refreshedAt); got != "active" || calls.Load() != 2 {
		t.Fatalf("expired status = %q calls=%d", got, calls.Load())
	}
	if _, observedAt := e.backendStatusSnapshot(context.Background(), refreshedAt); !observedAt.Equal(refreshedAt) {
		t.Fatalf("refreshed status observed at %v, want %v", observedAt, refreshedAt)
	}
	e.invalidateStatusCache()
	if got := e.backendStatus(context.Background(), now.Add(serviceStatusCacheTTL)); got != "active" || calls.Load() != 3 {
		t.Fatalf("after invalidate = %q calls=%d", got, calls.Load())
	}
}

func TestEventLogLastServiceAndWatchIndexes(t *testing.T) {
	l := NewEventLog(10)
	t0 := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return t0 }
	l.Add(Event{Service: "web", Kind: eventKindAction, Action: string(rules.ActionStart)})
	l.now = func() time.Time { return t0.Add(time.Minute) }
	l.Add(Event{Watch: "storage-root", Kind: eventKindNotify})
	l.now = func() time.Time { return t0.Add(2 * time.Minute) }
	l.Add(Event{Service: "web", Kind: eventKindAction, Action: string(rules.ActionRestart)})
	l.now = func() time.Time { return t0.Add(3 * time.Minute) }
	l.Add(Event{Watch: "storage-root", Kind: eventKindHookFail})

	ev, ok := l.LastService("web")
	if !ok || ev.Action != string(rules.ActionRestart) {
		t.Fatalf("LastService(web) = %+v ok=%v", ev, ok)
	}
	if _, ok := l.LastService("db"); ok {
		t.Fatal("LastService(db) should be missing")
	}
	watch, ok := l.LastWatchActivity("storage-root")
	if !ok || watch.Kind != eventKindHookFail {
		t.Fatalf("LastWatchActivity(storage-root) = %+v ok=%v", watch, ok)
	}
}

func TestWebBackendSLATimelineCache(t *testing.T) {
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	calls := 0
	b := &WebBackend{
		sla:      perfSLAReader{calls: &calls},
		slaCache: map[slaCacheKey]cachedSLATimelines{},
	}
	first := b.serviceSLAWindows("web", now)
	second := b.serviceSLAWindows("web", now.Add(10*time.Second))
	if calls != 1 {
		t.Fatalf("SLATimelines called %d times, want 1", calls)
	}
	if len(first) != len(second) || first[0].Window != second[0].Window {
		t.Fatalf("cached windows differ: %+v vs %+v", first, second)
	}
	wantObservedAt := now.Format(time.RFC3339)
	if first[0].ObservedAt != wantObservedAt || second[0].ObservedAt != wantObservedAt {
		t.Fatalf("cached observed_at = %q then %q, want %q", first[0].ObservedAt, second[0].ObservedAt, wantObservedAt)
	}
	refreshed := b.serviceSLAWindows("web", now.Add(slaTimelineCacheTTL))
	if calls != 2 {
		t.Fatalf("after TTL SLATimelines called %d times, want 2", calls)
	}
	if refreshed[0].ObservedAt != now.Add(slaTimelineCacheTTL).Format(time.RFC3339) {
		t.Fatalf("refreshed observed_at = %q", refreshed[0].ObservedAt)
	}
}

type countingProcReader struct {
	calls *int
}

func (r countingProcReader) PIDs() ([]int, error) {
	(*r.calls)++
	return nil, nil
}

func (r countingProcReader) Identity(int) (process.Identity, bool) {
	return process.Identity{}, false
}

type perfSLAReader struct {
	calls *int
}

func (f perfSLAReader) SLAReport(string, time.Time) ([]state.SLAValue, error) { return nil, nil }
func (f perfSLAReader) SLASeries(string, time.Time, time.Time) ([]state.SLAPoint, error) {
	return nil, nil
}
func (f perfSLAReader) CheckSLAReport(string, string, time.Time) ([]state.SLAValue, error) {
	return nil, nil
}
func (f perfSLAReader) CheckSLASeries(string, string, time.Time, time.Time) ([]state.SLAPoint, error) {
	return nil, nil
}
func (f perfSLAReader) SLATimelines(string, time.Time) ([]state.SLAWindowTimeline, error) {
	*f.calls++
	return []state.SLAWindowTimeline{{Window: "hour", Up: 1, Total: 1}}, nil
}
func (f perfSLAReader) CheckSLATimelines(string, string, time.Time) ([]state.SLAWindowTimeline, error) {
	*f.calls++
	return nil, nil
}
