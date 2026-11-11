package checks

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"sermo/internal/mounts"
	"sermo/internal/severity"
)

// The fixtures are what the kernel showed when a mounted USB disk was pulled:
// the mount stays in the table under its old device number, and the device's
// entry under /sys/dev/block is gone.
var (
	rootMount   = Mount{Device: "/dev/sda2", MountPoint: "/", FSType: "ext4", DeviceID: "8:2"}
	pulledMount = Mount{Device: "/dev/sdc1", MountPoint: "/misc/backup", FSType: "ext4", DeviceID: "8:33"}
	autofsMount = Mount{Device: "/etc/autofs/auto.misc", MountPoint: "/misc", FSType: FSTypeAutofs, DeviceID: "0:46"}
	nfsMount    = Mount{Device: "nas:/linux", MountPoint: "/net/nas", FSType: "nfs", DeviceID: "0:52"}
)

// goneDevices is a mountDeviceGoneFunc that reports the listed device numbers
// as gone and records every number it was asked about.
func goneDevices(asked *[]string, gone ...string) mountDeviceGoneFunc {
	return func(id string) bool {
		if asked != nil {
			*asked = append(*asked, id)
		}
		return slices.Contains(gone, id)
	}
}

func TestBlockDeviceGone(t *testing.T) {
	root := t.TempDir()
	sysfsEntry(t, root, "8:2", map[string]string{"size": "1953525168"})
	// A drive that stopped answering keeps its entry and is sized 0.
	sysfsEntry(t, root, "8:16", map[string]string{"size": "0"})
	// An entry whose size cannot be read is not a missing device.
	sysfsEntry(t, root, "8:48", nil)

	for id, want := range map[string]bool{"8:2": false, "8:16": true, "8:33": true, "8:48": false} {
		if got := blockDeviceGone(root, id); got != want {
			t.Errorf("blockDeviceGone(%q) = %v, want %v", id, got, want)
		}
	}
	// No such tree at all (a restricted container): nothing can be concluded.
	if blockDeviceGone(filepath.Join(root, "absent"), "8:33") {
		t.Error("a sysfs without /sys/dev/block must not report every device gone")
	}
	// A device number that is not one never becomes a path outside the tree.
	if blockDeviceGone(root, "../8:2") {
		t.Error("a malformed device number must not be judged gone")
	}
}

func TestAnonymousDeviceID(t *testing.T) {
	for id, want := range map[string]bool{"8:17": false, "253:4": false, "0:46": true, "": true, "garbage": true, "8:": true, "../8:2": true, "8:2/..": true, "+8:2": true} {
		if got := anonymousDeviceID(id); got != want {
			t.Errorf("anonymousDeviceID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestStaleMountsJudgesOnlyBlockBackedMounts(t *testing.T) {
	var asked []string
	// The disk is mounted twice (a bind mount): both mounts are stale, and sysfs
	// is consulted once for the device.
	bind := pulledMount
	bind.MountPoint = "/srv/backup"
	unknown := Mount{Device: "/dev/sdd1", MountPoint: "/mnt/x", FSType: "ext4"}
	// Flash behind MTD has a real device number and no block device in sysfs.
	flash := Mount{Device: "mtd0", MountPoint: "/flash", FSType: "jffs2", DeviceID: "31:0"}
	table := []Mount{rootMount, autofsMount, pulledMount, nfsMount, bind, unknown, flash}

	got := staleMounts(table, goneDevices(&asked, "8:33", "0:46", "0:52", "", "31:0"))
	if want := []Mount{pulledMount, bind}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stale = %+v, want %+v", got, want)
	}
	if want := []string{"8:2", "8:33"}; !reflect.DeepEqual(asked, want) {
		t.Fatalf("devices asked = %v, want %v (only a device node with a real device number is judged)", asked, want)
	}
}

// The device verdict is asked once and shared; whether a mount is judged at all
// is its own property, so an unjudged mount of a device listed first must not
// hide a judged one listed after it.
func TestStaleMountsJudgesEachMountOfADevice(t *testing.T) {
	var asked []string
	unnamed := Mount{Device: "none", MountPoint: "/a", FSType: "ext4", DeviceID: "8:33"}
	bind := pulledMount
	bind.MountPoint = "/b"
	got := staleMounts([]Mount{unnamed, pulledMount, bind}, goneDevices(&asked, "8:33"))
	if want := []Mount{pulledMount, bind}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stale = %+v, want %+v", got, want)
	}
	if want := []string{"8:33"}; !reflect.DeepEqual(asked, want) {
		t.Fatalf("devices asked = %v, want %v", asked, want)
	}
}

func TestAttachDeviceIDsPairsStackedMountsInOrder(t *testing.T) {
	table := []Mount{
		{MountPoint: "/"},
		{MountPoint: "/misc/backup", FSType: FSTypeAutofs},
		{MountPoint: "/misc/backup", FSType: "ext4"},
		{MountPoint: "/mnt/late"},
	}
	attachDeviceIDs(table, []mounts.MountInfo{
		{MountPoint: "/", DeviceID: "8:2"},
		{MountPoint: "/misc/backup", DeviceID: "0:46"},
		{MountPoint: "/misc/backup", DeviceID: "8:33"},
	})
	got := make([]string, len(table))
	for i := range table {
		got[i] = table[i].DeviceID
	}
	// A mount that appeared between the two reads keeps no number.
	if want := []string{"8:2", "0:46", "8:33", ""}; !reflect.DeepEqual(got, want) {
		t.Fatalf("device IDs = %v, want %v", got, want)
	}
}

func TestStaleMountsCheckRun(t *testing.T) {
	tests := []struct {
		name            string
		table           []Mount
		err             error
		gone            []string
		wantOK          bool
		wantUnavailable bool
		wantMsg         string
		wantData        map[string]any
	}{
		{
			name: "every device is there", table: []Mount{rootMount, autofsMount, pulledMount},
			wantMsg: "no stale mounts", wantData: map[string]any{DataKeyCount: uint64(0), DataKeyValue: uint64(0)},
		},
		{
			// The condition is what fires, so OK == true is the alarm here.
			name: "a pulled disk is named", table: []Mount{rootMount, autofsMount, pulledMount}, gone: []string{"8:33"},
			wantOK: true, wantMsg: "1 stale mount(s): /misc/backup (ext4 on /dev/sdc1)",
			wantData: map[string]any{DataKeyCount: uint64(1), DataKeyValue: uint64(1), DataKeyMounts: "/misc/backup (ext4 on /dev/sdc1)"},
		},
		{
			name: "unreadable mount table", err: errors.New("read /proc/mounts: permission denied"),
			wantUnavailable: true, wantMsg: "permission denied",
		},
		{
			// Without device numbers nothing can be judged: that is not "no stale mounts".
			name:            "mount table without device numbers",
			table:           []Mount{{Device: "/dev/sda2", MountPoint: "/", FSType: "ext4"}, {Device: "/dev/sdc1", MountPoint: "/misc/backup", FSType: "ext4"}},
			wantUnavailable: true, wantMsg: "no device numbers",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			check := staleMountsCheck{
				name: "s", timeout: time.Second, op: ">", value: 0,
				sampler: func() ([]Mount, error) { return tc.table, tc.err },
				gone:    goneDevices(nil, tc.gone...),
			}
			res := check.Run(context.Background())
			if res.OK != tc.wantOK || res.Unavailable != tc.wantUnavailable {
				t.Fatalf("OK = %v, Unavailable = %v; want %v, %v (%s)", res.OK, res.Unavailable, tc.wantOK, tc.wantUnavailable, res.Message)
			}
			if !strings.Contains(res.Message, tc.wantMsg) {
				t.Fatalf("message = %q, want substring %q", res.Message, tc.wantMsg)
			}
			if tc.wantData != nil && !reflect.DeepEqual(res.Data, tc.wantData) {
				t.Fatalf("data = %#v\nwant   %#v", res.Data, tc.wantData)
			}
		})
	}
}

func TestBuildStaleMountsCheck(t *testing.T) {
	check, errs := buildStaleMountsCheck(base{name: "w"}, map[string]any{}, Deps{})
	if built, ok := check.(staleMountsCheck); errs != "" || !ok || built.op != ">" || built.value != 0 {
		t.Fatalf("default = %+v, %q; want it to fire on any stale mount", check, errs)
	}
	check, errs = buildStaleMountsCheck(base{name: "w"}, map[string]any{CheckKeyCount: map[string]any{CheckKeyOp: ">=", CheckKeyValue: 2}}, Deps{})
	if built, ok := check.(staleMountsCheck); errs != "" || !ok || built.op != ">=" || built.value != 2 {
		t.Fatalf("explicit count = %+v, %q", check, errs)
	}
	if _, errs = buildStaleMountsCheck(base{name: "w"}, map[string]any{CheckKeyCount: "many"}, Deps{}); !strings.Contains(errs, "count") {
		t.Fatalf("errs = %q, want the malformed count rejected", errs)
	}
}

// A filesystem whose disk was pulled keeps answering statfs from the superblock
// the kernel holds. The storage check must fail on the mount, as it does for a
// missing or hung one, instead of grading those numbers — with or without
// `mounted`, and before statfs is consulted at all.
func TestStorageStaleMountFailsBeforeStatfs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mount mountCond
		preds []levelPred
		want  severity.Level
	}{
		{"mount only", mountCond{active: true, expectMount: true}, nil, severity.Warning},
		{"mount and space", mountCond{active: true, expectMount: true}, []levelPred{{"free_pct", "<", 20}}, severity.Error},
		{"space only", mountCond{expectMount: true}, []levelPred{{"free_pct", "<", 20}}, severity.Error},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := storageCheck{
				name: "fs", condition: true, severity: severity.Warning, timeout: time.Second,
				path: pulledMount.MountPoint, preds: tc.preds, mount: tc.mount,
				mountSampler: fakeMounts(rootMount, autofsMount, pulledMount),
				deviceGone:   goneDevices(nil, "8:33"),
				usage: func(string) (StorageStats, error) {
					t.Error("statfs consulted for a stale mount")
					return StorageStats{}, nil
				},
			}
			res := c.Run(context.Background())
			if !res.OK || res.Observation() == ObservationUnavailable || res.Data[DataKeyMountFailure] != MountFailureStale || res.Severity.Resolved() != tc.want {
				t.Fatalf("result = ok %v observation %v data %v severity %q", res.OK, res.Observation(), res.Data, res.Severity)
			}
			if want := "/misc/backup is mounted from /dev/sdc1, a device the kernel no longer has"; res.Message != want {
				t.Fatalf("message = %q, want %q", res.Message, want)
			}
		})
	}
}

// The replacement disk, back under another name, is mounted over the stale
// mount of its predecessor. The path now answers from the disk on top, which is
// also what an unmount of the path would take: the check must judge that one.
func TestStorageJudgesTheVisibleMountOfAStack(t *testing.T) {
	replacement := Mount{Device: "/dev/sde1", MountPoint: pulledMount.MountPoint, FSType: "ext4", DeviceID: "8:65"}
	c := storageCheck{
		name: "fs", condition: true, timeout: time.Second,
		path: pulledMount.MountPoint, mount: mountCond{active: true, expectMount: true},
		preds:        []levelPred{{"free_pct", "<", 20}},
		mountSampler: fakeMounts(rootMount, pulledMount, replacement),
		deviceGone:   goneDevices(nil, "8:33"),
		usage:        fakeStorage(10, 90, 900, 1000),
	}
	res := c.Run(context.Background())
	if res.OK || res.Data[DataKeyMountFailure] != nil {
		t.Fatalf("result = ok %v data %v, want the live mount on top graded as healthy", res.OK, res.Data)
	}

	// The other way round — the stale mount on top — is what the path reaches.
	c.mountSampler = fakeMounts(rootMount, replacement, pulledMount)
	if res = c.Run(context.Background()); res.Data[DataKeyMountFailure] != MountFailureStale {
		t.Fatalf("data = %v, want the stale mount on top reported", res.Data)
	}
}

// The same mount with its device present is an ordinary healthy mount.
func TestStorageLiveMountIsNotStale(t *testing.T) {
	c := storageCheck{
		name: "fs", condition: true, timeout: time.Second,
		path: pulledMount.MountPoint, mount: mountCond{active: true, expectMount: true},
		mountSampler: fakeMounts(rootMount, autofsMount, pulledMount),
		deviceGone:   goneDevices(nil),
		usage:        fakeStorage(10, 90, 900, 1000),
	}
	res := c.Run(context.Background())
	if res.OK || res.Data[DataKeyMountFailure] != nil {
		t.Fatalf("result = ok %v data %v, want a healthy mount", res.OK, res.Data)
	}
}
