// Package mounts contains shared helpers for Linux mount table and fstab data.
package mounts

import (
	"path/filepath"
	"strings"
)

const (
	mountFieldEscapeSpace     = `\040`
	mountFieldSpace           = " "
	mountFieldEscapeTab       = `\011`
	mountFieldTab             = "\t"
	mountFieldEscapeNewline   = `\012`
	mountFieldNewline         = "\n"
	mountFieldEscapeBackslash = `\134`
	mountFieldBackslash       = "\\"
)

var escapedFieldReplacer = strings.NewReplacer(
	mountFieldEscapeSpace,
	mountFieldSpace,
	mountFieldEscapeTab,
	mountFieldTab,
	mountFieldEscapeNewline,
	mountFieldNewline,
	mountFieldEscapeBackslash,
	mountFieldBackslash,
)

// UnescapeField decodes the octal escapes used in /proc/mounts and /etc/fstab
// fields for space, tab, newline and backslash.
func UnescapeField(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	return escapedFieldReplacer.Replace(s)
}

// PathUnder reports whether the absolute path is the mount point itself or a
// child of it. The comparison is lexical: both paths are cleaned, but symlinks
// are not resolved.
func PathUnder(path, mountPoint string) bool {
	path = filepath.Clean(path)
	mountPoint = filepath.Clean(mountPoint)
	if !filepath.IsAbs(path) || !filepath.IsAbs(mountPoint) {
		return false
	}
	if mountPoint == "/" {
		return true
	}
	return path == mountPoint || strings.HasPrefix(path, mountPoint+"/")
}

// PathStrictlyUnder reports whether the absolute path is a child of the mount
// point, excluding the mount point itself. It has the same lexical semantics as
// PathUnder.
func PathStrictlyUnder(path, mountPoint string) bool {
	path = filepath.Clean(path)
	mountPoint = filepath.Clean(mountPoint)
	return path != mountPoint && PathUnder(path, mountPoint)
}
