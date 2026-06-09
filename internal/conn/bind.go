package conn

import (
	"fmt"
	"net"
)

// resolveInterface maps an identifier — an interface name (eth0), an IP address
// it carries (192.168.1.2), or its MAC (00:11:22:33:44:55) — to the interface.
func resolveInterface(id string) (*net.Interface, error) {
	if ifi, err := net.InterfaceByName(id); err == nil {
		return ifi, nil
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	if mac, err := net.ParseMAC(id); err == nil {
		for i := range ifaces {
			if ifaces[i].HardwareAddr.String() == mac.String() {
				return &ifaces[i], nil
			}
		}
		return nil, fmt.Errorf("no interface has MAC %s", id)
	}
	if ip := net.ParseIP(id); ip != nil {
		for i := range ifaces {
			addrs, _ := ifaces[i].Addrs()
			for _, a := range addrs {
				if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
					return &ifaces[i], nil
				}
			}
		}
		return nil, fmt.Errorf("no interface has IP %s", id)
	}
	return nil, fmt.Errorf("interface %q is not a known name, IP or MAC", id)
}

// ResolveInterfaceName resolves an identifier (name/IP/MAC) to the interface's
// device name, for SO_BINDTODEVICE.
func ResolveInterfaceName(id string) (string, error) {
	ifi, err := resolveInterface(id)
	if err != nil {
		return "", err
	}
	return ifi.Name, nil
}

// ResolveInterfaceIPv4 resolves an identifier to a source IPv4 address to bind
// to. An identifier that is itself an IPv4 is used verbatim; a name/MAC resolves
// to the interface's first IPv4. For the ICMP probe (source-address binding).
func ResolveInterfaceIPv4(id string) (string, error) {
	if ip := net.ParseIP(id); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4.String(), nil
		}
	}
	ifi, err := resolveInterface(id)
	if err != nil {
		return "", err
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if v4 := n.IP.To4(); v4 != nil {
				return v4.String(), nil
			}
		}
	}
	return "", fmt.Errorf("interface %s has no IPv4 address", ifi.Name)
}

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
