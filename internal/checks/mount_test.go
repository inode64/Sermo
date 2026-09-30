package checks

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sermo/internal/severity"
)

func fakeMounts(ms ...Mount) MountSamplerFunc {
	return func() ([]Mount, error) { return ms, nil }
}

var dataMount = Mount{Device: "/dev/sdb1", MountPoint: "/data", FSType: "ext4", Options: []string{"rw", "noatime"}}

// storageMount builds a storage check with a mount condition for the integrated
// mount tests.
func storageMount(m mountCond, sampler MountSamplerFunc) storageCheck {
	return storageCheck{name: "fs", path: "/data", mount: m, mountSampler: sampler}
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
		name:  "fs",
		path:  "/var/lib/libvirt/images",
		mount: mountCond{active: true, expectMount: true},
		mountSampler: fakeMounts(
			Mount{Device: "systemd-1", MountPoint: "/var/lib/libvirt/images", FSType: "autofs", Options: []string{"rw"}},
			Mount{Device: "192.0.2.100:/", MountPoint: "/var/lib/libvirt/images", FSType: "ceph", Options: []string{"rw"}},
		),
	}
	res := c.Run(context.Background())
	if res.OK {
		t.Fatalf("mounted-as-expected should not alert, got %q", res.Message)
	}
	if res.Data["fstype"] != "ceph" || res.Data["device"] != "192.0.2.100:/" {
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

func TestStorageMountSamplerErrorIsPublished(t *testing.T) {
	c := storageMount(mountCond{active: true, expectMount: true}, func() ([]Mount, error) {
		return nil, errors.New("mount table unavailable")
	})
	res := c.Run(context.Background())
	if res.OK {
		t.Fatalf("mount sampler error should not alert: %+v", res)
	}
	if res.Data[DataKeyPath] != "/data" || res.Data[DataKeyMountSampleError] != "mount table unavailable" {
		t.Fatalf("mount sampler error data = %+v", res.Data)
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
		name:         "fs",
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

func TestMountForPathReturnsDeepestContainingMount(t *testing.T) {
	mounts := []Mount{
		{Device: "/dev/root", MountPoint: "/", FSType: "ext4"},
		{Device: "/dev/var", MountPoint: "/var", FSType: "ext4"},
		{Device: "/dev/data", MountPoint: "/var/lib/sermo", FSType: "xfs"},
		{Device: "/dev/other", MountPoint: "/var/lib-other", FSType: "xfs"},
	}
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "deepest", path: "/var/lib/sermo/db/state", want: "/var/lib/sermo"},
		{name: "boundary", path: "/var/lib-other/cache", want: "/var/lib-other"},
		{name: "clean separators and dot", path: "/var//lib/sermo/db/./state", want: "/var/lib/sermo"},
		{name: "clean parent", path: "/var/lib/sermo/../other", want: "/var"},
		{name: "sibling prefix", path: "/varnish/cache", want: "/"},
		{name: "relative", path: "var/lib/sermo"},
		{name: "empty", path: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MountForPath(mounts, tt.path)
			if tt.want == "" {
				if got != nil {
					t.Fatalf("MountForPath(%q) = %+v, want nil", tt.path, got)
				}
				return
			}
			if got == nil || got.MountPoint != tt.want {
				t.Fatalf("MountForPath(%q) = %+v, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestMountForPathPrefersRealMountOverAutofsPlaceholder(t *testing.T) {
	realMount := Mount{Device: "192.0.2.100:/", MountPoint: "/var/lib/libvirt/images", FSType: "ceph"}
	autofsMount := Mount{Device: "systemd-1", MountPoint: realMount.MountPoint, FSType: fsTypeAutofs}
	for _, tt := range []struct {
		name   string
		mounts []Mount
	}{
		{name: "autofs first", mounts: []Mount{autofsMount, realMount}},
		{name: "real first", mounts: []Mount{realMount, autofsMount}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := MountForPath(tt.mounts, "/var/lib/libvirt/images/base.qcow2")
			if got == nil || got.FSType != "ceph" {
				t.Fatalf("MountForPath = %+v, want ceph mount", got)
			}
		})
	}
}

func TestMountAtPathRequiresExactMountPoint(t *testing.T) {
	mounts := []Mount{
		{Device: "/dev/root", MountPoint: "/", FSType: "ext4"},
		{Device: "systemd-1", MountPoint: "/data", FSType: "autofs"},
		{Device: "/dev/data", MountPoint: "/data", FSType: "xfs"},
	}
	if got := MountAtPath(mounts, "/data/app"); got != nil {
		t.Fatalf("MountAtPath child = %+v, want nil", got)
	}
	got := MountAtPath(mounts, "/data")
	if got == nil || got.FSType != "xfs" {
		t.Fatalf("MountAtPath exact = %+v, want xfs", got)
	}
	if got := MountAtPath(mounts, "data"); got != nil {
		t.Fatalf("MountAtPath relative = %+v, want nil", got)
	}
}

// A declared severity grades a used/free ladder; a missing mount beside it is
// never demoted to an advisory. A mount-only check keeps its declaration.
func TestStorageMountFailureIsNeverAdvisoryBesideSpaceThresholds(t *testing.T) {
	tests := []struct {
		name  string
		preds []levelPred
		want  severity.Level
	}{
		{"space ladder", []levelPred{{"used_pct", ">=", 80}}, severity.Error},
		{"mount only", nil, severity.Warning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := storageCheck{
				name:         "fs",
				condition:    true,
				severity:     severity.Warning,
				path:         "/data",
				preds:        tt.preds,
				mount:        mountCond{active: true, expectMount: true},
				mountSampler: fakeMounts(),
			}
			res := c.Run(context.Background())
			if !res.OK || res.Severity.Resolved() != tt.want {
				t.Fatalf("result = ok %v severity %q, want failing at %q", res.OK, res.Severity, tt.want)
			}
		})
	}
}

// A hard network mount whose server is gone blocks statfs in the kernel. The
// check must answer unavailable within its timeout — graded as the outage a
// hung mount is — and later cycles must fail fast rather than stack another
// blocked call on the same mount.
func TestStorageHungStatfsIsBoundedAndNotStacked(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	c := storageCheck{
		name:      "fs",
		condition: true,
		severity:  severity.Warning,
		timeout:   20 * time.Millisecond,
		path:      "/hung-" + t.Name(),
		preds:     []levelPred{{"free_pct", "<", 20}},
		usage: func(string) (StorageStats, error) {
			calls.Add(1)
			<-release
			return StorageStats{FreePct: 50, UsedPct: 50}, nil
		},
		mountSampler: fakeMounts(),
	}
	first := c.Run(context.Background())
	if first.Observation() != ObservationUnavailable || !strings.Contains(first.Message, "no answer within") || first.Severity != severity.Error {
		t.Fatalf("first result = %+v, want unavailable at error after the timeout", first)
	}
	second := c.Run(context.Background())
	if !strings.Contains(second.Message, "still blocked") || calls.Load() != 1 {
		t.Fatalf("second result = %q after %d calls, want a fast failure without a new call", second.Message, calls.Load())
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		res := c.Run(context.Background())
		if res.Observation() == ObservationHealthy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the mount answered again but the check still reports %q", res.Message)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
