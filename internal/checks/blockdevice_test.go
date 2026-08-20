package checks

import (
	"errors"
	"fmt"
	"io/fs"
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
