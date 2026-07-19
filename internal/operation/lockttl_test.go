package operation

import (
	"testing"
	"time"
)

// A service whose stop_policy graceful wait exceeds the fixed 5-minute default
// must get an operation-lock TTL that outlives the operation, or the lock could
// expire mid-stop and let a second operation run concurrently.
func TestLockTTLCoversLongGracefulStop(t *testing.T) {
	tree := map[string]any{
		"stop_policy": map[string]any{
			"graceful_timeout": "6m",
		},
	}
	engine := New(Config{
		Service: "db",
		Unit:    "db.service",
		Tree:    tree,
	})
	minWanted := 6 * time.Minute // must at least cover the graceful wait
	if engine.LockTTL <= minWanted {
		t.Fatalf("LockTTL = %v, want > the 6m graceful stop", engine.LockTTL)
	}
	if engine.LockTTL <= engine.OperationTimeout {
		t.Fatalf("LockTTL %v must outlive the operation timeout %v", engine.LockTTL, engine.OperationTimeout)
	}
}

// An explicit LockTTL is honored as-is.
func TestLockTTLHonorsExplicitValue(t *testing.T) {
	engine := New(Config{Service: "db", Unit: "db.service", LockTTL: 42 * time.Second})
	if engine.LockTTL != 42*time.Second {
		t.Fatalf("LockTTL = %v, want the explicit 42s", engine.LockTTL)
	}
}
