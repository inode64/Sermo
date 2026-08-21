package checks

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestBlockDeviceName(t *testing.T) {
	for _, tc := range []struct {
		name, device, want string
	}{
		{name: "bare kernel name", device: "sda", want: "sda"},
		{name: "dev node", device: "/dev/sda", want: "sda"},
		{name: "partition", device: "/dev/sda1", want: "sda1"},
		{name: "nvme namespace", device: "/dev/nvme0n1", want: "nvme0n1"},
		{name: "surrounding space", device: "  /dev/sdb  ", want: "sdb"},
		{name: "empty", device: "", want: ""},
		{name: "dev root only", device: "/dev/", want: ""},
		// Anything that is not a plain kernel name must be refused rather than
		// joined onto the sysfs root.
		{name: "by-id path", device: "/dev/disk/by-id/wwn-0x5", want: ""},
		{name: "device mapper path", device: "/dev/mapper/vg-lv", want: ""},
		{name: "traversal", device: "/dev/../../etc/passwd", want: ""},
		{name: "dot", device: ".", want: ""},
		{name: "parent", device: "..", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := blockDeviceName(tc.device); got != tc.want {
				t.Errorf("blockDeviceName(%q) = %q, want %q", tc.device, got, tc.want)
			}
		})
	}
}

func TestBlockDeviceMissing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sectors uint64
		err     error
		want    bool
	}{
		{name: "live disk", sectors: livingDeviceSectors},
		{name: "zero capacity", sectors: 0, want: true},
		{name: "no sysfs entry", err: fmt.Errorf("open: %w", fs.ErrNotExist), want: true},
		// An entry that exists but cannot be read says nothing about the disk,
		// so a hardened or partial sysfs must not invent a missing device.
		{name: "unreadable entry", err: errors.New("permission denied")},
		{name: "malformed entry", err: errors.New("malformed /sys/class/block/sda/size")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			size := func(string) (uint64, error) { return tc.sectors, tc.err }
			if got := blockDeviceMissing(size, "/dev/sda"); got != tc.want {
				t.Errorf("blockDeviceMissing = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDefaultBlockDeviceSizeRejectsNonDeviceName(t *testing.T) {
	if _, err := defaultBlockDeviceSize("/dev/disk/by-id/wwn-0x5"); err == nil {
		t.Error("a path that is not a kernel device name must not be turned into a sysfs read")
	}
}

// The paths are verbatim /sys/class/block link targets from a host carrying all
// four shapes, because the classification is only as good as the real spellings.
func TestBusFromDevicePath(t *testing.T) {
	for _, tc := range []struct {
		name, target, want string
	}{
		{
			name:   "sata disk",
			target: "../../devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda",
			want:   BusSATA,
		},
		{
			// The usb bridge emulates a scsi host and target: the outer bus wins,
			// or every USB disk would read as plain scsi.
			name:   "usb disk behind a scsi bridge",
			target: "../../devices/pci0000:00/0000:00:1c.2/0000:03:00.0/usb4/4-2/4-2:1.0/host6/target6:0:0/6:0:0:0/block/sdd",
			want:   BusUSB,
		},
		{
			name:   "nvme namespace",
			target: "../../devices/pci0000:00/0000:00:1d.4/0000:09:00.0/nvme/nvme0/nvme0n1",
			want:   BusNVMe,
		},
		{
			name:   "md array has no bus",
			target: "../../devices/virtual/block/md126",
			want:   BusVirtual,
		},
		{
			name:   "bare scsi",
			target: "../../devices/pci0000:00/0000:00:10.0/host2/target2:0:0/2:0:0:0/block/sdc",
			want:   BusSCSI,
		},
		{
			// A bare prefix match would read this as SATA.
			name:   "a component that merely starts with a bus name",
			target: "../../devices/platform/atacontroller-test/block/sde",
			want:   "",
		},
		{name: "empty", target: "", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := busFromDevicePath(tc.target); got != tc.want {
				t.Fatalf("busFromDevicePath() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A device sysfs does not describe reports no bus rather than a guessed one.
func TestDefaultBlockDeviceBusRejectsNonDeviceName(t *testing.T) {
	for _, device := range []string{"", "../../etc", "by-id/wwn-0x5000", "."} {
		if got := defaultBlockDeviceBus(device); got != "" {
			t.Errorf("defaultBlockDeviceBus(%q) = %q, want no answer", device, got)
		}
	}
}

func TestSysDeviceModel(t *testing.T) {
	for _, tc := range []struct {
		name, vendor, model, want string
	}{
		{"libata placeholder vendor is dropped", "ATA", "WDC WD20EFRX-68E", "WDC WD20EFRX-68E"},
		{"a real SCSI vendor names the drive", "SEAGATE", "ST4000NM0023", "SEAGATE ST4000NM0023"},
		{"nvme publishes the model alone", "", "Samsung SSD 980 500GB", "Samsung SSD 980 500GB"},
		{"a vendor with no model still identifies something", "SEAGATE", "", "SEAGATE"},
		{"nothing known reports nothing", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sysDeviceModel(tc.vendor, tc.model); got != tc.want {
				t.Errorf("sysDeviceModel(%q, %q) = %q, want %q", tc.vendor, tc.model, got, tc.want)
			}
		})
	}
}

func TestWithDeviceIdentityOmitsWhatIsNotKnown(t *testing.T) {
	data := withDeviceIdentity(map[string]any{DataKeyDevice: "/dev/sda"}, BlockDeviceIdentity{Model: "TEST DISK"})
	if data[DataKeyModel] != "TEST DISK" {
		t.Errorf("Data[%s] = %v, want the known model", DataKeyModel, data[DataKeyModel])
	}
	for _, key := range []string{DataKeySerialNumber, DataKeyFirmware, DataKeyWWN, DataKeyRotationRate, DataKeyCapacityBytes} {
		if _, ok := data[key]; ok {
			t.Errorf("Data[%s] is present, want an unknown field omitted rather than blank", key)
		}
	}
}

func TestSysDeviceRotation(t *testing.T) {
	dir := t.TempDir()
	if got := sysDeviceRotation(dir); got != "" {
		t.Errorf("rotation = %q, want nothing when the kernel publishes no flag", got)
	}
	for _, tc := range []struct{ flag, want string }{
		{"1", rotationRotational},
		{"0", rotationSolidState},
		// The flag is one byte plus a newline; trimming is what makes it match.
		{"1\n", rotationRotational},
	} {
		path := filepath.Join(dir, sysQueueRotationalFile)
		if err := os.WriteFile(path, []byte(tc.flag), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := sysDeviceRotation(dir); got != tc.want {
			t.Errorf("rotation for flag %q = %q, want %q", tc.flag, got, tc.want)
		}
	}
}
