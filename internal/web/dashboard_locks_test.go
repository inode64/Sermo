package web

import (
	"context"
	"testing"
	"time"
)

type combinedLockBackend struct {
	monitoringReadBackend
	reads int
}

func (b *combinedLockBackend) ServicesAndLocks(context.Context) ([]Service, []Lock) {
	b.reads++
	return []Service{{Name: "shared", Enabled: true, Monitored: true}}, []Lock{{Name: "backup"}}
}

func TestDashboardUsesCombinedServiceLockRead(t *testing.T) {
	backend := &combinedLockBackend{}
	snapshot := CollectDashboardSnapshot(context.Background(), backend, time.Hour)
	if backend.reads != 1 || len(snapshot.Services) != 1 || snapshot.Services[0].Name != "shared" || len(snapshot.Locks) != 1 {
		t.Fatalf("combined reads=%d snapshot=%+v", backend.reads, snapshot)
	}
}

type monitoringReadBackend struct {
	fakeBackend
	monitoringReads int
}

func (b *monitoringReadBackend) MonitoringStatus(context.Context) MonitoringStatus {
	b.monitoringReads++
	return MonitoringStatus{Total: 99}
}

func TestDashboardMonitoringUsesCollectedServices(t *testing.T) {
	plain := &monitoringReadBackend{services: []Service{
		{Name: "active", Enabled: true, Monitored: true},
		{Name: "paused", Enabled: true},
		{Name: "disabled"},
	}}
	combined := &combinedLockBackend{}
	for _, tc := range []struct {
		name    string
		backend Backend
		reads   *int
		want    MonitoringStatus
	}{
		{"separate", plain, &plain.monitoringReads, MonitoringStatus{Total: 2, Monitored: 1, Paused: 1}},
		{"combined", combined, &combined.monitoringReads, MonitoringStatus{Total: 1, Monitored: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := CollectDashboardSnapshot(t.Context(), tc.backend, time.Hour)
			if snapshot.Monitoring != tc.want || *tc.reads != 0 {
				t.Fatalf("monitoring = %+v, want %+v; redundant reads = %d", snapshot.Monitoring, tc.want, *tc.reads)
			}
		})
	}
}
