package diag

import (
	"net"
	"os"
	"strings"
)

// OSHost implements Host against the real machine: the filesystem, the network
// interfaces and /proc/mounts.
type OSHost struct{}

// PathExists reports whether path exists on the host filesystem.
func (OSHost) PathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// InterfaceExists reports whether a network interface with the given name exists.
func (OSHost) InterfaceExists(name string) bool {
	_, err := net.InterfaceByName(name)
	return err == nil
}

// IsMountPoint reports whether path is a mount point, per /proc/mounts.
func (OSHost) IsMountPoint(path string) bool {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && unescapeMount(fields[1]) == path {
			return true
		}
	}
	return false
}

// unescapeMount decodes /proc/mounts octal escapes (space/tab/newline/backslash).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(s)
}
