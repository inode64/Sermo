// Package mounts contains shared helpers for Linux mount table and fstab data.
package mounts

import "strings"

var escapedFieldReplacer = strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)

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
