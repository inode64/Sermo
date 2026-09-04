package procnet

import (
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// ParseHost decodes a /proc/net hex address as IPv4 or IPv6.
func ParseHost(hexAddr string, ipv6 bool) (string, bool) {
	if ipv6 {
		return ParseIPv6Host(hexAddr)
	}
	return ParseIPv4Host(hexAddr)
}

// ParseIPv4Host decodes a little-endian IPv4 address from /proc/net hex.
func ParseIPv4Host(hexAddr string) (string, bool) {
	if len(hexAddr) != IPv4HexChars {
		return "", false
	}
	raw, err := strconv.ParseUint(hexAddr, HexBase, IPv4Bits)
	if err != nil {
		return "", false
	}
	var b [net.IPv4len]byte
	binary.LittleEndian.PutUint32(b[:], uint32(raw))
	ip := net.IPv4(b[IPv4Byte0], b[IPv4Byte1], b[IPv4Byte2], b[IPv4Byte3])
	return ip.String(), true
}

// ParseIPv6Host decodes a little-endian IPv6 address from /proc/net hex.
func ParseIPv6Host(hexAddr string) (string, bool) {
	if len(hexAddr) != IPv6HexChars {
		return "", false
	}
	var b [net.IPv6len]byte
	for i := range IPv6Words {
		start := i * IPv6WordHexChars
		raw, err := strconv.ParseUint(hexAddr[start:start+IPv6WordHexChars], HexBase, IPv6WordBits)
		if err != nil {
			return "", false
		}
		binary.LittleEndian.PutUint32(b[i*net.IPv4len:], uint32(raw))
	}
	return net.IP(b[:]).String(), true
}

// ParseIPv4Socket decodes a /proc/net IPv4 local_address field (HHHHHHHH:PPPP).
func ParseIPv4Socket(localAddress string) (host string, port int, err error) {
	addrHex, portHex, ok := strings.Cut(localAddress, AddressSeparator)
	if !ok {
		return "", 0, fmt.Errorf("malformed socket address %q", localAddress)
	}
	host, ok = ParseIPv4Host(addrHex)
	if !ok {
		return "", 0, fmt.Errorf("malformed socket address %q", localAddress)
	}
	rawPort, err := strconv.ParseUint(portHex, HexBase, PortBits)
	if err != nil {
		return "", 0, fmt.Errorf("malformed socket port %q: %w", localAddress, err)
	}
	return host, int(rawPort), nil
}
