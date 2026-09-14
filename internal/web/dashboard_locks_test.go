package web

import (
	"context"
	"testing"
	"time"
)

type combinedLockBackend struct {
	fakeBackend
	reads int
}

func (b *combinedLockBackend) ServicesAndLocks(context.Context) ([]Service, []Lock) {
	b.reads++
	return []Service{{Name: "shared"}}, []Lock{{Name: "backup"}}
}

func TestDashboardUsesCombinedServiceLockRead(t *testing.T) {
	backend := &combinedLockBackend{}
	snapshot := CollectDashboardSnapshot(context.Background(), backend, time.Hour)
	if backend.reads != 1 || len(snapshot.Services) != 1 || snapshot.Services[0].Name != "shared" || len(snapshot.Locks) != 1 {
		t.Fatalf("combined reads=%d snapshot=%+v", backend.reads, snapshot)
	}
}
