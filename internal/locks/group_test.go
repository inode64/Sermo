package locks

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAcquireWithGroupSerializesMembersOnly(t *testing.T) {
	l := opLocker(t, fakeProc{alive: map[int]bool{5000: true}, ticks: map[int]uint64{5000: 7777}}, nil)
	release, err := l.AcquireWithGroup("rpcbind", "rpc", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()
	if _, err := l.AcquireWithGroup("statd", "rpc", time.Minute); !errors.As(err, new(*HeldError)) {
		t.Fatalf("related service was not blocked: %v", err)
	}
	unrelated, err := l.AcquireWithGroup("web", "http", time.Minute)
	if err != nil {
		t.Fatalf("unrelated operation blocked: %v", err)
	}
	if err := unrelated(); err != nil {
		t.Fatal(err)
	}
	// A direct operation still sees the member's own lock, and a group name
	// equal to a service name cannot collide across namespaces.
	if _, err := l.Acquire("rpcbind", time.Minute); !errors.As(err, new(*HeldError)) {
		t.Fatalf("member's own lock absent: %v", err)
	}
	independent, err := l.AcquireWithGroup("rpc", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := independent(); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	next, err := l.AcquireWithGroup("statd", "rpc", time.Minute)
	if err != nil {
		t.Fatalf("group leaked after completion: %v", err)
	}
	if err := next(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireWithGroupReleasesGroupWhenMemberIsBusy(t *testing.T) {
	l := opLocker(t, fakeProc{alive: map[int]bool{5000: true}, ticks: map[int]uint64{5000: 7777}}, nil)
	member, err := l.Acquire("statd", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = member.Release() }()
	if _, err := l.AcquireWithGroup("statd", "rpc", time.Minute); !errors.As(err, new(*HeldError)) {
		t.Fatalf("busy member was accepted: %v", err)
	}
	release, err := l.AcquireWithGroup("rpcbind", "rpc", time.Minute)
	if err != nil {
		t.Fatalf("failed acquisition leaked group: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireWithGroupReclaimsExpiredLeaseWithAudit(t *testing.T) {
	var events []string
	l := opLocker(t, fakeProc{}, &events)
	writeLock(t, filepath.Join(l.Dir, operationGroupsDirectory), "rpc.lock", lockFile{Service: "rpc", OwnerPID: 9999, ExpiresAt: fixedNow.Add(-time.Second)})
	release, err := l.AcquireWithGroup("statd", "rpc", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()
	if len(events) != 1 || !strings.Contains(events[0], "operation group rpc") {
		t.Fatalf("group reclaim not audited: %v", events)
	}
}

func TestAcquireWithGroupConcurrentContendersCannotEnter(t *testing.T) {
	l := opLocker(t, fakeProc{alive: map[int]bool{5000: true}, ticks: map[int]uint64{5000: 7777}}, nil)
	release, err := l.AcquireWithGroup("rpcbind", "rpc", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()
	const contenders = 16
	results := make(chan error, contenders)
	var wg sync.WaitGroup
	for range contenders {
		wg.Go(func() {
			unlock, err := l.AcquireWithGroup("statd", "rpc", time.Minute)
			if unlock != nil {
				_ = unlock()
			}
			results <- err
		})
	}
	wg.Wait()
	close(results)
	for err := range results {
		if !errors.As(err, new(*HeldError)) {
			t.Fatalf("contender entered a held group: %v", err)
		}
	}
}
