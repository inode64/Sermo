//go:build linux

package conn

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// bindControl binds the socket to iface via SO_BINDTODEVICE before connect/listen,
// so traffic egresses (and is received on) that interface regardless of the
// routing table. Needs CAP_NET_RAW.
func bindControl(iface string) func(network, address string, c syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface)
		}); err != nil {
			return err
		}
		return serr
	}
}
