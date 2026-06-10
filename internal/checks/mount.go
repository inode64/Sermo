package checks

import (
	"fmt"
	"os"
	"path/filepath"
	"sermo/internal/cfgval"
	"slices"
	"strings"
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

// mountCond are the optional mount expectations folded into a disk check, so a
// filesystem's mount and its space are checked from one entry (no duplicated
// path). active is true when any condition was configured.
type mountCond struct {
	active      bool
	expectMount bool // require mounted; when false, require NOT mounted
	fstype      string
	device      string
	options     []string
}

// parseMountCond reads the mount expectations from a disk check entry. Any of
// mounted/fstype/options/device activates mount verification; `mounted` defaults
// to true when a condition is present.
func parseMountCond(entry map[string]any) mountCond {
	m := mountCond{expectMount: true}
	if v, ok := entry["mounted"].(bool); ok {
		m.active, m.expectMount = true, v
	}
	m.fstype = cfgval.AsString(entry["fstype"])
	m.device = cfgval.AsString(entry["device"])
	m.options = cfgval.StringArray(entry["options"])
	if m.fstype != "" || m.device != "" || len(m.options) > 0 {
		m.active = true
	}
	return m
}

// evaluate checks the mount table for path against the expectations. problem is
// true when the expectation is violated; info is the matching mount entry (nil
// when not mounted).
func (m mountCond) evaluate(mounts []Mount, path string) (mounted, problem bool, reason string, info *Mount) {
	for i := range mounts {
		if mounts[i].MountPoint == path {
			info = &mounts[i]
			break
		}
	}
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

	var problems []string
	if m.fstype != "" && info.FSType != m.fstype {
		problems = append(problems, fmt.Sprintf("fstype %s (want %s)", info.FSType, m.fstype))
	}
	if m.device != "" && info.Device != m.device {
		problems = append(problems, fmt.Sprintf("device %s (want %s)", info.Device, m.device))
	}
	for _, opt := range m.options {
		if !slices.Contains(info.Options, opt) {
			problems = append(problems, "missing option "+opt)
		}
	}
	if len(problems) > 0 {
		return mounted, true, strings.Join(problems, ", "), info
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
			Device:     unescapeMount(fields[0]),
			MountPoint: unescapeMount(fields[1]),
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
// known. It is useful for operator views where a disk check points at a
// directory below the actual mountpoint.
func MountForPath(mounts []Mount, path string) *Mount {
	cleanPath := filepath.Clean(path)
	var best *Mount
	for i := range mounts {
		mp := filepath.Clean(mounts[i].MountPoint)
		if !pathUnderMount(cleanPath, mp) {
			continue
		}
		if best == nil || len(mp) > len(filepath.Clean(best.MountPoint)) {
			best = &mounts[i]
		}
	}
	return best
}

func pathUnderMount(path, mountPoint string) bool {
	if mountPoint == "." || path == "." {
		return false
	}
	if mountPoint == "/" {
		return strings.HasPrefix(path, "/")
	}
	return path == mountPoint || strings.HasPrefix(path, mountPoint+"/")
}

// unescapeMount decodes the octal escapes /proc/mounts uses for space, tab,
// newline and backslash in device and mount-point fields.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}
