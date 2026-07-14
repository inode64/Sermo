package mountctl

import (
	"context"
	"errors"
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
	force     bool
	calls     []string
	signalled *int
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	r.calls = append(r.calls, strings.Join(append([]string{name}, args...), " "))
	switch name {
	case ActionMount:
		*r.mounted = true
		return execx.Result{}, nil
	case ActionUmount:
		if len(args) > 0 && args[0] == "-l" {
			*r.mounted = false
			return execx.Result{}, nil
		}
		if len(args) > 0 && args[0] == "-f" {
			if r.force {
				*r.mounted = false
				return execx.Result{}, nil
			}
			return execx.Result{ExitCode: 32}, errors.New("run umount: exit code 32")
		}
		if r.busy && (r.signalled == nil || *r.signalled == 0) {
			return execx.Result{ExitCode: 32}, errors.New("run umount: exit code 32")
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

	procs, err := usersWithLookup(ctx, "/nonexistent-mount", nil)
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

	for i := range 2 {
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

func TestReleaseRefusesRootMount(t *testing.T) {
	mounted := true
	runner := &fakeRunner{mounted: &mounted}
	c := testController(t, &mounted, runner)
	discovered := 0
	c.DiscoverUsers = func(string) ([]process.Process, error) {
		discovered++
		return []process.Process{{PID: 123}}, nil
	}
	spec := EphemeralSpec("/")

	res, err := c.Release(context.Background(), spec)
	if err == nil {
		t.Fatal("Release / succeeded")
	}
	if res.Status != ResultFailed || res.Action != ActionUmount || !res.Mounted {
		t.Fatalf("Release / = %+v, want failed mounted umount result", res)
	}
	if !strings.Contains(res.Message, "root filesystem cannot be unmounted") {
		t.Fatalf("Release / message = %q", res.Message)
	}
	if len(runner.calls) != 0 || discovered != 0 {
		t.Fatalf("commands=%v discovered=%d, want no umount, blockers or signals", runner.calls, discovered)
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

func TestBlockersScansOnlyWhenMounted(t *testing.T) {
	mounted := false
	runner := &fakeRunner{mounted: &mounted}
	c := testController(t, &mounted, runner)
	scans := 0
	c.DiscoverUsers = func(string) ([]process.Process, error) {
		scans++
		return []process.Process{{PID: 123, User: "backup"}}, nil
	}
	spec := EphemeralSpec("/mnt/backup")

	blockers, err := c.Blockers(context.Background(), spec)
	if err != nil {
		t.Fatalf("Blockers unmounted: %v", err)
	}
	if len(blockers) != 0 || scans != 0 {
		t.Fatalf("unmounted blockers=%+v scans=%d, want no scan", blockers, scans)
	}

	mounted = true
	blockers, err = c.Blockers(context.Background(), spec)
	if err != nil {
		t.Fatalf("Blockers mounted: %v", err)
	}
	if len(blockers) != 1 || blockers[0].PID != 123 || scans != 1 {
		t.Fatalf("mounted blockers=%+v scans=%d", blockers, scans)
	}
}

func TestReleaseBusySurfacesCanceledDiscoveryError(t *testing.T) {
	mounted := true
	runner := &fakeRunner{mounted: &mounted, busy: true}
	c := testController(t, &mounted, runner)
	c.DiscoverUsers = func(string) ([]process.Process, error) {
		return nil, context.Canceled
	}
	spec := EphemeralSpec("/mnt/backup")

	res, err := c.Release(context.Background(), spec)
	if err == nil {
		t.Fatal("Release busy mount succeeded")
	}
	if !strings.Contains(res.Message, "could not enumerate blockers: cancelled") {
		t.Fatalf("Release message = %q, want cancelled discovery error", res.Message)
	}
}

func TestReleaseBusySurfacesDiscoveryError(t *testing.T) {
	mounted := true
	runner := &fakeRunner{mounted: &mounted, busy: true}
	c := testController(t, &mounted, runner)
	c.DiscoverUsers = func(string) ([]process.Process, error) {
		return nil, errors.New("read /proc: permission denied")
	}
	spec := EphemeralSpec("/mnt/backup")

	res, err := c.Release(context.Background(), spec)
	if err == nil {
		t.Fatal("Release busy mount succeeded")
	}
	if !strings.Contains(res.Message, "could not enumerate blockers") {
		t.Fatalf("Release message = %q, want it to surface the discovery error", res.Message)
	}
}

func TestReleaseAllowsLazyOnlyWhenExplicit(t *testing.T) {
	mounted := true
	runner := &fakeRunner{mounted: &mounted, busy: true}
	c := testController(t, &mounted, runner)
	spec := EphemeralSpec("/mnt/backup")

	res, err := c.ReleaseWithOptions(context.Background(), spec, ReleaseOptions{AllowLazy: true})
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

func TestReleaseAllowsForceOnlyWhenRequested(t *testing.T) {
	mounted := true
	runner := &fakeRunner{mounted: &mounted, busy: true, force: true}
	c := testController(t, &mounted, runner)
	spec := EphemeralSpec("/mnt/backup")

	res, err := c.ReleaseWithOptions(context.Background(), spec, ReleaseOptions{AllowForce: true})
	if err != nil {
		t.Fatalf("Release force: %v", err)
	}
	if !res.Forced || mounted {
		t.Fatalf("Release = %+v mounted=%t, want forced unmounted", res, mounted)
	}
	if got := strings.Join(runner.calls, "|"); got != "umount /mnt/backup|umount -f /mnt/backup" {
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
	spec.KillOnlyIf = process.KillSelector{Users: []string{"backup"}, ExeAny: []string{"/usr/bin/rsync"}}

	res, err := c.ReleaseWithOptions(context.Background(), spec, ReleaseOptions{KillBlockers: true})
	if err != nil {
		t.Fatalf("Release with signalling: %v", err)
	}
	if sig.calls == 0 || mounted {
		t.Fatalf("signals=%d mounted=%t res=%+v, want signalled and unmounted", sig.calls, mounted, res)
	}
}

func TestReleaseKillBlockersRequiresSelector(t *testing.T) {
	mounted := true
	runner := &fakeRunner{mounted: &mounted, busy: true}
	c := testController(t, &mounted, runner)
	c.DiscoverUsers = func(string) ([]process.Process, error) {
		return []process.Process{{PID: 123, Exe: "/usr/bin/rsync", ExeOK: true, UID: 1000}}, nil
	}

	res, err := c.ReleaseWithOptions(context.Background(), EphemeralSpec("/mnt/backup"), ReleaseOptions{KillBlockers: true})
	if err == nil {
		t.Fatal("Release with kill blockers and no selector succeeded")
	}
	if res.Message != mountMessageKillSelectorRequired || len(res.Blockers) != 1 {
		t.Fatalf("Release = %+v, want selector error with blockers", res)
	}
}

type slowMountRunner struct{}

func (slowMountRunner) Run(ctx context.Context, name string, _ ...string) (execx.Result, error) {
	<-ctx.Done()
	return execx.Result{ExitCode: -1}, fmt.Errorf("run %s: %w", name, ctx.Err())
}

func TestControllerRunTimeoutMessage(t *testing.T) {
	c := Controller{Runner: slowMountRunner{}, CommandTimeout: time.Millisecond}
	err := c.run(context.Background(), ActionUmount, "/mnt/backup")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout after 1ms") {
		t.Fatalf("error = %q, want timeout after duration", err.Error())
	}
	if strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("error = %q, want operator-facing timeout without raw context error", err.Error())
	}
}
