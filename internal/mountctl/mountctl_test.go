package mountctl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/execx"
	"sermo/internal/process"
)

type fakeRunner struct {
	mounted   *bool
	busy      bool
	calls     []string
	signalled *int
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	r.calls = append(r.calls, strings.Join(append([]string{name}, args...), " "))
	switch name {
	case "mount":
		*r.mounted = true
		return execx.Result{}, nil
	case "umount":
		if len(args) > 0 && args[0] == "-l" {
			*r.mounted = false
			return execx.Result{}, nil
		}
		if r.busy && (r.signalled == nil || *r.signalled == 0) {
			return execx.Result{ExitCode: 32}, fmt.Errorf("run umount: exit code 32")
		}
		*r.mounted = false
		return execx.Result{}, nil
	default:
		return execx.Result{}, fmt.Errorf("unexpected command %s", name)
	}
}

type fakeSignaler struct{ calls int }

func (s *fakeSignaler) Signal(int, syscall.Signal) error {
	s.calls++
	return nil
}

func testController(t *testing.T, mounted *bool, runner *fakeRunner) Controller {
	t.Helper()
	return Controller{
		Runtime: t.TempDir(),
		Runner:  runner,
		Mounts: func() ([]checks.Mount, error) {
			if *mounted {
				return []checks.Mount{{MountPoint: "/mnt/backup", FSType: "nfs4"}}, nil
			}
			return nil, nil
		},
		InFstab: func(path string) (bool, error) { return path == "/mnt/backup", nil },
		Sleep:   func(time.Duration) {},
	}
}

func TestUsersWithLookupStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the scan begins

	procs, err := UsersWithLookupContext(ctx, "/nonexistent-mount", nil)
	if err == nil {
		t.Fatal("usersWithLookup returned nil error for a cancelled context; want context.Canceled")
	}
	if len(procs) != 0 {
		t.Fatalf("usersWithLookup returned %d processes; want none for a cancelled scan", len(procs))
	}
}

func TestPidUsesPathHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// The current process always uses /; a cancelled context must abort before readlink.
	if pidUsesPath(ctx, os.Getpid(), "/") {
		t.Fatal("pidUsesPath must stop immediately when the context is cancelled")
	}
}

func TestAcquireRefcountMountsOnlyOnFirstUse(t *testing.T) {
	mounted := false
	runner := &fakeRunner{mounted: &mounted}
	c := testController(t, &mounted, runner)
	spec := EphemeralSpec("/mnt/backup")

	for i := 0; i < 2; i++ {
		if _, err := c.Acquire(context.Background(), spec); err != nil {
			t.Fatalf("Acquire #%d: %v", i+1, err)
		}
	}

	status, err := c.ReadStatus(spec)
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	if status.Refcount != 2 || !status.Mounted {
		t.Fatalf("status = %+v, want mounted refcount=2", status)
	}
	if got := strings.Join(runner.calls, "|"); got != "mount /mnt/backup" {
		t.Fatalf("commands = %q, want one mount", got)
	}
}

func TestFstabEntriesParsesEscapedMountpoints(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fstab")
	body := "# comment\n" +
		"UUID=backup /mnt/backup ext4 defaults,noauto 0 2\n" +
		"/dev/sdb1 /srv/My\\040Data xfs nofail 0 2\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := FstabEntries(path)
	if err != nil {
		t.Fatalf("FstabEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want two", entries)
	}
	if entries[0].Path != "/mnt/backup" || entries[0].FSType != "ext4" || entries[0].Options != "defaults,noauto" {
		t.Fatalf("first entry = %+v", entries[0])
	}
	if entries[1].Path != "/srv/My Data" || entries[1].Source != "/dev/sdb1" {
		t.Fatalf("escaped entry = %+v", entries[1])
	}
}

func TestReleaseUnmountsOnlyWhenRefcountReachesZero(t *testing.T) {
	mounted := false
	runner := &fakeRunner{mounted: &mounted}
	c := testController(t, &mounted, runner)
	spec := EphemeralSpec("/mnt/backup")

	if _, err := c.Acquire(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Acquire(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if res, err := c.Release(context.Background(), spec); err != nil || res.Refcount != 1 || !mounted {
		t.Fatalf("first Release = %+v/%v mounted=%t, want refcount=1 mounted", res, err, mounted)
	}
	if res, err := c.Release(context.Background(), spec); err != nil || res.Refcount != 0 || mounted {
		t.Fatalf("second Release = %+v/%v mounted=%t, want refcount=0 unmounted", res, err, mounted)
	}
	if got := strings.Join(runner.calls, "|"); got != "mount /mnt/backup|umount /mnt/backup" {
		t.Fatalf("commands = %q", got)
	}
}

func TestAcquireRequiresFstabWhenMounting(t *testing.T) {
	mounted := false
	runner := &fakeRunner{mounted: &mounted}
	c := testController(t, &mounted, runner)
	c.InFstab = func(string) (bool, error) { return false, nil }

	if _, err := c.Acquire(context.Background(), EphemeralSpec("/mnt/backup")); err == nil {
		t.Fatal("Acquire without fstab entry succeeded")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("commands = %v, want none", runner.calls)
	}
}

func TestStateIDScrubsConfiguredName(t *testing.T) {
	got := stateID(Spec{Name: "../escape"})
	if got == ".." || strings.Contains(got, "/") || strings.Contains(got, `\`) {
		t.Fatalf("stateID = %q, want simple safe filename", got)
	}
}

func TestReleaseBusyWithoutLazyReportsBlockers(t *testing.T) {
	mounted := true
	runner := &fakeRunner{mounted: &mounted, busy: true}
	c := testController(t, &mounted, runner)
	c.DiscoverUsers = func(string) ([]process.Process, error) {
		return []process.Process{{PID: 123, Exe: "/usr/bin/rsync", ExeOK: true, UID: 1000}}, nil
	}

	res, err := c.Release(context.Background(), EphemeralSpec("/mnt/backup"))
	if err == nil {
		t.Fatal("Release busy mount succeeded")
	}
	if len(res.Blockers) != 1 || !mounted {
		t.Fatalf("Release = %+v mounted=%t, want blocker and mounted", res, mounted)
	}
}

func TestReleaseAllowsLazyOnlyWhenExplicit(t *testing.T) {
	mounted := true
	runner := &fakeRunner{mounted: &mounted, busy: true}
	c := testController(t, &mounted, runner)
	spec := EphemeralSpec("/mnt/backup")
	spec.Umount.AllowLazy = true

	res, err := c.Release(context.Background(), spec)
	if err != nil {
		t.Fatalf("Release lazy: %v", err)
	}
	if !res.Lazy || mounted {
		t.Fatalf("Release = %+v mounted=%t, want lazy unmounted", res, mounted)
	}
	if got := strings.Join(runner.calls, "|"); got != "umount /mnt/backup|umount -l /mnt/backup" {
		t.Fatalf("commands = %q", got)
	}
}

func TestReleaseSignalsOnlyWithKillPolicy(t *testing.T) {
	mounted := true
	signalled := 0
	runner := &fakeRunner{mounted: &mounted, busy: true, signalled: &signalled}
	c := testController(t, &mounted, runner)
	sig := &fakeSignaler{}
	c.Signaler = sig
	c.ResolveUser = func(name string) (uint32, bool) {
		if name == "backup" {
			return 1000, true
		}
		return 0, false
	}
	c.DiscoverUsers = func(string) ([]process.Process, error) {
		if sig.calls > 0 {
			signalled = sig.calls
			return nil, nil
		}
		return []process.Process{{PID: 123, Exe: "/usr/bin/rsync", ExeOK: true, UID: 1000}}, nil
	}
	spec := EphemeralSpec("/mnt/backup")
	spec.Umount.AllowSIGKILL = true
	spec.KillOnlyIf = process.KillSelector{Users: []string{"backup"}, ExeAny: []string{"/usr/bin/rsync"}}

	res, err := c.Release(context.Background(), spec)
	if err != nil {
		t.Fatalf("Release with signalling: %v", err)
	}
	if sig.calls == 0 || mounted {
		t.Fatalf("signals=%d mounted=%t res=%+v, want signalled and unmounted", sig.calls, mounted, res)
	}
}
