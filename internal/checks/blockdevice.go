package checks

import (
	"cmp"
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

// blockSectorBytes is the unit both /sys/class/block/<device>/size and
// /proc/diskstats count in, whatever the device's real sector size.
const blockSectorBytes = 512

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
func (b base) missingDeviceResult(identity BlockDeviceIdentityFunc, prefix, device string, start time.Time) Result {
	res := b.unavailableResult(prefix+": "+deviceMissingReason, start)
	res.Data = withDeviceIdentity(MissingDeviceResultData(device), resolveDeviceIdentity(identity, device))
	return res
}

// deviceFailureResult labels one failed device probe. The same failure reads as
// "missing" when sysfs no longer sizes the device and keeps the tool's own
// message when the device is still there, so a dead disk never reports as two
// unrelated faults across the checks that address it.
func (b base) deviceFailureResult(probe deviceProbe, prefix, device, reason string, start time.Time) Result {
	if blockDeviceMissing(probe.size, device) {
		return b.missingDeviceResult(probe.identity, prefix, device, start)
	}
	res := b.unavailableResult(prefix+": "+reason, start)
	res.Data = withDeviceIdentity(map[string]any{DataKeyDevice: device}, resolveDeviceIdentity(probe.identity, device))
	return res
}

// deviceProbe is the pair of sysfs readers every device-addressing check needs
// when its own tool failed: one to decide whether the device is still there, one
// to say what it is. They travel together because they answer the same question
// — which disk is this, and is it gone — and are injected together in tests.
type deviceProbe struct {
	size     BlockDeviceSizeFunc
	identity BlockDeviceIdentityFunc
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

// sysfs attribute files under /sys/class/block/<device>/device that name the
// hardware. The kernel fills them from the last successful IDENTIFY/INQUIRY and
// keeps them after the drive stops answering, which is exactly when a check has
// no report of its own to identify the disk with.
const (
	sysDeviceDir         = "device"
	sysDeviceVendorFile  = "vendor"
	sysDeviceModelFile   = "model"
	sysDeviceSerialFile  = "serial"
	sysDeviceRevFile     = "rev"
	sysDeviceFirmwareRev = "firmware_rev"
	sysDeviceWWIDFile    = "wwid"
	// sysDeviceATAVendor is what the SCSI layer writes as the vendor of every
	// SATA disk behind libata. It names the transport, not the manufacturer, so
	// it must not be glued in front of the model.
	sysDeviceATAVendor = "ATA"
	// sysQueueDir/sysQueueRotationalFile is the kernel's own answer to "does
	// this drive have platters", as a bare 0/1. It sits under the block device
	// rather than under its `device` link, and it survives the drive going
	// quiet exactly as the identification attributes do.
	sysQueueDir             = "queue"
	sysQueueRotationalFile  = "rotational"
	sysQueueRotationalSpins = "1"
)

// Rotation-rate wordings, in smartctl's own vocabulary so one drive reads the
// same whichever source described it. sysfs only knows *whether* a drive spins,
// so a platter drive it described carries no rpm figure; smartctl reports the
// exact rate and is preferred whenever it answered.
const (
	rotationSolidState = "SSD"
	rotationRotational = "rotational"
	rotationRPMFormat  = "%d rpm"
)

// BlockDeviceIdentity is what a block device *is*, as opposed to what it last
// measured: the fields that identify the physical drive an operator has to pull
// out of a bay. Every field is optional — sysfs and smartctl each publish a
// different subset, and a device that never answered publishes almost none.
type BlockDeviceIdentity struct {
	Model    string
	Serial   string
	Firmware string
	WWN      string
	// Rotation says what kind of drive this is — "7200 rpm", "rotational" or
	// "SSD" — which is what makes the rest of the readings mean something: wear
	// is the number that matters on flash, reallocated sectors on platters.
	Rotation string
	// CapacityBytes is the drive's total size, 0 when unknown. A device that
	// fell off its bus is sized 0 by sysfs, which is why capacity is the one
	// identity field a dead drive stops publishing.
	CapacityBytes uint64
}

// BlockDeviceIdentityFunc reports the hardware identity the kernel holds for a
// block device. Injected for tests; the default reads sysfs.
type BlockDeviceIdentityFunc func(device string) BlockDeviceIdentity

// defaultBlockDeviceIdentity reads the identity sysfs publishes for one block
// device. SCSI/SATA disks spell their firmware revision `rev` and carry no
// serial at all (the WWN is their only unique id); NVMe disks spell it
// `firmware_rev` and do publish a serial. Reading both spellings covers every
// transport with one pass and no device-type branching.
func defaultBlockDeviceIdentity(device string) BlockDeviceIdentity {
	name := blockDeviceName(device)
	if name == "" {
		return BlockDeviceIdentity{}
	}
	dir := filepath.Join(sysBlockPath, name, sysDeviceDir)
	identity := BlockDeviceIdentity{
		Model:    sysDeviceModel(sysDeviceAttr(dir, sysDeviceVendorFile), sysDeviceAttr(dir, sysDeviceModelFile)),
		Serial:   sysDeviceAttr(dir, sysDeviceSerialFile),
		Firmware: cmp.Or(sysDeviceAttr(dir, sysDeviceFirmwareRev), sysDeviceAttr(dir, sysDeviceRevFile)),
		WWN:      sysDeviceAttr(dir, sysDeviceWWIDFile),
		Rotation: sysDeviceRotation(filepath.Join(sysBlockPath, name, sysQueueDir)),
	}
	if sectors, err := defaultBlockDeviceSize(device); err == nil {
		identity.CapacityBytes = sectors * blockSectorBytes
	}
	return identity
}

// resolveDeviceIdentity returns the check's injected identity reader, falling
// back to sysfs, in the shape every device-addressing check uses it: one lookup
// for one device.
func resolveDeviceIdentity(identity BlockDeviceIdentityFunc, device string) BlockDeviceIdentity {
	if identity == nil {
		identity = defaultBlockDeviceIdentity
	}
	return identity(device)
}

// sysDeviceModel joins the two halves sysfs splits a drive's name into. The
// vendor is dropped when it is libata's placeholder, so a SATA disk reads
// "WDC WD20EFRX-68E" rather than "ATA WDC WD20EFRX-68E".
func sysDeviceModel(vendor, model string) string {
	if vendor == "" || vendor == sysDeviceATAVendor || model == "" {
		return cmp.Or(model, vendor)
	}
	return vendor + " " + model
}

// sysDeviceRotation reads the kernel's rotational flag, which answers only
// whether the drive has platters. A drive that does gets no rpm figure here:
// the flag does not carry one, and inventing a rate would be worse than saying
// only what is known.
func sysDeviceRotation(queueDir string) string {
	switch sysDeviceAttr(queueDir, sysQueueRotationalFile) {
	case sysQueueRotationalSpins:
		return rotationRotational
	case "":
		return ""
	default:
		return rotationSolidState
	}
}

// sysDeviceAttr reads one sysfs device attribute, or "" when it is absent or
// unreadable. Absence is the normal case: each transport publishes its own
// subset, so a missing file is never an error worth reporting.
func sysDeviceAttr(dir, file string) string {
	data, err := os.ReadFile(filepath.Join(dir, file)) //nolint:gosec // G304: sysBlockPath joined with a validated kernel device name
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// withDeviceIdentity records what the kernel says the device is alongside its
// readings. Identity is a property of the hardware rather than of the sample, so
// it stays true — and stays worth publishing — when the drive stops answering.
func withDeviceIdentity(data map[string]any, identity BlockDeviceIdentity) map[string]any {
	if data == nil {
		data = map[string]any{}
	}
	for _, field := range [...]struct{ key, value string }{
		{DataKeyModel, identity.Model},
		{DataKeySerialNumber, identity.Serial},
		{DataKeyFirmware, identity.Firmware},
		{DataKeyWWN, identity.WWN},
		{DataKeyRotationRate, identity.Rotation},
	} {
		if field.value != "" {
			data[field.key] = field.value
		}
	}
	if identity.CapacityBytes > 0 {
		data[DataKeyCapacityBytes] = identity.CapacityBytes
	}
	return data
}
