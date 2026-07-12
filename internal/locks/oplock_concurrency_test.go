package locks

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// TestReclaimStaleConcurrentSingleHolder guards the mutual-exclusion invariant
// under contention on a stale lock: many contenders reclaiming the same stale
// lock at once must yield exactly one holder. Before reclaimStale serialized its
// remove, an unconditional os.Remove could delete a peer's freshly created lock,
// letting two contenders both acquire. The reclaim is rare/slow so the flock
// serialization is cheap; this test pins the invariant.
func TestReclaimStaleConcurrentSingleHolder(t *testing.T) {
	dir := t.TempDir()
	// A stale lock left by a dead owner (9999 not alive).
	writeLock(t, dir, "mysql.lock", lockFile{
		Service: "mysql", OwnerPID: 9999, OwnerStartTicks: 1, ExpiresAt: fixedNow.Add(time.Hour),
	})

	const contenders = 8
	alive := map[int]bool{}
	for i := range contenders {
		alive[6000+i] = true // every contender is a live, distinct "process"
	}
	proc := fakeProc{alive: alive}

	var wg sync.WaitGroup
	var mu sync.Mutex
	successes, held := 0, 0
	for i := range contenders {
		pid := 6000 + i
		l := OperationLocker{
			Dir:  dir,
			Proc: proc,
			Now:  func() time.Time { return fixedNow },
			Self: func() (int, uint64) { return pid, uint64(pid) },
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := l.Acquire("mysql", time.Hour)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && h != nil:
				successes++
			case isHeld(err):
				held++
			default:
				t.Errorf("unexpected Acquire result: handle=%v err=%v", h, err)
			}
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("successes = %d, want exactly 1 holder under contention", successes)
	}
	if held != contenders-1 {
		t.Fatalf("held = %d, want %d (all non-winners blocked)", held, contenders-1)
	}
}

func isHeld(err error) bool {
	var he *HeldError
	return errors.As(err, &he)
}
