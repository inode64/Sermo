package volume

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
)

func staticMounts(ms ...Mount) MountSource {
	return func() ([]Mount, error) { return ms, nil }
}

func TestResolveLVM(t *testing.T) {
	r := &execxtest.Runner{ByName: map[string]execx.Result{
		"lvs": {Stdout: "  vg0,data\n"},
	}}
	e := Expander{
		Runner: r,
		Mounts: staticMounts(
			Mount{Device: "/dev/mapper/vg0-data", MountPoint: "/mnt/backup", FSType: "ext4"},
			Mount{Device: "/dev/sda1", MountPoint: "/", FSType: "ext4"},
		),
	}
	// A path *under* the mountpoint resolves to the containing mount.
	tgt, err := e.Resolve(context.Background(), "/mnt/backup/sub")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if tgt.Mountpoint != "/mnt/backup" || tgt.FSType != "ext4" {
		t.Fatalf("mount fields wrong: %+v", tgt)
	}
	if tgt.VG != "vg0" || tgt.LV != "data" {
		t.Fatalf("VG/LV = %q/%q, want vg0/data", tgt.VG, tgt.LV)
	}
}

func TestResolveUsesSharedMountSelection(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		mounts     []Mount
		wantMount  string
		wantFSType string
		wantDevice string
	}{
		{
			name: "deepest cleaned mount",
			path: "/data//db/./records",
			mounts: []Mount{
				{Device: "/dev/mapper/vg0-root", MountPoint: "/", FSType: "ext4"},
				{Device: "/dev/mapper/vg0-data", MountPoint: "/data", FSType: "ext4"},
				{Device: "/dev/mapper/vg0-db", MountPoint: "/data/db/", FSType: "xfs"},
			},
			wantMount:  "/data/db/",
			wantFSType: "xfs",
			wantDevice: "/dev/mapper/vg0-db",
		},
		{
			name: "parent segment",
			path: "/data/../srv/records",
			mounts: []Mount{
				{Device: "/dev/mapper/vg0-root", MountPoint: "/", FSType: "ext4"},
				{Device: "/dev/mapper/vg0-data", MountPoint: "/data", FSType: "ext4"},
				{Device: "/dev/mapper/vg0-srv", MountPoint: "/srv", FSType: "xfs"},
			},
			wantMount:  "/srv",
			wantFSType: "xfs",
			wantDevice: "/dev/mapper/vg0-srv",
		},
		{
			name: "real filesystem after autofs",
			path: "/mnt/archive/records",
			mounts: []Mount{
				{Device: "systemd-1", MountPoint: "/mnt/archive", FSType: checks.FSTypeAutofs},
				{Device: "/dev/mapper/vg0-archive", MountPoint: "/mnt/archive", FSType: "ext4"},
			},
			wantMount:  "/mnt/archive",
			wantFSType: "ext4",
			wantDevice: "/dev/mapper/vg0-archive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &execxtest.Runner{ByName: map[string]execx.Result{
				cmdLVS: {Stdout: "vg0,data\n"},
			}}
			target, err := (Expander{Runner: r, Mounts: staticMounts(tt.mounts...)}).Resolve(context.Background(), tt.path)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tt.path, err)
			}
			if target.Mountpoint != tt.wantMount || target.FSType != tt.wantFSType {
				t.Fatalf("Resolve(%q) target = %+v, want mount %q and filesystem %q", tt.path, target, tt.wantMount, tt.wantFSType)
			}
			if len(r.Lines()) != 1 || !strings.HasSuffix(r.Lines()[0], " "+tt.wantDevice) {
				t.Fatalf("Resolve(%q) calls = %v, want lvs for %q", tt.path, r.Lines(), tt.wantDevice)
			}
		})
	}
}

func TestResolveRejectsInvalidPathBeforeLVS(t *testing.T) {
	for _, path := range []string{"", "data/records"} {
		t.Run(fmt.Sprintf("path=%q", path), func(t *testing.T) {
			r := &execxtest.Runner{ByName: map[string]execx.Result{
				cmdLVS: {Stdout: "vg0,root\n"},
			}}
			e := Expander{
				Runner: r,
				Mounts: staticMounts(Mount{Device: "/dev/mapper/vg0-root", MountPoint: "/", FSType: "ext4"}),
			}
			if _, err := e.Resolve(context.Background(), path); err == nil {
				t.Fatalf("Resolve(%q) succeeded, want no containing mount", path)
			}
			if len(r.Lines()) != 0 {
				t.Fatalf("Resolve(%q) calls = %v, want no lvs command", path, r.Lines())
			}
		})
	}
}

func TestListFiltersPseudoFilesystems(t *testing.T) {
	src := staticMounts(
		Mount{Device: "proc", MountPoint: "/proc", FSType: "proc"},
		Mount{Device: "tmpfs", MountPoint: "/run", FSType: "tmpfs"},
		Mount{Device: "systemd-1", MountPoint: "/var/lib/libvirt/images", FSType: "autofs"},
		Mount{Device: "/dev/sda1", MountPoint: "/", FSType: "ext4"},
		Mount{Device: "/dev/mapper/vg0-data", MountPoint: "/mnt/backup", FSType: "ext4"},
		Mount{Device: "192.0.2.102:/srv/backup", MountPoint: "/srv/backup", FSType: "nfs4"},
		Mount{Device: "192.0.2.100:/", MountPoint: "/var/lib/libvirt/images", FSType: "ceph"},
		Mount{Device: "/dev/sda1", MountPoint: "/", FSType: "ext4"}, // dup mountpoint
		Mount{Device: "/dev/sda1", MountPoint: "/srv/workspace", FSType: "ext4"},
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
		if got[i].MountPoint != want[i] {
			t.Fatalf("mount[%d] = %q, want %q; got %+v", i, got[i].MountPoint, want[i], got)
		}
	}
}

func TestListRejectsNonStorageMounts(t *testing.T) {
	src := staticMounts(
		Mount{Device: "none", MountPoint: "/run/credentials/x.service", FSType: "tmpfs"},
		Mount{Device: "systemd-1", MountPoint: "/proc/sys/fs/binfmt_misc", FSType: "autofs"},
		Mount{Device: "systemd-1", MountPoint: "/mnt/placeholder", FSType: "autofs"},
		Mount{Device: "rpc_pipefs", MountPoint: "/run/rpc_pipefs", FSType: "rpc_pipefs"},
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
	r := &execxtest.Runner{ByName: map[string]execx.Result{
		"vgs": {Stdout: "  2147483648\n"}, // 2 GiB free
	}}
	e := Expander{Runner: r}
	tgt := Target{Mountpoint: "/mnt/backup", FSType: "ext4", VG: "vg0", LV: "data"}

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
	assertCalls(t, r.Lines(), want)
}

func TestExpandXFSAndBtrfsUseMountpoint(t *testing.T) {
	for _, tc := range []struct {
		fs   string
		grow string
	}{
		{"xfs", "xfs_growfs /mnt/backup"},
		{"btrfs", "btrfs filesystem resize max /mnt/backup"},
	} {
		r := &execxtest.Runner{ByName: map[string]execx.Result{"vgs": {Stdout: "1073741824"}}}
		e := Expander{Runner: r}
		tgt := Target{Mountpoint: "/mnt/backup", FSType: tc.fs, VG: "vg0", LV: "data"}
		if _, err := e.Expand(context.Background(), tgt, 512<<20); err != nil {
			t.Fatalf("%s Expand: %v", tc.fs, err)
		}
		if got := r.Lines()[len(r.Lines())-1]; got != tc.grow {
			t.Fatalf("%s last call = %q, want %q", tc.fs, got, tc.grow)
		}
	}
}

func TestExpandRejectsNonPositiveSize(t *testing.T) {
	for _, by := range []int64{0, -1 << 20} {
		r := &execxtest.Runner{ByName: map[string]execx.Result{"vgs": {Stdout: "2147483648"}}}
		e := Expander{Runner: r}
		tgt := Target{Mountpoint: "/mnt/backup", FSType: "ext4", VG: "vg0", LV: "data"}
		if _, err := e.Expand(context.Background(), tgt, by); err == nil {
			t.Fatalf("Expand(by=%d) must error, not run lvextend", by)
		}
		for _, call := range r.Lines() {
			if strings.HasPrefix(call, "lvextend") {
				t.Fatalf("Expand(by=%d) ran %q; lvextend must not run for a non-positive size", by, call)
			}
		}
	}
}

func TestExpandNoFreeSpaceErrors(t *testing.T) {
	r := &execxtest.Runner{ByName: map[string]execx.Result{"vgs": {Stdout: "0"}}}
	e := Expander{Runner: r}
	tgt := Target{Mountpoint: "/mnt/backup", FSType: "ext4", VG: "vg0", LV: "data"}
	if _, err := e.Expand(context.Background(), tgt, 1<<30); err == nil {
		t.Fatal("expand with zero VG free must error")
	}
	for _, c := range r.Lines() {
		if strings.HasPrefix(c, "lvextend") {
			t.Fatalf("must not lvextend when the VG is full: %v", r.Lines())
		}
	}
}

func TestExpandUnknownFSErrors(t *testing.T) {
	r := &execxtest.Runner{ByName: map[string]execx.Result{"vgs": {Stdout: "1073741824"}}}
	e := Expander{Runner: r}
	tgt := Target{Mountpoint: "/x", FSType: "reiserfs", VG: "vg0", LV: "data"}
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
	r := &execxtest.Runner{
		ByName: map[string]execx.Result{"lvs": {ExitCode: 5}},
		Errs:   map[string]error{"lvs": context.DeadlineExceeded},
	}
	e := Expander{
		Runner: r,
		Mounts: staticMounts(Mount{Device: "/dev/sda1", MountPoint: "/data", FSType: "xfs"}),
	}
	if _, err := e.Resolve(context.Background(), "/data"); err == nil {
		t.Fatal("a non-LVM device must error")
	}
}

func TestResolveUnknownPath(t *testing.T) {
	e2 := Expander{Runner: &execxtest.Runner{}, Mounts: staticMounts(Mount{Device: "/dev/sdb1", MountPoint: "/srv", FSType: "ext4"})}
	if _, err := e2.Resolve(context.Background(), "/mnt/x"); err == nil {
		t.Fatal("a path with no containing mount must error")
	}
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
			Mount{Device: "/dev/mapper/vg0-data", MountPoint: "/data", FSType: "ext4"},
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
