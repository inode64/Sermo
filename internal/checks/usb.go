package checks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/hostfs"
)

// sysfs attribute files of one USB device directory.
const (
	sysUSBVendorIDFile     = "idVendor"
	sysUSBProductIDFile    = "idProduct"
	sysUSBClassFile        = "bDeviceClass"
	sysUSBManufacturerFile = "manufacturer"
	sysUSBProductFile      = "product"
	// sysUSBHubClass is the device class of every hub, the root hub of each
	// controller included. A hub is plumbing, not a device an operator plugs in
	// to use, so it is never a candidate.
	sysUSBHubClass = "09"
	// sysUSBInterfaceMark separates a device name from its configuration and
	// interface ("3-2:1.0"). Those entries describe a function of a device
	// already listed under its own name.
	sysUSBInterfaceMark = ":"

	usbIDSeparator = ":"
)

// usbDeviceIDPattern is a vendor and product pair as sysfs and lsusb spell it.
var usbDeviceIDPattern = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{4}$`)

// usbDevice is one device on the USB bus, as sysfs describes it.
type usbDevice struct {
	// Port is the kernel's name for where the device is plugged in: bus, then
	// the port on each hub down to it ("3-2", "3-4.1").
	Port string
	// ID is the vendor and product pair in lsusb's spelling, "0480:a00c".
	ID           string
	Manufacturer string
	Product      string
	Serial       string
}

// label names a device the way an operator recognizes it: its ID and whatever
// the device says it is.
func (d usbDevice) label() string {
	if name := d.name(); name != "" {
		return d.ID + " " + name
	}
	return d.ID
}

// name joins what the device calls itself. Many products already begin with
// their maker's name, which is then not said twice.
func (d usbDevice) name() string {
	if strings.HasPrefix(d.Product, d.Manufacturer) {
		return d.Product
	}
	return strings.TrimSpace(d.Manufacturer + " " + d.Product)
}

// identityData publishes what the device is under the keys every
// device-addressing check uses.
func (d usbDevice) identityData() ([]identityField, []identityNumber) {
	return []identityField{
		{DataKeyModel, d.name()},
		{DataKeySerialNumber, d.Serial},
	}, nil
}

// usbSamplerFunc lists the USB devices currently connected, hubs excluded. It
// returns a wrapped fs.ErrNotExist when the host exposes no USB bus. Tests
// replace it; nil reads /sys/bus/usb/devices.
type usbSamplerFunc func() ([]usbDevice, error)

// usbCheck asserts that a USB device the host depends on is connected: a backup
// disk, a UPS, a modem, a licence dongle. It is a health check (OK==true is
// healthy), so as a watch it fires while the device is unplugged. Selection is
// by what the device is — its vendor:product ID, its serial number, or both —
// never by port or kernel name, which change with the socket it is plugged into.
type usbCheck struct {
	base
	id      string
	serial  string
	sampler usbSamplerFunc
}

func (c usbCheck) Run(_ context.Context) Result {
	start := time.Now()
	devices, err := samplerOr(c.sampler, defaultUSBSampler)()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return c.unavailableResult(CheckTypeUSB+": this host exposes no USB bus", start)
		}
		return c.unavailableResult(CheckTypeUSB+": "+err.Error(), start)
	}
	var matched []usbDevice
	for _, d := range devices {
		if c.matches(d) {
			matched = append(matched, d)
		}
	}
	data := map[string]any{DataKeyCount: len(devices), DataKeyValue: len(matched)}
	if len(matched) == 0 {
		// What is plugged in instead is the first thing the operator needs: a
		// different disk of the rotation, or nothing at all.
		others := make([]string, len(devices))
		for i, d := range devices {
			others[i] = d.label()
		}
		if len(others) > 0 {
			data[DataKeyConnected] = strings.Join(others, ", ")
		}
		res := c.result(false, fmt.Sprintf("no USB device %s connected (%d other device(s))", c.wanted(), len(devices)), start)
		res.Data = data
		return res
	}
	found := matched[0]
	data[DataKeyDevice], data[DataKeyBusPort] = found.ID, found.Port
	res := c.result(true, fmt.Sprintf("USB device %s connected at %s", found.label(), found.Port), start)
	res.Data = withIdentity(data, found)
	return res
}

func (c usbCheck) matches(d usbDevice) bool {
	return (c.id == "" || d.ID == c.id) && (c.serial == "" || d.Serial == c.serial)
}

// wanted spells the configured selection for the failure message.
func (c usbCheck) wanted() string {
	switch {
	case c.id == "":
		return "with serial " + c.serial
	case c.serial == "":
		return c.id
	default:
		return c.id + " with serial " + c.serial
	}
}

func buildUSBCheck(b base, entry map[string]any) (Check, string) {
	id := cfgval.AsString(entry[CheckKeyDevice])
	// sysfs attributes are compared trimmed, so the configured serial is too.
	serial := strings.TrimSpace(cfgval.AsString(entry[CheckKeySerialNumber]))
	if id == "" && serial == "" {
		return nil, CheckTypeUSB + " check requires device (vendor:product) and/or serial_number"
	}
	if id != "" {
		normalized, ok := ParseUSBDeviceID(id)
		if !ok {
			return nil, CheckTypeUSB + " check device must be " + USBDeviceIDSummary
		}
		id = normalized
	}
	return usbCheck{base: b, id: id, serial: serial}, ""
}

// USBDeviceIDSummary is the user-facing description of a usb check's `device`,
// shared with config validation.
const USBDeviceIDSummary = "a vendor:product ID as lsusb prints it (e.g. 0480:a00c)"

// ParseUSBDeviceID normalizes a vendor:product ID to the lower-case spelling
// sysfs uses. It reports false for anything that is not two groups of four
// hexadecimal digits.
func ParseUSBDeviceID(id string) (string, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	return id, usbDeviceIDPattern.MatchString(id)
}

func defaultUSBSampler() ([]usbDevice, error) { return readUSBDevices(sysUSBDevicesPath) }

// readUSBDevices lists the devices under one sysfs USB device directory. An
// entry that publishes no vendor ID is not a device (or is one mid-removal) and
// is skipped rather than reported half-described.
func readUSBDevices(root string) ([]usbDevice, error) {
	entries, err := hostfs.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("list USB devices: %w", err)
	}
	var devices []usbDevice
	for _, entry := range entries {
		port := entry.Name()
		if strings.Contains(port, sysUSBInterfaceMark) {
			continue
		}
		dir := filepath.Join(root, port)
		vendor := sysDeviceAttr(dir, sysUSBVendorIDFile)
		if vendor == "" || sysDeviceAttr(dir, sysUSBClassFile) == sysUSBHubClass {
			continue
		}
		devices = append(devices, usbDevice{
			Port:         port,
			ID:           strings.ToLower(vendor + usbIDSeparator + sysDeviceAttr(dir, sysUSBProductIDFile)),
			Manufacturer: sysDeviceAttr(dir, sysUSBManufacturerFile),
			Product:      sysDeviceAttr(dir, sysUSBProductFile),
			Serial:       sysDeviceAttr(dir, sysDeviceSerialFile),
		})
	}
	return devices, nil
}
