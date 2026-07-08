package app

import "testing"

func TestOperationLockReclaimEvent(t *testing.T) {
	var events []Event
	fn := operationLockReclaimEvent(func(e Event) { events = append(events, e) })
	fn("mysql", "expired")
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Service != "mysql" || events[0].Kind != eventKindAlert {
		t.Fatalf("event = %+v", events[0])
	}
	if events[0].Message != "reclaimed stale operation lock (expired)" {
		t.Fatalf("message = %q", events[0].Message)
	}
	if operationLockReclaimEvent(nil) != nil {
		t.Fatal("nil emit should yield nil callback")
	}
}

func TestConfigureOperationLockerWiresOnReclaim(t *testing.T) {
	var reclaimed []string
	locker := configureOperationLocker(t.TempDir(), func(service, reason string) {
		reclaimed = append(reclaimed, service+":"+reason)
	})
	if locker.OnReclaim == nil {
		t.Fatal("OnReclaim not wired")
	}
	locker.OnReclaim("web", "dead owner")
	if len(reclaimed) != 1 || reclaimed[0] != "web:dead owner" {
		t.Fatalf("reclaimed = %v", reclaimed)
	}
}

// Ensure configureOperationLocker uses the ops subdirectory.
func TestConfigureOperationLockerOpsDir(t *testing.T) {
	root := t.TempDir()
	locker := configureOperationLocker(root, nil)
	want := root + "/ops"
	if locker.Dir != want {
		t.Fatalf("Dir = %q, want %q", locker.Dir, want)
	}
}
