package checks

import (
	"fmt"
	"path/filepath"
	"sermo/internal/hostfs"
	"strings"

	"sermo/internal/mounts"
)

// FSTypeAutofs identifies autofs placeholder mounts in the mount table.
const FSTypeAutofs = "autofs"

const (
	procMountsMinFields       = 4
	procMountsDeviceIndex     = 0
	procMountsMountPointIndex = 1
	procMountsFSTypeIndex     = 2
	procMountsOptionsIndex    = 3
	procMountsOptionsSep      = ","
)

// Mount is one entry of the mount table.
type Mount struct {
	Device     string
	MountPoint string
	FSType     string
	Options    []string
	// DeviceID is the filesystem's device number, "major:minor", which
	// identifies the block device it sits on whatever the mount source spells.
	// Empty when the sampler does not know it.
	DeviceID string
}

// MountSamplerFunc returns the current mount table. Injected for tests; the
// default reads /proc/mounts.
type MountSamplerFunc func() ([]Mount, error)

// mountCond are the optional mount expectations folded into a storage check, so a
// filesystem's mount and its space are checked from one entry (no duplicated
// path). active is true when any condition was configured.
type mountCond struct {
	active      bool
	expectMount bool // require mounted; when false, require NOT mounted
}

// parseMountCond reads the mount expectation from a storage check entry. Only the
// `mounted` predicate is configurable; filesystem type, source device and
// options are reported as data but do not control the check.
func parseMountCond(entry map[string]any) mountCond {
	m := mountCond{expectMount: true}
	if v, ok := entry[CheckKeyMounted].(bool); ok {
		m.active, m.expectMount = true, v
	}
	return m
}

// evaluate returns the reason a mount expectation is violated, or an empty
// reason when satisfied. info is the matching mount entry, nil when unmounted.
func (m mountCond) evaluate(table []Mount, path string) (reason string, info *Mount) {
	info = MountAtPath(table, path)
	if !m.expectMount && info != nil {
		return "is mounted (want unmounted)", info
	}
	if m.expectMount && info == nil {
		return "is not mounted", info
	}
	return "", info
}

// DefaultMounts reads the mount table from /proc/mounts, with each
// filesystem's device number from /proc/self/mountinfo.
func DefaultMounts() ([]Mount, error) {
	data, err := hostfs.ReadFile(procMountsPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", procMountsPath, err)
	}
	out := parseProcMounts(string(data))
	// Best effort: the device numbers only add what a stale-mount verdict needs,
	// and a table without them is still the table every other reader wants.
	if info, err := hostfs.ReadFile(procSelfMountInfo); err == nil {
		attachDeviceIDs(out, mounts.ParseMountInfo(info))
	}
	return out, nil
}

// attachDeviceIDs gives each mount its device number. Both files list the
// mounts of one namespace in the same order, so the mounts stacked on one point
// pair up in sequence. A mount that changed between the two reads is left
// without a number, or with its successor's, for that one sample.
func attachDeviceIDs(table []Mount, info []mounts.MountInfo) {
	ids := make(map[string][]string, len(info))
	for _, entry := range info {
		ids[entry.MountPoint] = append(ids[entry.MountPoint], entry.DeviceID)
	}
	for i := range table {
		point := table[i].MountPoint
		if queue := ids[point]; len(queue) > 0 {
			table[i].DeviceID, ids[point] = queue[0], queue[1:]
		}
	}
}

func parseProcMounts(data string) []Mount {
	var out []Mount
	for line := range strings.SplitSeq(data, checkLineSeparator) {
		fields := strings.Fields(line)
		if len(fields) < procMountsMinFields {
			continue
		}
		out = append(out, Mount{
			Device:     mounts.UnescapeField(fields[procMountsDeviceIndex]),
			MountPoint: mounts.UnescapeField(fields[procMountsMountPointIndex]),
			FSType:     fields[procMountsFSTypeIndex],
			Options:    strings.Split(fields[procMountsOptionsIndex], procMountsOptionsSep),
		})
	}
	return out
}

// MountForPath returns the deepest mount containing path, or nil when none is
// known. It is useful for operator views where a storage check points at a
// directory below the actual mountpoint.
func MountForPath(table []Mount, path string) *Mount {
	cleanPath := filepath.Clean(path)
	var best *Mount
	bestLen := 0
	for i := range table {
		mp := filepath.Clean(table[i].MountPoint)
		if !mounts.PathUnder(cleanPath, mp) {
			continue
		}
		if best == nil || len(mp) > bestLen {
			best = &table[i]
			bestLen = len(mp)
			continue
		}
		if len(mp) == bestLen {
			best = betterMount(best, &table[i])
		}
	}
	return best
}

// MountAtPath returns the mount whose mountpoint exactly matches path, preferring
// a real filesystem over an autofs placeholder when both entries share the path.
func MountAtPath(table []Mount, path string) *Mount {
	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) {
		return nil
	}
	var best *Mount
	for i := range table {
		if filepath.Clean(table[i].MountPoint) == cleanPath {
			best = betterMount(best, &table[i])
		}
	}
	return best
}

func betterMount(current, candidate *Mount) *Mount {
	if current == nil {
		return candidate
	}
	if current.FSType == FSTypeAutofs && candidate.FSType != FSTypeAutofs {
		return candidate
	}
	return current
}
