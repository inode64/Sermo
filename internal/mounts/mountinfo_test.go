package mounts

import (
	"reflect"
	"testing"
)

func TestParseMountInfo(t *testing.T) {
	data := "36 25 8:17 / /mnt/my\\040data rw - ext4 /dev/sdb1 rw\n" +
		// Optional fields sit between the options and the separator.
		"129 100 8:65 / /misc/backup rw,relatime shared:454 master:2 - ext4 /dev/sde1 rw,errors=remount-ro\n" +
		"100 22 0:46 / /misc rw,relatime shared:93 - autofs /etc/autofs/auto.misc rw,fd=6\n" +
		// The leading columns are all that is read.
		"40 25 8:3 / /srv rw\n" +
		"short line\n"
	want := []MountInfo{
		{MountPoint: "/mnt/my data", DeviceID: "8:17"},
		{MountPoint: "/misc/backup", DeviceID: "8:65"},
		{MountPoint: "/misc", DeviceID: "0:46"},
		{MountPoint: "/srv", DeviceID: "8:3"},
	}
	if got := ParseMountInfo([]byte(data)); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseMountInfo =\n%+v\nwant\n%+v", got, want)
	}
}
