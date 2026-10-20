package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/locks"
	"sermo/internal/mountctl"
	"sermo/internal/severity"
)

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

// Every reclaimed lock, whichever path took it, is the same warning notice; the
// operation lock keeps the wording the event_log severity migration matches.
func TestLockReclaimEvents(t *testing.T) {
	for _, tc := range []struct {
		name           string
		reclaim        LockReclaim
		service, watch string
		message        string
	}{
		{name: "operation", reclaim: OperationLockReclaim("mysql", "expired"), service: "mysql", message: "reclaimed stale operation lock (expired)"},
		{name: "named", reclaim: NamedLockReclaim("mysql", "backup", "dead owner"), service: "mysql", message: "reclaimed stale lock mysql.backup (dead owner)"},
		{name: "bare named", reclaim: NamedLockReclaim("mysql", "", "expired"), service: "mysql", message: "reclaimed stale lock mysql (expired)"},
		{name: "watch control", reclaim: WatchLockReclaim("md0", "pid reuse"), watch: "md0", message: "reclaimed stale operation lock (pid reuse)"},
		{name: "mount", reclaim: MountLockReclaim(mountctl.Spec{Name: "backup", Path: "/mnt/backup"}, "expired"), watch: "backup", message: "reclaimed stale mount operation lock (expired)"},
		{name: "expansion", reclaim: ExpansionLockReclaim("data", "/srv", "expired"), watch: "data", message: "reclaimed stale expansion lock for /srv (expired)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.reclaim.event()
			if e.Service != tc.service || e.Watch != tc.watch || e.Kind != eventKindAlert || e.Severity != severity.Warning || !e.Notice || e.Message != tc.message {
				t.Fatalf("event = %+v", e)
			}
			rec := LockReclaimEventRecord(tc.reclaim)
			if rec.Service != tc.service || rec.Watch != tc.watch || rec.Kind != eventKindAlert || rec.Severity != severity.Warning.String() || rec.Message != tc.message {
				t.Fatalf("record = %+v", rec)
			}
		})
	}
	if operationLockReclaimEvent(nil) != nil || watchLockReclaimEvent(nil, "md0") != nil || mountLockReclaimEvent(nil) != nil {
		t.Fatal("nil emit should yield nil callbacks")
	}
}

// plantExpiredOperationLock leaves an operation lock under runtimeDir whose TTL
// ran out an hour ago, owned by this live process.
func plantExpiredOperationLock(t *testing.T, dir, name string) {
	t.Helper()
	locker := locks.NewOperationLocker(dir)
	locker.Now = func() time.Time { return time.Now().Add(-2 * time.Hour) }
	if _, err := locker.Acquire(name, time.Hour); err != nil {
		t.Fatalf("plant expired lock: %v", err)
	}
}

func TestRunLockedControlReportsReclaimedLock(t *testing.T) {
	runtime := t.TempDir()
	plantExpiredOperationLock(t, locks.RuntimeOpsDir(runtime), raidControlLockPrefix+"md0")
	var reasons []string
	ok, message := runLockedControl(t.Context(), runtime, raidControlLockPrefix+"md0", "RAID array md0", time.Second,
		func(reason string) { reasons = append(reasons, reason) },
		func(context.Context) (bool, string) { return true, "done" })
	if !ok || message != "done" {
		t.Fatalf("ok=%v message=%q", ok, message)
	}
	if len(reasons) != 1 || reasons[0] != "expired" {
		t.Fatalf("reclaims = %v, want one expired", reasons)
	}
}

// A stale expansion lock reclaimed by a watch's expansion is reported against
// that watch, whichever caller (the watch or the web button) expanded.
func TestLockedVolumeExpanderReportsReclaimedLock(t *testing.T) {
	runtime := t.TempDir()
	plantExpiredOperationLock(t, locks.RuntimeOpsDir(runtime), expandLockName("/srv"))
	var events []Event
	e := lockedVolumeExpander{
		inner:      &fakeExpander{},
		runtimeDir: runtime,
		timeout:    time.Second,
		emit:       func(ev Event) { events = append(events, ev) },
	}
	if _, err := e.ExpandPath(withExpandWatch(t.Context(), "data"), "/srv", 1); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Watch != "data" || events[0].Message != "reclaimed stale expansion lock for /srv (expired)" {
		t.Fatalf("events = %+v", events)
	}
}
