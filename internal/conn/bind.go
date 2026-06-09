package conn

import "net"

// BindDialer returns a net.Dialer that egresses through the named network
// interface (Linux SO_BINDTODEVICE). With an empty iface it is a plain dialer, so
// every protocol can route its dial through BindDialer(cfg.Interface)
// unconditionally. Exported so the checks package (tcp/ports/http/websocket) binds
// the same way.
//
// SO_BINDTODEVICE forces the socket to send and receive only on iface regardless
// of the routing table — what you want when a host has several interfaces and the
// probe must leave through a specific one. It requires CAP_NET_RAW (root), so an
// unprivileged daemon dialing with an interface set fails loudly rather than
// silently using the wrong link. Only supported on Linux.
func BindDialer(iface string) *net.Dialer {
	d := &net.Dialer{}
	if iface != "" {
		d.Control = bindControl(iface)
	}
	return d
}

// BindListenConfig is the net.ListenConfig equivalent of BindDialer, for packet
// sockets opened with ListenPacket (e.g. the ICMP and TFTP probes).
func BindListenConfig(iface string) net.ListenConfig {
	var lc net.ListenConfig
	if iface != "" {
		lc.Control = bindControl(iface)
	}
	return lc
}
