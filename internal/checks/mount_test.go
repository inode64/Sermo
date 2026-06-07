package checks

import (
	"context"
	"testing"
)

func fakeMounts(ms ...Mount) MountSamplerFunc {
	return func() ([]Mount, error) { return ms, nil }
}

var dataMount = Mount{Device: "/dev/sdb1", MountPoint: "/data", FSType: "ext4", Options: []string{"rw", "noatime"}}

func TestMountMountedOK(t *testing.T) {
	c := mountCheck{base: base{name: "m"}, path: "/data", expectMount: true, sampler: fakeMounts(dataMount)}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("a mounted path should pass, got %q", res.Message)
	}
	if res.Data["mounted"] != true || res.Data["fstype"] != "ext4" {
		t.Fatalf("unexpected data: %+v", res.Data)
	}
}

func TestMountNotMountedFails(t *testing.T) {
	c := mountCheck{base: base{name: "m"}, path: "/data", expectMount: true, sampler: fakeMounts()}
	res := c.Run(context.Background())
	if res.OK {
		t.Fatal("an unmounted path should fail")
	}
	if res.Data["mounted"] != false {
		t.Fatalf("data mounted should be false: %+v", res.Data)
	}
}

func TestMountExpectUnmounted(t *testing.T) {
	// mounted: false — fail when the path IS a mount point.
	mountedNow := mountCheck{base: base{name: "m"}, path: "/data", expectMount: false, sampler: fakeMounts(dataMount)}
	if mountedNow.Run(context.Background()).OK {
		t.Fatal("expected not-mounted should fail when mounted")
	}
	notMounted := mountCheck{base: base{name: "m"}, path: "/data", expectMount: false, sampler: fakeMounts()}
	if !notMounted.Run(context.Background()).OK {
		t.Fatal("expected not-mounted should pass when not mounted")
	}
}

func TestMountConditionsFstypeOptionsDevice(t *testing.T) {
	base := func() mountCheck {
		return mountCheck{base: baseT("m"), path: "/data", expectMount: true, sampler: fakeMounts(dataMount)}
	}
	// fstype mismatch.
	bad := base()
	bad.fstype = "xfs"
	if bad.Run(context.Background()).OK {
		t.Fatal("fstype mismatch should fail")
	}
	// missing option.
	bad = base()
	bad.options = []string{"ro"}
	if bad.Run(context.Background()).OK {
		t.Fatal("a missing option should fail")
	}
	// device mismatch.
	bad = base()
	bad.device = "/dev/sda1"
	if bad.Run(context.Background()).OK {
		t.Fatal("device mismatch should fail")
	}
	// all conditions satisfied.
	good := base()
	good.fstype = "ext4"
	good.options = []string{"rw", "noatime"}
	good.device = "/dev/sdb1"
	if !good.Run(context.Background()).OK {
		t.Fatal("matching fstype/options/device should pass")
	}
}

func TestBuildMountCheck(t *testing.T) {
	built, warns := Build(map[string]any{
		"data": map[string]any{
			"type": "mount", "path": "/data", "fstype": "ext4", "options": []any{"rw"},
		},
	}, Deps{MountSampler: fakeMounts(dataMount)})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(built) != 1 || !built[0].Check.Run(context.Background()).OK {
		t.Fatal("mount check should build and pass")
	}
	if _, warns := Build(map[string]any{"m": map[string]any{"type": "mount"}}, Deps{}); len(warns) == 0 {
		t.Fatal("mount check without a path should warn")
	}
}

func baseT(name string) base { return base{name: name} }

func TestUnescapeMount(t *testing.T) {
	if got := unescapeMount(`/mnt/my\040disk`); got != "/mnt/my disk" {
		t.Fatalf("unescapeMount = %q", got)
	}
}
