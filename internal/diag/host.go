package diag

import (
	"net"
	"os"
	"sermo/internal/checks"
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

// IsMountPoint reports whether path is a mount point, per the shared
// /proc/mounts parser (internal/checks owns the escaping rules).
func (OSHost) IsMountPoint(path string) bool {
	mounts, err := checks.DefaultMounts()
	if err != nil {
		return false
	}
	for i := range mounts {
		if mounts[i].MountPoint == path {
			return true
		}
	}
	return false
}
