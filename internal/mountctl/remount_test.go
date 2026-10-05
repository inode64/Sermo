package mountctl

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"sermo/internal/checks"
)

// remountHost is a fake host for Remount: one NFS mount at path, optionally
// under an autofs trigger, whose unmount honours only the flags it accepts.
type remountHost struct {
	path      string
	autofs    bool
	mounted   bool
	accepts   int // umount2 flag that detaches; 0 detaches on any flag
	unmounts  []int
	answering bool
}

func (h *remountHost) table() ([]checks.Mount, error) {
	var out []checks.Mount
	if h.autofs {
		out = append(out, checks.Mount{MountPoint: "/net", FSType: checks.FSTypeAutofs})
	}
	if h.mounted {
		out = append(out, checks.Mount{MountPoint: h.path, FSType: "nfs"})
	}
	return out, nil
}

func (h *remountHost) unmount(_ string, flags int) error {
	h.unmounts = append(h.unmounts, flags)
	if h.accepts == 0 || flags == h.accepts {
		h.mounted = false
		return nil
	}
	return unix.EBUSY
}

func (h *remountHost) answers(string, time.Duration) error {
	if h.autofs && !h.mounted && h.answering {
		h.mounted = true // the access mounts it again
	}
	if !h.mounted || !h.answering {
		return errors.New("hung mount: no answer within 1s")
	}
	return nil
}

func remountController(t *testing.T, h *remountHost, runner *fakeRunner, inFstab bool) Controller {
	t.Helper()
	return Controller{
		Runtime: t.TempDir(),
		Runner:  runner,
		Mounts:  h.table,
		InFstab: func(string) (bool, error) { return inFstab, nil },
		Unmount: h.unmount,
		Answers: h.answers,
	}
}

func TestRemountFromFstabForcesTheHungMountOff(t *testing.T) {
	h := &remountHost{path: "/mnt/nas", mounted: true, answering: true}
	runner := &fakeRunner{mounted: &h.mounted}
	res, err := remountController(t, h, runner, true).Remount(context.Background(), EphemeralSpec(h.path))
	if err != nil || res.Status != ResultOK || !res.Forced || res.Lazy {
		t.Fatalf("Remount = %+v, %v; want a forced remount", res, err)
	}
	if len(h.unmounts) != 1 || h.unmounts[0] != unix.MNT_FORCE || strings.Join(runner.calls, "|") != "mount /mnt/nas" {
		t.Fatalf("unmounts %v commands %q", h.unmounts, runner.calls)
	}
}

func TestRemountFallsBackToALazyDetachUnderAutofs(t *testing.T) {
	h := &remountHost{path: "/net/nas/linux", autofs: true, mounted: true, accepts: unix.MNT_DETACH, answering: true}
	runner := &fakeRunner{mounted: &h.mounted}
	res, err := remountController(t, h, runner, false).Remount(context.Background(), EphemeralSpec(h.path))
	if err != nil || !res.Lazy || !h.mounted || len(runner.calls) != 0 {
		t.Fatalf("Remount = %+v, %v; commands %q; want autofs to mount it on access", res, err, runner.calls)
	}
}

func TestRemountReportsAMountThatStillDoesNotAnswer(t *testing.T) {
	h := &remountHost{path: "/mnt/nas", mounted: true}
	runner := &fakeRunner{mounted: &h.mounted}
	res, err := remountController(t, h, runner, true).Remount(context.Background(), EphemeralSpec(h.path))
	if err == nil || res.Status != ResultFailed || !strings.Contains(res.Message, "still does not answer") {
		t.Fatalf("Remount = %+v, %v; want a failure", res, err)
	}
}

func TestRemountRefusesAPathItCannotMountAgain(t *testing.T) {
	h := &remountHost{path: "/mnt/nas", answering: true}
	runner := &fakeRunner{mounted: &h.mounted}
	if _, err := remountController(t, h, runner, false).Remount(context.Background(), EphemeralSpec(h.path)); err == nil || !strings.Contains(err.Error(), "neither declared") {
		t.Fatalf("Remount error = %v", err)
	}
	if _, err := remountController(t, h, runner, true).Remount(context.Background(), EphemeralSpec("/")); err == nil {
		t.Fatal("Remount of / succeeded")
	}
}
