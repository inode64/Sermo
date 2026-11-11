package mounts

import "strings"

const (
	mountInfoDeviceIndex = 2
	mountInfoPointIndex  = 4
	mountInfoMinFields   = mountInfoPointIndex + 1
)

// MountInfo is one line of /proc/<pid>/mountinfo: where a filesystem sits and
// which one it is.
type MountInfo struct {
	MountPoint string
	// DeviceID is the filesystem's device number as "major:minor".
	DeviceID string
}

// ParseMountInfo reads /proc/<pid>/mountinfo. Optional fields only start after
// the mount options, so the leading columns have fixed positions.
func ParseMountInfo(data []byte) []MountInfo {
	var table []MountInfo
	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) < mountInfoMinFields {
			continue
		}
		table = append(table, MountInfo{
			MountPoint: UnescapeField(fields[mountInfoPointIndex]),
			DeviceID:   fields[mountInfoDeviceIndex],
		})
	}
	return table
}
