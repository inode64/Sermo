//go:build linux

package conn

import (
	"context"
	"net"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

const (
	defaultDHCPExchangeTimeout = 5 * time.Second
	networkIP4                 = "ip4"
)

// dhcpExchange sends packet and returns the first DHCP reply matching xid. When
// iface is set it broadcasts out that link (255.255.255.255:67); otherwise it
// unicasts to server (host:port). Either way it binds the privileged client port
// 68 to receive the reply, so it needs CAP_NET_BIND_SERVICE, or root. Replies for
// other clients arriving on port 68 are skipped until the context deadline.
//
// The requested link is pinned per datagram with IP_PKTINFO rather than by
// binding the socket to the device, which is this probe's documented deviation
// from the SO_BINDTODEVICE rule the rest of internal/conn follows. A DHCP server
// running on this same host answers with a broadcast the kernel loops back, and
// that copy does not carry the LAN device SO_BINDTODEVICE filters on: a
// device-bound socket never sees it, so the probe timed out while the server was
// answering every single cycle. IP_PKTINFO pins egress exactly as strictly —
// nothing falls back to default routing — and additionally reports the link a
// reply came in on, so the same check is applied in userspace, where the
// looped-back copy *is* attributed to the link it was sent out of. Verified
// against dnsmasq serving two of the host's own LANs.
func dhcpExchange(ctx context.Context, iface, server string, packet []byte, xid uint32) ([]byte, error) {
	ifIndex, err := dhcpEgressIndex(iface)
	if err != nil {
		return nil, err
	}
	lc := net.ListenConfig{
		Control: func(_, _ string, c syscall.RawConn) error {
			var serr error
			if err := c.Control(func(fd uintptr) {
				if serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); serr != nil {
					return
				}
				serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_BROADCAST, 1)
			}); err != nil {
				return probeErr(ProtocolNameDHCP, stepDHCPBindSocket, err)
			}
			if serr != nil {
				return probeErr(ProtocolNameDHCP, stepDHCPBindSocket, serr)
			}
			return nil
		},
	}
	pc, err := lc.ListenPacket(ctx, "udp4", ":"+strconv.Itoa(dhcpClientPort))
	if err != nil {
		return nil, probeErr(ProtocolNameDHCP, stepListen, err)
	}
	defer func() { _ = pc.Close() }()

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(defaultDHCPExchangeTimeout)
	}
	_ = pc.SetDeadline(deadline)

	dst, err := dhcpDestination(iface, server)
	if err != nil {
		return nil, err
	}

	p := ipv4.NewPacketConn(pc)
	var egress *ipv4.ControlMessage
	if ifIndex != 0 {
		if err := p.SetControlMessage(ipv4.FlagInterface, true); err != nil {
			return nil, probeErr(ProtocolNameDHCP, stepDHCPBindSocket, err)
		}
		egress = &ipv4.ControlMessage{IfIndex: ifIndex}
	}
	if _, err := p.WriteTo(packet, egress, dst); err != nil {
		return nil, probeErr(ProtocolNameDHCP, stepRequest, err)
	}

	buf := make([]byte, dhcpUDPBufferBytes)
	for {
		n, cm, _, err := p.ReadFrom(buf)
		if err != nil {
			return nil, probeErr(ProtocolNameDHCP, stepReply, err)
		}
		// Another link's DHCP traffic, or another client's reply on ours: keep
		// reading until the deadline.
		if dhcpFromInterface(dhcpIngressIndex(cm), ifIndex) && dhcpReplyMatches(buf[:n], xid) {
			reply := make([]byte, n)
			copy(reply, buf[:n])
			return reply, nil
		}
	}
}

// dhcpEgressIndex resolves the link a per-interface probe must leave through. A
// unicast probe names no link and yields 0.
func dhcpEgressIndex(iface string) (int, error) {
	if iface == "" {
		return 0, nil
	}
	ifi, err := resolveInterface(iface) // accepts name/IP/MAC
	if err != nil {
		return 0, probeErr(ProtocolNameDHCP, stepDHCPBindSocket, err)
	}
	return ifi.Index, nil
}

// dhcpIngressIndex is the link a reply arrived on, or 0 when the kernel attached
// no control message to say.
func dhcpIngressIndex(cm *ipv4.ControlMessage) int {
	if cm == nil {
		return 0
	}
	return cm.IfIndex
}

// dhcpDestination is the limited broadcast address for a per-interface probe, or
// the resolved server address for a unicast probe.
func dhcpDestination(iface, server string) (*net.UDPAddr, error) {
	if iface != "" {
		return &net.UDPAddr{IP: net.IPv4bcast, Port: dhcpServerPort}, nil
	}
	host, portStr, err := net.SplitHostPort(server)
	if err != nil {
		return nil, probeErr(ProtocolNameDHCP, stepDHCPServerAddress, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, probeErr(ProtocolNameDHCP, stepDHCPServerPort, err)
	}
	ip, err := net.ResolveIPAddr(networkIP4, host)
	if err != nil {
		return nil, probeErr(ProtocolNameDHCP, stepResolveServer, err)
	}
	return &net.UDPAddr{IP: ip.IP, Port: port}, nil
}
