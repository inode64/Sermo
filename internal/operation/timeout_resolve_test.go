package operation

import (
	"testing"
	"time"
)

// ResolveTimeout falls back to DefaultOperationTimeout for a non-positive
// configured value and otherwise raises the configured value to the stop-policy
// minimum. Pin both branches; mutation testing left them unasserted.
func TestResolveTimeout(t *testing.T) {
	empty := map[string]any{}
	minEmpty := MinimumTimeout(empty)
	if minEmpty >= DefaultOperationTimeout {
		t.Fatalf("test assumes MinimumTimeout(empty) %v < DefaultOperationTimeout %v", minEmpty, DefaultOperationTimeout)
	}

	// configured <= 0 -> the default (which exceeds the empty-policy minimum).
	if got := ResolveTimeout(0, empty); got != DefaultOperationTimeout {
		t.Errorf("ResolveTimeout(0) = %v, want %v", got, DefaultOperationTimeout)
	}
	if got := ResolveTimeout(-time.Second, empty); got != DefaultOperationTimeout {
		t.Errorf("ResolveTimeout(-1s) = %v, want %v", got, DefaultOperationTimeout)
	}

	// A generous configured timeout is kept verbatim.
	big := DefaultOperationTimeout + time.Hour
	if got := ResolveTimeout(big, empty); got != big {
		t.Errorf("ResolveTimeout(big) = %v, want %v", got, big)
	}

	// A tiny configured timeout is raised to the policy minimum.
	if got := ResolveTimeout(1, empty); got != minEmpty {
		t.Errorf("ResolveTimeout(1ns) = %v, want the policy minimum %v", got, minEmpty)
	}
}
