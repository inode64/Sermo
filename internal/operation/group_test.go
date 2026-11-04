package operation

import (
	"errors"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/locks"
)

type groupProber struct{}

func (groupProber) Alive(pid int) bool { return pid == 5000 || pid == 6000 }
func (groupProber) StartTicks(pid int) (uint64, bool) {
	if pid == 6000 {
		return 8888, true
	}
	return 7777, pid == 5000
}

func TestOperationGroupUsesTheSharedEngineGate(t *testing.T) {
	// Give the two engines one runtime, as daemon, CLI and web do on a host.
	locker := locks.OperationLocker{Dir: t.TempDir(), Proc: groupProber{}, Self: func() (int, uint64) { return 5000, 7777 }}
	manager := &fakeManager{}
	first := New(Config{Service: "rpcbind", Unit: "rpcbind", Tree: map[string]any{config.ServiceKeyOperationGroup: "rpc"}, Locker: &locker})
	other := locker
	other.Self = func() (int, uint64) { return 6000, 8888 }
	second := New(Config{Service: "statd", Unit: "statd", Tree: map[string]any{config.ServiceKeyOperationGroup: "rpc"}, Locker: &other, Manager: manager})
	release, err := first.AcquireLock(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()
	if _, err := second.AcquireLock(time.Minute); !errors.As(err, new(*locks.HeldError)) {
		t.Fatalf("group contention not preserved: %v", err)
	}
	result := second.Do(t.Context(), actionStart)
	if result.Status != ResultBlocked || len(manager.calls) != 0 {
		t.Fatalf("related operation reached backend: %+v, calls=%v", result, manager.calls)
	}
}
