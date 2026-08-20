package checks

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// devNodePrefix is the /dev path prefix a configured device may carry: the smart
// and hdparm checks address "/dev/sda" while diskio addresses the bare kernel
// name "sda". Both reduce to the same sysfs entry.
const devNodePrefix = "/dev/"

// sysBlockSizeFile holds a block device's capacity in 512-byte sectors.
const sysBlockSizeFile = "size"

// deviceMissingReason is the message every device-addressing check reports when
// the kernel stops backing its device. One wording keeps the dashboard summary,
// the event log and the notifier text identical across check types.
const deviceMissingReason = "device " + DeviceStateMissing

// BlockDeviceSizeFunc reports a block device's capacity in 512-byte sectors, as
// the kernel currently sees it. It returns a wrapped fs.ErrNotExist when the
// kernel exposes no such device at all.
type BlockDeviceSizeFunc func(device string) (uint64, error)

// blockDeviceMissing reports whether the kernel no longer backs device with any
// capacity. Presence of the /dev node proves nothing: a disk that drops off its
// bus keeps both its node and its /proc/diskstats row, and only sysfs drops the
// capacity to 0. An unreadable — as opposed to absent — sysfs entry is
// deliberately not treated as missing: callers use this to label a failure they
// already hold, and a hardened or partial sysfs must not invent a dead disk.
func blockDeviceMissing(size BlockDeviceSizeFunc, device string) bool {
	sectors, err := keyedSamplerOr(size, defaultBlockDeviceSize)(device)
	if err != nil {
		return errors.Is(err, fs.ErrNotExist)
	}
	return sectors == 0
}

// defaultBlockDeviceSize reads the capacity sysfs publishes for one whole-disk
// or partition block device. Both shapes live directly under /sys/class/block,
// so partitions need no special case.
func defaultBlockDeviceSize(device string) (uint64, error) {
	name := blockDeviceName(device)
	if name == "" {
		return 0, fmt.Errorf("block device %q: not a kernel device name", device)
	}
	path := filepath.Join(sysBlockPath, name, sysBlockSizeFile)
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is sysBlockPath joined with a validated kernel device name
	if err != nil {
		// Wrapped, not replaced: blockDeviceMissing tells an absent device from
		// an unreadable one through errors.Is(err, fs.ErrNotExist).
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	sectors, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf(malformedFileFormat, path)
	}
	return sectors, nil
}

// blockDeviceName reduces a configured device to its bare kernel name. Anything
// that is not a plain name — a by-id or device-mapper path, a traversal — is
// rejected rather than turned into a sysfs path, so a config value can never
// steer the read outside /sys/class/block.
func blockDeviceName(device string) string {
	name := strings.TrimPrefix(strings.TrimSpace(device), devNodePrefix)
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, filepath.Separator) {
		return ""
	}
	return name
}

// missingDeviceResult is the unavailable observation a device-addressing check
// publishes once it establishes that its device is gone. The state travels as
// reading data, not only in the message, so the dashboard shows "missing" in the
// health and state columns instead of leaving them blank.
func (b base) missingDeviceResult(prefix, device string, start time.Time) Result {
	res := b.unavailableResult(prefix+": "+deviceMissingReason, start)
	res.Data = MissingDeviceResultData(device)
	return res
}

// deviceFailureResult labels one failed device probe. The same failure reads as
// "missing" when sysfs no longer sizes the device and keeps the tool's own
// message when the device is still there, so a dead disk never reports as two
// unrelated faults across the checks that address it.
func (b base) deviceFailureResult(size BlockDeviceSizeFunc, prefix, device, reason string, start time.Time) Result {
	if blockDeviceMissing(size, device) {
		return b.missingDeviceResult(prefix, device, start)
	}
	return b.unavailableResult(prefix+": "+reason, start)
}

// MissingDeviceResultData is the persisted reading data for a device that no
// longer answers, shared by the check cycle and the snapshot-backed watch view.
func MissingDeviceResultData(device string) map[string]any {
	return map[string]any{
		DataKeyDevice:      device,
		DataKeyHealth:      DeviceStateMissing,
		DataKeyDeviceState: DeviceStateMissing,
	}
}
