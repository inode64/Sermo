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

// Block device transports, as the kernel's own device path spells them. They
// explain behaviour the numbers alone do not: a USB disk parks itself and answers
// a benchmark with its own spin-up, and a virtual device has no bus at all.
const (
	BusSATA    = "sata"
	BusUSB     = "usb"
	BusNVMe    = "nvme"
	BusSCSI    = "scsi"
	BusVirtio  = "virtio"
	BusMMC     = "mmc"
	BusVirtual = "virtual"
)

// BlockDeviceBusFunc reports the transport a block device sits on, or "" when
// the kernel does not say. Injected for tests; the default reads sysfs.
type BlockDeviceBusFunc func(device string) string

// busMarkers maps a device-path component to the transport it proves, in the
// order they must be tested: a USB disk's path carries the scsi host and target
// its bridge emulates, so the outer bus has to win over the inner one.
var busMarkers = []struct {
	prefix string
	bus    string
}{
	{"virtual", BusVirtual},
	{"nvme", BusNVMe},
	{"usb", BusUSB},
	{"ata", BusSATA},
	{"virtio", BusVirtio},
	{"mmc", BusMMC},
	{"host", BusSCSI},
}

// defaultBlockDeviceBus classifies a device by the sysfs path the kernel links
// it to. The link target is read, never followed: it already names every bus the
// device hangs off, and reading it touches one entry rather than walking the
// tree. An unknown or unreadable device reports "", which every caller renders
// as no answer rather than inventing one.
func defaultBlockDeviceBus(device string) string {
	name := blockDeviceName(device)
	if name == "" {
		return ""
	}
	target, err := os.Readlink(filepath.Join(sysBlockPath, name))
	if err != nil {
		return ""
	}
	return busFromDevicePath(target)
}

// busFromDevicePath names the transport a kernel device path proves. Order is
// the whole design: a USB disk's path also carries the scsi host and target its
// bridge emulates, so the outer bus is tested before the inner one.
func busFromDevicePath(target string) string {
	for _, marker := range busMarkers {
		for part := range strings.SplitSeq(target, string(filepath.Separator)) {
			if busComponent(part, marker.prefix) {
				return marker.bus
			}
		}
	}
	return ""
}

// withDeviceBus records the transport a device sits on alongside its readings.
// It is a property of the device rather than of the sample, so it is resolved
// here — one readlink per cycle — instead of on every dashboard poll.
func withDeviceBus(data map[string]any, bus BlockDeviceBusFunc, device string) map[string]any {
	resolve := bus
	if resolve == nil {
		resolve = defaultBlockDeviceBus
	}
	if name := resolve(device); name != "" {
		data[DataKeyBus] = name
	}
	return data
}

// busComponent reports whether one device-path component names this bus: the
// bare word ("nvme", "virtual") or the word with its instance number ("usb4",
// "ata1"). Matching a bare prefix would let "atatest" pass for SATA.
func busComponent(part, prefix string) bool {
	rest, ok := strings.CutPrefix(part, prefix)
	if !ok {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

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
