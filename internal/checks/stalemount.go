package checks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"sermo/internal/hostfs"
)

// mountDeviceGoneFunc reports whether the kernel no longer has the block device
// numbered deviceID ("major:minor"). Tests replace it; nil reads /sys/dev/block.
type mountDeviceGoneFunc func(deviceID string) bool

func defaultMountDeviceGone(deviceID string) bool { return blockDeviceGone(sysDevBlockPath, deviceID) }

// blockDeviceGone reports whether the block device numbered deviceID has left
// the kernel. A disk pulled while mounted — a USB drive unplugged, a hot-swap
// bay opened — keeps its filesystem in the mount table, and statfs keeps answering
// from the superblock the kernel holds; what goes is the device's sysfs entry.
// A device still listed but sized 0 stopped answering the same way (see
// blockDeviceMissing). As there, an unreadable entry is not a missing one, and
// a sysfs with no such tree at all (a restricted container) proves nothing.
func blockDeviceGone(root, deviceID string) bool {
	// Only a real major:minor pair becomes a path under root.
	if anonymousDeviceID(deviceID) {
		return false
	}
	entry := filepath.Join(root, deviceID)
	if _, err := hostfs.Lstat(entry); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return false
		}
		_, rootErr := hostfs.Lstat(root)
		return rootErr == nil
	}
	sectors, err := readProcUint(filepath.Join(entry, sysBlockSizeFile))
	return err == nil && sectors == 0
}

// anonymousDeviceID reports a device number that names no block device: the
// kernel numbers network, virtual and multi-device filesystems (NFS, tmpfs,
// btrfs, ZFS, overlay) under major 0. Anything that is not two decimal numbers
// is anonymous too, which is what lets a caller build a sysfs path from the rest.
func anonymousDeviceID(deviceID string) bool {
	major, minor, ok := strings.Cut(deviceID, ":")
	if !ok {
		return true
	}
	if n, err := strconv.ParseUint(major, 10, 32); err != nil || n == 0 {
		return true
	}
	_, err := strconv.ParseUint(minor, 10, 32)
	return err != nil
}

// mountStale reports whether m is still mounted from a block device that is
// gone. Only a mount that names a device node and carries a real device number
// is judged: a network, virtual or multi-device filesystem has no single block
// device whose absence would prove anything, and a filesystem on a device sysfs
// does not list as a block device (flash behind MTD) must not read as one that
// left. The verdict can drive a forced unmount, so what cannot be proven is
// left alone. Only the mount's table entry and sysfs are read, never the mount
// itself: the question neither triggers an automount nor waits on a dead
// filesystem.
func mountStale(m *Mount, gone mountDeviceGoneFunc) bool {
	if anonymousDeviceID(m.DeviceID) || !strings.HasPrefix(m.Device, devNodePrefix) {
		return false
	}
	if gone == nil {
		gone = defaultMountDeviceGone
	}
	return gone(m.DeviceID)
}

// staleMounts returns the mounts of table that are stale, asking sysfs once
// per device however many times it is mounted.
func staleMounts(table []Mount, gone mountDeviceGoneFunc) []Mount {
	if gone == nil {
		gone = defaultMountDeviceGone
	}
	verdicts := make(map[string]bool)
	once := func(deviceID string) bool {
		isGone, asked := verdicts[deviceID]
		if !asked {
			isGone = gone(deviceID)
			verdicts[deviceID] = isGone
		}
		return isGone
	}
	var stale []Mount
	for i := range table {
		if mountStale(&table[i], once) {
			stale = append(stale, table[i])
		}
	}
	return stale
}

// visibleMount returns the filesystem a path under m's mount point actually
// reaches: of the mounts stacked on that point, the last one listed. A disk
// that came back under another name is mounted over the stale mount of its
// predecessor, and it is the one on top that answers and that an unmount of
// the path would take.
func visibleMount(table []Mount, m *Mount) *Mount {
	for i := len(table) - 1; i >= 0; i-- {
		if table[i].MountPoint == m.MountPoint {
			return &table[i]
		}
	}
	return m
}

// staleMountsCheck counts and names the filesystems still mounted from a block
// device the kernel no longer has.
//
// It covers what a per-path check cannot. A storage watch must name its path and
// reads it, which keeps an automount alive; this one looks at every mount of the
// host and touches none. Condition-style — the `count` predicate (default `> 0`)
// decides — and without a remediation action of its own: which stale mount may
// be detached is a per-path decision, taken by a storage watch's `then.remount`.
type staleMountsCheck struct {
	base
	op      string
	value   float64
	sampler MountSamplerFunc
	gone    mountDeviceGoneFunc
}

func (c staleMountsCheck) Run(_ context.Context) Result {
	start := time.Now()
	table, err := samplerOr(c.sampler, DefaultMounts)()
	if err != nil {
		return c.unavailableResult(CheckTypeStaleMounts+": "+err.Error(), start)
	}
	// A check that cannot judge must not read as "no stale mounts": without
	// device numbers or without the sysfs tree every mount would pass.
	if !slices.ContainsFunc(table, func(m Mount) bool { return m.DeviceID != "" }) {
		return c.unavailableResult(CheckTypeStaleMounts+": the mount table carries no device numbers ("+procSelfMountInfo+" unreadable)", start)
	}
	if c.gone == nil {
		if _, err := hostfs.Lstat(sysDevBlockPath); err != nil {
			return c.unavailableResult(CheckTypeStaleMounts+": "+err.Error(), start)
		}
	}
	stale := staleMounts(table, c.gone)
	names := make([]string, len(stale))
	for i := range stale {
		names[i] = fmt.Sprintf("%s (%s on %s)", stale[i].MountPoint, stale[i].FSType, stale[i].Device)
	}
	return c.namedCountResult(names, c.op, c.value, "stale mount", DataKeyMounts, start)
}

func buildStaleMountsCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	op, value, errs := optionalCountPred(entry, StaleMountsPredFields, CheckTypeStaleMounts+" check")
	if errs != "" {
		return nil, errs
	}
	return staleMountsCheck{base: b, op: op, value: value, sampler: deps.MountSampler}, ""
}
