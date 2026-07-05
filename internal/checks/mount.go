package checks

import (
	"os"
	"path/filepath"
	"strings"

	"sermo/internal/mounts"
)

// Mount is one entry of the mount table.
type Mount struct {
	Device     string
	MountPoint string
	FSType     string
	Options    []string
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
	if v, ok := entry["mounted"].(bool); ok {
		m.active, m.expectMount = true, v
	}
	return m
}

// evaluate checks the mount table for path against the expectations. problem is
// true when the expectation is violated; info is the matching mount entry (nil
// when not mounted).
func (m mountCond) evaluate(mounts []Mount, path string) (mounted, problem bool, reason string, info *Mount) {
	info = MountAtPath(mounts, path)
	mounted = info != nil

	if !m.expectMount {
		if mounted {
			return mounted, true, "is mounted (want unmounted)", info
		}
		return mounted, false, "", info
	}
	if !mounted {
		return mounted, true, "is not mounted", info
	}
	return mounted, false, "", info
}

// defaultMountSampler reads the mount table from /proc/mounts.
func defaultMountSampler() ([]Mount, error) {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil, err
	}
	var out []Mount
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		out = append(out, Mount{
			Device:     mounts.UnescapeField(fields[0]),
			MountPoint: mounts.UnescapeField(fields[1]),
			FSType:     fields[2],
			Options:    strings.Split(fields[3], ","),
		})
	}
	return out, nil
}

// DefaultMounts reads the host mount table from /proc/mounts.
func DefaultMounts() ([]Mount, error) {
	return defaultMountSampler()
}

// MountForPath returns the deepest mount containing path, or nil when none is
// known. It is useful for operator views where a storage check points at a
// directory below the actual mountpoint.
func MountForPath(table []Mount, path string) *Mount {
	cleanPath := filepath.Clean(path)
	var best *Mount
	for i := range table {
		mp := filepath.Clean(table[i].MountPoint)
		if !mounts.PathUnder(cleanPath, mp) {
			continue
		}
		if best == nil || len(mp) > len(filepath.Clean(best.MountPoint)) {
			best = &table[i]
			continue
		}
		if len(mp) == len(filepath.Clean(best.MountPoint)) {
			best = betterMount(best, &table[i])
		}
	}
	return best
}

// MountAtPath returns the mount whose mountpoint exactly matches path, preferring
// a real filesystem over an autofs placeholder when both entries share the path.
func MountAtPath(mounts []Mount, path string) *Mount {
	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) {
		return nil
	}
	var best *Mount
	for i := range mounts {
		if filepath.Clean(mounts[i].MountPoint) == cleanPath {
			best = betterMount(best, &mounts[i])
		}
	}
	return best
}

func betterMount(current, candidate *Mount) *Mount {
	if current == nil {
		return candidate
	}
	if current.FSType == "autofs" && candidate.FSType != "autofs" {
		return candidate
	}
	return current
}
