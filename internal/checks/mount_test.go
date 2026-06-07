package checks

import (
	"context"
	"testing"
)

func fakeMounts(ms ...Mount) MountSamplerFunc {
	return func() ([]Mount, error) { return ms, nil }
}

var dataMount = Mount{Device: "/dev/sdb1", MountPoint: "/data", FSType: "ext4", Options: []string{"rw", "noatime"}}

// diskMount builds a disk check with mount conditions (and optional space preds)
// for the integrated mount tests.
func diskMount(m mountCond, sampler MountSamplerFunc, preds ...diskPred) diskCheck {
	return diskCheck{base: base{name: "fs"}, path: "/data", preds: preds, mount: m, mountSampler: sampler}
}

func TestDiskMountedOK(t *testing.T) {
	c := diskMount(mountCond{active: true, expectMount: true}, fakeMounts(dataMount))
	res := c.Run(context.Background())
	if res.OK { // mounted as expected, no space pred -> not an alert
		t.Fatalf("mounted-as-expected should not alert, got %q", res.Message)
	}
	if res.Data["mounted"] != true || res.Data["fstype"] != "ext4" {
		t.Fatalf("unexpected data: %+v", res.Data)
	}
}

func TestDiskNotMountedAlerts(t *testing.T) {
	c := diskMount(mountCond{active: true, expectMount: true}, fakeMounts())
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatal("an unmounted path must alert (OK=true)")
	}
	if res.Data["mounted"] != false {
		t.Fatalf("data mounted should be false: %+v", res.Data)
	}
}

func TestDiskExpectUnmounted(t *testing.T) {
	mountedNow := diskMount(mountCond{active: true, expectMount: false}, fakeMounts(dataMount))
	if !mountedNow.Run(context.Background()).OK {
		t.Fatal("expected-unmounted must alert when mounted")
	}
	notMounted := diskMount(mountCond{active: true, expectMount: false}, fakeMounts())
	if notMounted.Run(context.Background()).OK {
		t.Fatal("expected-unmounted must not alert when not mounted")
	}
}

func TestDiskMountConditionsFstypeOptionsDevice(t *testing.T) {
	bad := diskMount(mountCond{active: true, expectMount: true, fstype: "xfs"}, fakeMounts(dataMount))
	if !bad.Run(context.Background()).OK {
		t.Fatal("fstype mismatch must alert")
	}
	bad = diskMount(mountCond{active: true, expectMount: true, options: []string{"ro"}}, fakeMounts(dataMount))
	if !bad.Run(context.Background()).OK {
		t.Fatal("a missing option must alert")
	}
	bad = diskMount(mountCond{active: true, expectMount: true, device: "/dev/sda1"}, fakeMounts(dataMount))
	if !bad.Run(context.Background()).OK {
		t.Fatal("device mismatch must alert")
	}
	good := diskMount(mountCond{active: true, expectMount: true, fstype: "ext4", options: []string{"rw", "noatime"}, device: "/dev/sdb1"}, fakeMounts(dataMount))
	if good.Run(context.Background()).OK {
		t.Fatal("matching fstype/options/device must not alert")
	}
}

func TestDiskMountTakesPrecedenceOverSpace(t *testing.T) {
	// Not mounted: the space predicate must be skipped (statfs would read the
	// parent fs); the check alerts on the mount problem, and usage is never called.
	usageCalled := false
	c := diskCheck{
		base:         base{name: "fs"},
		path:         "/data",
		preds:        []diskPred{{"used_pct", ">=", 90}},
		mount:        mountCond{active: true, expectMount: true},
		mountSampler: fakeMounts(), // not mounted
		usage:        func(string) (DiskStats, error) { usageCalled = true; return DiskStats{}, nil },
	}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatal("unmounted path must alert before checking space")
	}
	if usageCalled {
		t.Fatal("statfs must not run when the mount expectation is violated")
	}
}

func TestBuildDiskMountCheck(t *testing.T) {
	// Mount-only disk check (no space predicate) builds and runs.
	built, warns := Build(map[string]any{
		"data": map[string]any{"type": "disk", "path": "/data", "fstype": "ext4", "options": []any{"rw"}},
	}, Deps{MountSampler: fakeMounts(dataMount)})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(built) != 1 || built[0].Check.Run(context.Background()).OK {
		t.Fatal("mounted-as-expected disk check should build and not alert")
	}
	// A disk check with neither space predicate nor mount condition is rejected.
	if _, warns := Build(map[string]any{"d": map[string]any{"type": "disk", "path": "/data"}}, Deps{}); len(warns) == 0 {
		t.Fatal("a disk check with no predicate and no mount condition should warn")
	}
}

func TestUnescapeMount(t *testing.T) {
	if got := unescapeMount(`/mnt/my\040disk`); got != "/mnt/my disk" {
		t.Fatalf("unescapeMount = %q", got)
	}
}
