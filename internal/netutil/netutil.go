// Package netutil names shared network constants.
package netutil

import (
	"net"
	"strconv"
)

const (
	// LoopbackIPv4 is the IPv4 loopback address used for local-only defaults.
	LoopbackIPv4 = "127.0.0.1"
	// NetworkUnix is the net package network name for Unix-domain sockets.
	NetworkUnix = "unix"
)

// JoinHostPort formats host with an integer port using net.JoinHostPort rules.
func JoinHostPort(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}
