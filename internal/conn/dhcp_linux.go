//go:build linux

package conn

import (
	"context"
	"encoding/binary"
	"net"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// dhcpExchange sends packet and returns the first DHCP reply matching xid. When
// iface is set it broadcasts out that link (255.255.255.255:67), binding the
// socket to the interface; otherwise it unicasts to server (host:port). Either
// way it binds the privileged client port 68 to receive the reply, so it needs
// CAP_NET_BIND_SERVICE (and CAP_NET_RAW for SO_BINDTODEVICE), or root. Replies
// for other clients arriving on port 68 are skipped until the context deadline.
func dhcpExchange(ctx context.Context, iface, server string, packet []byte, xid uint32) ([]byte, error) {
	lc := net.ListenConfig{
		Control: func(_, _ string, c syscall.RawConn) error {
			var serr error
			if err := c.Control(func(fd uintptr) {
				if serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); serr != nil {
					return
				}
				if serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_BROADCAST, 1); serr != nil {
					return
				}
				if iface != "" {
					dev, derr := ResolveInterfaceName(iface) // accepts name/IP/MAC
					if derr != nil {
						serr = derr
						return
					}
					serr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, dev)
				}
			}); err != nil {
				return err
			}
			return serr
		},
	}
	pc, err := lc.ListenPacket(ctx, "udp4", ":"+strconv.Itoa(dhcpClientPort))
	if err != nil {
		return nil, err
	}
	defer func() { _ = pc.Close() }()

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(5 * time.Second)
	}
	_ = pc.SetDeadline(deadline)

	dst, err := dhcpDestination(iface, server)
	if err != nil {
		return nil, err
	}
	if _, err := pc.WriteTo(packet, dst); err != nil {
		return nil, err
	}

	buf := make([]byte, 1500)
	for {
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			return nil, err
		}
		if n >= 8 && buf[0] == dhcpOpBootReply && binary.BigEndian.Uint32(buf[4:8]) == xid {
			reply := make([]byte, n)
			copy(reply, buf[:n])
			return reply, nil
		}
		// A reply for another client; keep reading until the deadline.
	}
}

// dhcpDestination is the limited broadcast address for a per-interface probe, or
// the resolved server address for a unicast probe.
func dhcpDestination(iface, server string) (*net.UDPAddr, error) {
	if iface != "" {
		return &net.UDPAddr{IP: net.IPv4bcast, Port: dhcpServerPort}, nil
	}
	host, portStr, err := net.SplitHostPort(server)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, err
	}
	ip, err := net.ResolveIPAddr("ip4", host)
	if err != nil {
		return nil, err
	}
	return &net.UDPAddr{IP: ip.IP, Port: port}, nil
}
