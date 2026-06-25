package checks

import (
	"context"
	"testing"
)

func fakeMounts(ms ...Mount) MountSamplerFunc {
	return func() ([]Mount, error) { return ms, nil }
}

var dataMount = Mount{Device: "/dev/sdb1", MountPoint: "/data", FSType: "ext4", Options: []string{"rw", "noatime"}}

// storageMount builds a storage check with a mount condition (and optional space preds)
// for the integrated mount tests.
func storageMount(m mountCond, sampler MountSamplerFunc, preds ...levelPred) storageCheck {
	return storageCheck{base: base{name: "fs"}, path: "/data", preds: preds, mount: m, mountSampler: sampler}
}

func TestStorageMountedOK(t *testing.T) {
	c := storageMount(mountCond{active: true, expectMount: true}, fakeMounts(dataMount))
	res := c.Run(context.Background())
	if res.OK { // mounted as expected, no space pred -> not an alert
		t.Fatalf("mounted-as-expected should not alert, got %q", res.Message)
	}
	if res.Data["mounted"] != true || res.Data["fstype"] != "ext4" {
		t.Fatalf("unexpected data: %+v", res.Data)
	}
}

func TestStorageMountedPrefersRealMountOverAutofsPlaceholder(t *testing.T) {
	c := storageCheck{
		base:  base{name: "fs"},
		path:  "/var/lib/libvirt/images",
		mount: mountCond{active: true, expectMount: true},
		mountSampler: fakeMounts(
			Mount{Device: "systemd-1", MountPoint: "/var/lib/libvirt/images", FSType: "autofs", Options: []string{"rw"}},
			Mount{Device: "172.31.27.100:/", MountPoint: "/var/lib/libvirt/images", FSType: "ceph", Options: []string{"rw"}},
		),
	}
	res := c.Run(context.Background())
	if res.OK {
		t.Fatalf("mounted-as-expected should not alert, got %q", res.Message)
	}
	if res.Data["fstype"] != "ceph" || res.Data["device"] != "172.31.27.100:/" {
		t.Fatalf("unexpected mount data: %+v", res.Data)
	}
}

func TestStorageNotMountedAlerts(t *testing.T) {
	c := storageMount(mountCond{active: true, expectMount: true}, fakeMounts())
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatal("an unmounted path must alert (OK=true)")
	}
	if res.Data["mounted"] != false {
		t.Fatalf("data mounted should be false: %+v", res.Data)
	}
}

func TestStorageExpectUnmounted(t *testing.T) {
	mountedNow := storageMount(mountCond{active: true, expectMount: false}, fakeMounts(dataMount))
	if !mountedNow.Run(context.Background()).OK {
		t.Fatal("expected-unmounted must alert when mounted")
	}
	notMounted := storageMount(mountCond{active: true, expectMount: false}, fakeMounts())
	if notMounted.Run(context.Background()).OK {
		t.Fatal("expected-unmounted must not alert when not mounted")
	}
}

func TestStorageMountTakesPrecedenceOverSpace(t *testing.T) {
	// Not mounted: the space predicate must be skipped (statfs would read the
	// parent fs); the check alerts on the mount problem, and usage is never called.
	usageCalled := false
	c := storageCheck{
		base:         base{name: "fs"},
		path:         "/data",
		preds:        []levelPred{{"used_pct", ">=", 90}},
		mount:        mountCond{active: true, expectMount: true},
		mountSampler: fakeMounts(), // not mounted
		usage:        func(string) (StorageStats, error) { usageCalled = true; return StorageStats{}, nil },
	}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatal("unmounted path must alert before checking space")
	}
	if usageCalled {
		t.Fatal("statfs must not run when the mount expectation is violated")
	}
}

func TestBuildStorageMountCheck(t *testing.T) {
	// Mount-only storage check (no space predicate) builds and runs.
	built, warns := Build(map[string]any{
		"data": map[string]any{"type": "storage", "path": "/data", "mounted": true},
	}, Deps{MountSampler: fakeMounts(dataMount)})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(built) != 1 || built[0].Check.Run(context.Background()).OK {
		t.Fatal("mounted-as-expected storage check should build and not alert")
	}
	// A storage check with neither space predicate nor mount condition is rejected.
	if _, warns := Build(map[string]any{"d": map[string]any{"type": "storage", "path": "/data"}}, Deps{}); len(warns) == 0 {
		t.Fatal("a storage check with no predicate and no mount condition should warn")
	}
}

func TestUnescapeMount(t *testing.T) {
	if got := unescapeMount(`/mnt/my\040disk`); got != "/mnt/my disk" {
		t.Fatalf("unescapeMount = %q", got)
	}
	if got := unescapeMount(`/mnt/tab\011nl\012bs\134x`); got != "/mnt/tab\tnl\nbs\\x" {
		t.Fatalf("unescapeMount escapes = %q", got)
	}
}

func TestMountForPathReturnsDeepestContainingMount(t *testing.T) {
	mounts := []Mount{
		{Device: "/dev/root", MountPoint: "/", FSType: "ext4"},
		{Device: "/dev/var", MountPoint: "/var", FSType: "ext4"},
		{Device: "/dev/data", MountPoint: "/var/lib/sermo", FSType: "xfs"},
		{Device: "/dev/other", MountPoint: "/var/lib-other", FSType: "xfs"},
	}

	got := MountForPath(mounts, "/var/lib/sermo/db/state")
	if got == nil || got.MountPoint != "/var/lib/sermo" {
		t.Fatalf("MountForPath deep path = %+v, want /var/lib/sermo", got)
	}

	got = MountForPath(mounts, "/var/lib-other/cache")
	if got == nil || got.MountPoint != "/var/lib-other" {
		t.Fatalf("MountForPath boundary path = %+v, want /var/lib-other", got)
	}

	if got = MountForPath(mounts, "var/lib/sermo"); got != nil {
		t.Fatalf("MountForPath(relative path) = %+v, want nil", got)
	}

	got = MountForPath([]Mount{{Device: "/dev/var", MountPoint: "/var", FSType: "ext4"}}, "/varnish/cache")
	if got != nil {
		t.Fatalf("MountForPath(sibling prefix) = %+v, want nil", got)
	}
}

func TestMountForPathPrefersRealMountOverAutofsPlaceholder(t *testing.T) {
	mounts := []Mount{
		{Device: "systemd-1", MountPoint: "/var/lib/libvirt/images", FSType: "autofs"},
		{Device: "172.31.27.100:/", MountPoint: "/var/lib/libvirt/images", FSType: "ceph"},
	}
	got := MountForPath(mounts, "/var/lib/libvirt/images/base.qcow2")
	if got == nil || got.FSType != "ceph" {
		t.Fatalf("MountForPath = %+v, want ceph mount", got)
	}
}

func TestMountForPathEqualLengthPrefersBetterMount(t *testing.T) {
	// Two mounts share the same mountpoint length; the longest-prefix tie is
	// broken by betterMount, which keeps the real (non-autofs) mount already
	// chosen rather than overwriting it with the equal-length autofs entry.
	mounts := []Mount{
		{Device: "/dev/sda1", MountPoint: "/mnt", FSType: "ext4"},
		{Device: "systemd-1", MountPoint: "/mnt", FSType: "autofs"},
	}
	got := MountForPath(mounts, "/mnt/data")
	if got == nil || got.FSType != "ext4" {
		t.Fatalf("MountForPath = %+v, want the ext4 mount (betterMount tie-break)", got)
	}
}
