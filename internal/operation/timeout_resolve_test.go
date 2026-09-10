package operation

import (
	"testing"
	"time"

	"sermo/internal/process"
)

// resolveTimeout falls back to DefaultOperationTimeout for a non-positive
// configured value and otherwise raises the configured value to the stop-policy
// minimum. Pin both branches; mutation testing left them unasserted.
func TestResolvedTimeout(t *testing.T) {
	policy := process.KillPolicy{}
	minEmpty := minimumTimeout(policy)
	if minEmpty >= DefaultOperationTimeout {
		t.Fatalf("test assumes minimumTimeout(empty) %v < DefaultOperationTimeout %v", minEmpty, DefaultOperationTimeout)
	}

	// configured <= 0 -> the default (which exceeds the empty-policy minimum).
	if got := resolveTimeout(0, policy); got != DefaultOperationTimeout {
		t.Errorf("resolveTimeout(0) = %v, want %v", got, DefaultOperationTimeout)
	}
	if got := resolveTimeout(-time.Second, policy); got != DefaultOperationTimeout {
		t.Errorf("resolveTimeout(-1s) = %v, want %v", got, DefaultOperationTimeout)
	}

	// A generous configured timeout is kept verbatim.
	big := DefaultOperationTimeout + time.Hour
	if got := resolveTimeout(big, policy); got != big {
		t.Errorf("resolveTimeout(big) = %v, want %v", got, big)
	}

	// A tiny configured timeout is raised to the policy minimum.
	if got := resolveTimeout(1, policy); got != minEmpty {
		t.Errorf("resolveTimeout(1ns) = %v, want the policy minimum %v", got, minEmpty)
	}
}
