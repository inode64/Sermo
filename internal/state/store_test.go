package state

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreActiveDefaultsToNotFound(t *testing.T) {
	s := openTemp(t)

	active, found, err := s.Active("web")
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if found {
		t.Errorf("a service with no recorded state must report found=false (got active=%v)", active)
	}
}

func TestStoreMonitorStateRoundTrip(t *testing.T) {
	s := openTemp(t)
	s.now = func() time.Time { return time.Date(2026, 6, 7, 9, 0, 0, 0, time.UTC) }

	if err := s.SetActive("web", false, SourceWeb); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	rec, found, err := s.MonitorState("web")
	if err != nil || !found {
		t.Fatalf("MonitorState: found=%v err=%v", found, err)
	}
	if rec.Active || rec.Source != SourceWeb || !rec.UpdatedAt.Equal(s.now()) {
		t.Fatalf("record = %+v", rec)
	}
}

func TestStoreSetActiveRoundTrip(t *testing.T) {
	s := openTemp(t)

	if err := s.SetActive("web", false, SourceCLI); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	active, found, err := s.Active("web")
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if !found || active {
		t.Errorf("want found=true active=false, got found=%v active=%v", found, active)
	}

	// Upsert flips the state without duplicating the row.
	if err := s.SetActive("web", true, SourceConfig); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if active, found, _ = s.Active("web"); !found || !active {
		t.Errorf("want found=true active=true after re-set, got found=%v active=%v", found, active)
	}
}

// State must survive a daemon restart/reboot — this is what `monitor: previous`
// relies on. Reopening the same file must preserve the recorded state.
func TestStorePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), Filename)

	first, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := first.SetActive("db", false, SourceCLI); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	active, found, err := second.Active("db")
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if !found || active {
		t.Errorf("state did not persist across reopen: found=%v active=%v", found, active)
	}
}

func TestPruneUnconfiguredServices(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)

	for _, service := range []string{"web", "ghost"} {
		if err := s.SetActive(service, false, SourceCLI); err != nil {
			t.Fatalf("SetActive(%s): %v", service, err)
		}
		if err := s.RecordSLA(service, true, now); err != nil {
			t.Fatalf("RecordSLA(%s): %v", service, err)
		}
		if err := s.RecordCheckSLA(service, "http", true, now); err != nil {
			t.Fatalf("RecordCheckSLA(%s): %v", service, err)
		}
		if err := s.RecordMeasurement(service, "http", 10, now); err != nil {
			t.Fatalf("RecordMeasurement(%s): %v", service, err)
		}
		if err := s.RecordMetric(service, "http", "latency", 10, now); err != nil {
			t.Fatalf("RecordMetric(%s): %v", service, err)
		}
		if err := s.RecordServiceMetric(service, "cpu", 10, now); err != nil {
			t.Fatalf("RecordServiceMetric(%s): %v", service, err)
		}
	}

	result, err := s.PruneUnconfiguredServices([]string{"web"})
	if err != nil {
		t.Fatalf("PruneUnconfiguredServices: %v", err)
	}
	if result.Rows != 6 || len(result.Services) != 1 || result.Services[0] != "ghost" {
		t.Fatalf("result = %+v, want ghost with 6 rows", result)
	}

	if _, found, err := s.Active("ghost"); err != nil || found {
		t.Fatalf("ghost active: found=%v err=%v, want removed", found, err)
	}
	if _, found, err := s.Active("web"); err != nil || !found {
		t.Fatalf("web active: found=%v err=%v, want kept", found, err)
	}
	if _, total, err := s.SLA("ghost", time.Hour, now.Add(time.Minute)); err != nil || total != 0 {
		t.Fatalf("ghost SLA total=%d err=%v, want 0", total, err)
	}
	if _, total, err := s.SLA("web", time.Hour, now.Add(time.Minute)); err != nil || total != 1 {
		t.Fatalf("web SLA total=%d err=%v, want 1", total, err)
	}
	if _, total, err := s.CheckSLA("ghost", "http", time.Hour, now.Add(time.Minute)); err != nil || total != 0 {
		t.Fatalf("ghost check SLA total=%d err=%v, want 0", total, err)
	}
	if _, total, err := s.CheckSLA("web", "http", time.Hour, now.Add(time.Minute)); err != nil || total != 1 {
		t.Fatalf("web check SLA total=%d err=%v, want 1", total, err)
	}
	if stat, err := s.MeasurementSummary("ghost", "http", time.Hour, now.Add(time.Minute)); err != nil || stat.Count != 0 {
		t.Fatalf("ghost measurement = %+v err=%v, want empty", stat, err)
	}
	if stat, err := s.MetricSummary("ghost", "http", "latency", time.Hour, now.Add(time.Minute)); err != nil || stat.Count != 0 {
		t.Fatalf("ghost metric = %+v err=%v, want empty", stat, err)
	}
	if stat, err := s.ServiceMetricSummary("ghost", "cpu", time.Hour, now.Add(time.Minute)); err != nil || stat.Count != 0 {
		t.Fatalf("ghost service metric = %+v err=%v, want empty", stat, err)
	}
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), Filename))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
