//go:build !linux

package conn

import (
	"fmt"
	"syscall"
)

// bindControl is unsupported off Linux: it fails the dial/listen so an operator
// who asked to egress a specific interface is not silently sent out the wrong one.
func bindControl(iface string) func(network, address string, c syscall.RawConn) error {
	return func(string, string, syscall.RawConn) error {
		return fmt.Errorf("egress interface binding (%q) is only supported on Linux", iface)
	}
}
