package servicemgr

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrInvalidOpenRCUnit reports a unit name that is not one component of the
// init script directory. rc-service runs an absolute name as given and joins a
// relative one under init.d before following "..", so such a name would run a
// script outside /etc/init.d as root.
var ErrInvalidOpenRCUnit = errors.New("invalid OpenRC unit")

// ValidOpenRCUnit reports whether unit names exactly one OpenRC init script.
func ValidOpenRCUnit(unit string) bool {
	_, ok := openRCUnitPath(openRCInitDir, unit)
	return ok
}

func invalidOpenRCUnit(unit string) error {
	return fmt.Errorf("%w %q", ErrInvalidOpenRCUnit, unit)
}

// openRCUnitPath confines a unit name to one component of an init-owned directory.
// Validate before Join, which would otherwise erase traversal components.
func openRCUnitPath(dir, unit string) (string, bool) {
	if !filepath.IsLocal(unit) || unit == "." || filepath.Base(unit) != unit || strings.ContainsAny(unit, "\\\x00") {
		return "", false
	}
	return filepath.Join(dir, unit), true
}

// cgroupProcsPath maps an absolute, canonical hierarchy path to the unified
// cgroup mount. A root or namespace-relative path cannot identify owned PIDs.
func cgroupProcsPath(group string) (string, bool) {
	if !filepath.IsAbs(group) || filepath.Clean(group) != group || strings.ContainsRune(group, 0) {
		return "", false
	}
	relative := strings.TrimPrefix(group, "/")
	if !filepath.IsLocal(relative) {
		return "", false
	}
	return filepath.Join(cgroupRoot, relative, "cgroup.procs"), true
}
