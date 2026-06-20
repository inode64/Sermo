package app

import (
	"testing"
	"time"
)

// TestReloadPreservesWatchLastNotifyAt pins that a reload keeps a firing watch's
// lastNotifyAt. Before the fix the snapshot restored firing but reset
// lastNotifyAt to the zero time, so a watch with then.notify_interval re-sent its
// reminder on the first cycle after every reload (now - zero >= interval).
func TestReloadPreservesWatchLastNotifyAt(t *testing.T) {
	when := time.Now().Add(-30 * time.Minute)
	old := &Watch{Name: "storage", firing: true, lastNotifyAt: when}

	saved := captureWatchState([]*Watch{old})

	fresh := &Watch{Name: "storage"}
	applyWatchState([]*Watch{fresh}, saved)

	if !fresh.firing {
		t.Fatal("firing should be preserved across reload")
	}
	if !fresh.lastNotifyAt.Equal(when) {
		t.Fatalf("lastNotifyAt = %v, want %v (preserved across reload)", fresh.lastNotifyAt, when)
	}
}
