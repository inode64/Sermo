package volume

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"sermo/internal/execx"
)

// fakeRunner returns canned output per command name and records the argv.
type fakeRunner struct {
	out   map[string]execx.Result
	err   map[string]error
	calls []string
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	call := name
	if len(args) > 0 {
		call += " " + strings.Join(args, " ")
	}
	r.calls = append(r.calls, call)
	return r.out[name], r.err[name]
}

func staticMounts(ms ...Mount) MountSource {
	return func() ([]Mount, error) { return ms, nil }
}

func TestContainingMountLongestPrefixWins(t *testing.T) {
	mounts := []Mount{
		{Mountpoint: "/"},
		{Mountpoint: "/data"},
		{Mountpoint: "/data/db/"}, // trailing slash must normalize
	}
	cases := []struct {
		path string
		want string
		ok   bool
	}{
		{"/data/db/x", "/data/db/", true}, // most specific mount
		{"/data/other", "/data", true},
		{"/etc", "/", true},      // falls back to root
		{"/data", "/data", true}, // exact match
	}
	for _, tc := range cases {
		// Order independence: the longest matching prefix must win regardless of
		// the order mounts are scanned in.
		for _, ms := range [][]Mount{mounts, {mounts[2], mounts[1], mounts[0]}} {
			got, ok := containingMount(ms, tc.path)
			if ok != tc.ok || got.Mountpoint != tc.want {
				t.Fatalf("containingMount(%q) = %q/%v, want %q/%v", tc.path, got.Mountpoint, ok, tc.want, tc.ok)
			}
		}
	}
}

func TestResolveLVM(t *testing.T) {
	r := &fakeRunner{out: map[string]execx.Result{
		"lvs": {Stdout: "  vg0,data\n"},
	}}
	e := Expander{
		Runner: r,
		Mounts: staticMounts(
			Mount{Device: "/dev/mapper/vg0-data", Mountpoint: "/mnt/backup", FSType: "ext4"},
			Mount{Device: "/dev/sda1", Mountpoint: "/", FSType: "ext4"},
		),
	}
	// A path *under* the mountpoint resolves to the containing mount.
	tgt, err := e.Resolve(context.Background(), "/mnt/backup/sub")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if tgt.Mountpoint != "/mnt/backup" || tgt.FSType != "ext4" || tgt.Device != "/dev/mapper/vg0-data" {
		t.Fatalf("mount fields wrong: %+v", tgt)
	}
	if tgt.VG != "vg0" || tgt.LV != "data" {
		t.Fatalf("VG/LV = %q/%q, want vg0/data", tgt.VG, tgt.LV)
	}
}

func TestListFiltersPseudoFilesystems(t *testing.T) {
	src := staticMounts(
		Mount{Device: "proc", Mountpoint: "/proc", FSType: "proc"},
		Mount{Device: "tmpfs", Mountpoint: "/run", FSType: "tmpfs"},
		Mount{Device: "systemd-1", Mountpoint: "/var/lib/libvirt/images", FSType: "autofs"},
		Mount{Device: "/dev/sda1", Mountpoint: "/", FSType: "ext4"},
		Mount{Device: "/dev/mapper/vg0-data", Mountpoint: "/mnt/backup", FSType: "ext4"},
		Mount{Device: "192.0.2.102:/srv/backup", Mountpoint: "/srv/backup", FSType: "nfs4"},
		Mount{Device: "192.0.2.100:/", Mountpoint: "/var/lib/libvirt/images", FSType: "ceph"},
		Mount{Device: "/dev/sda1", Mountpoint: "/", FSType: "ext4"}, // dup mountpoint
		Mount{Device: "/dev/sda1", Mountpoint: "/srv/workspace", FSType: "ext4"},
	)
	got, err := List(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d mounts, want 4 (real storage mounts, deduped): %+v", len(got), got)
	}
	want := []string{"/", "/mnt/backup", "/srv/backup", "/var/lib/libvirt/images"}
	for i := range want {
		if got[i].Mountpoint != want[i] {
			t.Fatalf("mount[%d] = %q, want %q; got %+v", i, got[i].Mountpoint, want[i], got)
		}
	}
}

func TestListRejectsNonStorageMounts(t *testing.T) {
	src := staticMounts(
		Mount{Device: "none", Mountpoint: "/run/credentials/x.service", FSType: "tmpfs"},
		Mount{Device: "systemd-1", Mountpoint: "/proc/sys/fs/binfmt_misc", FSType: "autofs"},
		Mount{Device: "systemd-1", Mountpoint: "/mnt/placeholder", FSType: "autofs"},
		Mount{Device: "rpc_pipefs", Mountpoint: "/run/rpc_pipefs", FSType: "rpc_pipefs"},
	)
	got, err := List(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("unexpected mounts: %+v", got)
	}
}

func TestExpandExt4CapsToFreeAndGrows(t *testing.T) {
	r := &fakeRunner{out: map[string]execx.Result{
		"vgs": {Stdout: "  2147483648\n"}, // 2 GiB free
	}}
	e := Expander{Runner: r}
	tgt := Target{Mountpoint: "/mnt/backup", FSType: "ext4", Device: "/dev/mapper/vg0-data", VG: "vg0", LV: "data"}

	// Request 5 GiB but only 2 GiB free -> cap to 2 GiB.
	res, err := e.Expand(context.Background(), tgt, 5<<30)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if res.GrewBytes != 2<<30 {
		t.Fatalf("GrewBytes = %d, want %d", res.GrewBytes, 2<<30)
	}
	want := []string{
		"vgs --noheadings -o vg_free --units b --nosuffix vg0",
		"lvextend -L+2147483648b /dev/vg0/data",
		"resize2fs /dev/vg0/data",
	}
	assertCalls(t, r.calls, want)
}

func TestExpandXFSAndBtrfsUseMountpoint(t *testing.T) {
	for _, tc := range []struct {
		fs   string
		grow string
	}{
		{"xfs", "xfs_growfs /mnt/backup"},
		{"btrfs", "btrfs filesystem resize max /mnt/backup"},
	} {
		r := &fakeRunner{out: map[string]execx.Result{"vgs": {Stdout: "1073741824"}}}
		e := Expander{Runner: r}
		tgt := Target{Mountpoint: "/mnt/backup", FSType: tc.fs, Device: "/dev/vg0/data", VG: "vg0", LV: "data"}
		if _, err := e.Expand(context.Background(), tgt, 512<<20); err != nil {
			t.Fatalf("%s Expand: %v", tc.fs, err)
		}
		if got := r.calls[len(r.calls)-1]; got != tc.grow {
			t.Fatalf("%s last call = %q, want %q", tc.fs, got, tc.grow)
		}
	}
}

func TestExpandRejectsNonPositiveSize(t *testing.T) {
	for _, by := range []int64{0, -1 << 20} {
		r := &fakeRunner{out: map[string]execx.Result{"vgs": {Stdout: "2147483648"}}}
		e := Expander{Runner: r}
		tgt := Target{Mountpoint: "/mnt/backup", FSType: "ext4", Device: "/dev/vg0/data", VG: "vg0", LV: "data"}
		if _, err := e.Expand(context.Background(), tgt, by); err == nil {
			t.Fatalf("Expand(by=%d) must error, not run lvextend", by)
		}
		for _, call := range r.calls {
			if strings.HasPrefix(call, "lvextend") {
				t.Fatalf("Expand(by=%d) ran %q; lvextend must not run for a non-positive size", by, call)
			}
		}
	}
}

func TestExpandNoFreeSpaceErrors(t *testing.T) {
	r := &fakeRunner{out: map[string]execx.Result{"vgs": {Stdout: "0"}}}
	e := Expander{Runner: r}
	tgt := Target{Mountpoint: "/mnt/backup", FSType: "ext4", Device: "/dev/vg0/data", VG: "vg0", LV: "data"}
	if _, err := e.Expand(context.Background(), tgt, 1<<30); err == nil {
		t.Fatal("expand with zero VG free must error")
	}
	for _, c := range r.calls {
		if strings.HasPrefix(c, "lvextend") {
			t.Fatalf("must not lvextend when the VG is full: %v", r.calls)
		}
	}
}

func TestExpandUnknownFSErrors(t *testing.T) {
	r := &fakeRunner{out: map[string]execx.Result{"vgs": {Stdout: "1073741824"}}}
	e := Expander{Runner: r}
	tgt := Target{Mountpoint: "/x", FSType: "reiserfs", Device: "/dev/vg0/data", VG: "vg0", LV: "data"}
	if _, err := e.Expand(context.Background(), tgt, 1<<20); err == nil {
		t.Fatal("unknown fstype must error")
	}
}

func assertCalls(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolveNotLVM(t *testing.T) {
	r := &fakeRunner{
		out: map[string]execx.Result{"lvs": {ExitCode: 5}},
		err: map[string]error{"lvs": context.DeadlineExceeded},
	}
	e := Expander{
		Runner: r,
		Mounts: staticMounts(Mount{Device: "/dev/sda1", Mountpoint: "/data", FSType: "xfs"}),
	}
	if _, err := e.Resolve(context.Background(), "/data"); err == nil {
		t.Fatal("a non-LVM device must error")
	}
}

func TestResolveUnknownPath(t *testing.T) {
	e := Expander{Runner: &fakeRunner{}, Mounts: staticMounts(Mount{Device: "/dev/sda1", Mountpoint: "/", FSType: "ext4"})}
	// "/" matches as a fallback containing mount, so use a path that cannot match
	// when there is no root mount.
	e2 := Expander{Runner: &fakeRunner{}, Mounts: staticMounts(Mount{Device: "/dev/sdb1", Mountpoint: "/srv", FSType: "ext4"})}
	if _, err := e2.Resolve(context.Background(), "/mnt/x"); err == nil {
		t.Fatal("a path with no containing mount must error")
	}
	_ = e
}

type slowVolumeRunner struct{}

func (slowVolumeRunner) Run(ctx context.Context, name string, _ ...string) (execx.Result, error) {
	<-ctx.Done()
	return execx.Result{ExitCode: -1}, fmt.Errorf("run %s: %w", name, ctx.Err())
}

func TestResolveLVSTimeoutMessage(t *testing.T) {
	e := Expander{
		Runner:  slowVolumeRunner{},
		Timeout: time.Millisecond,
		Mounts: staticMounts(
			Mount{Device: "/dev/mapper/vg0-data", Mountpoint: "/data", FSType: "ext4"},
		),
	}
	_, err := e.Resolve(context.Background(), "/data/sub")
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
