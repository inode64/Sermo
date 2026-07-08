// Package mounts contains shared helpers for Linux mount table and fstab data.
package mounts

import "strings"

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

// PathUnder reports whether path is the mount point itself or a child of it.
// Callers pass cleaned paths; under "/" requires an absolute path.
func PathUnder(path, mountPoint string) bool {
	if mountPoint == "." || path == "." {
		return false
	}
	if mountPoint == "/" {
		return strings.HasPrefix(path, "/")
	}
	return path == mountPoint || strings.HasPrefix(path, mountPoint+"/")
}
