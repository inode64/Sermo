package conn

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

func init() { Register(dhclientProtocol{}, protocolAliasDHClient) }

const procNetUDPPath = "/proc/net/udp"

const (
	procUDPHeaderField              = "sl"
	procUDPMinFields                = 10
	procUDPHeaderIndex              = 0
	procUDPLocalAddressIndex        = 1
	procUDPStateIndex               = 3
	procUDPInodeIndex               = 9
	procUDPAddressSeparator         = ":"
	procUDPHexBase                  = 16
	procUDPPortBits                 = 16
	procUDPIPv4Bits                 = 32
	procUDPFormatBase               = 10
	dhclientAnyInterface            = "any interface"
	dhclientLeaseBlockStart         = "lease {"
	dhclientLeaseBlockEnd           = "}"
	dhclientLeaseExpireField        = "expire"
	dhclientLeaseExpirePrefix       = dhclientLeaseExpireField + " "
	dhclientLeaseFixedAddressPrefix = "fixed-address "
	dhclientLeaseInterfacePrefix    = "interface "
	dhclientLeaseQuoteCutset        = `"`
	dhclientLeaseTerminator         = ";"
	dhclientLeaseTimeLayout         = "2006/01/02 15:04:05"
	dhclientLeaseExpireMinFields    = 4
	dhclientLeaseExpireFieldIndex   = 0
	dhclientLeaseExpireDateIndex    = 2
	dhclientLeaseExpireTimeIndex    = 3
	ipv4Byte0                       = 0
	ipv4Byte1                       = 1
	ipv4Byte2                       = 2
	ipv4Byte3                       = 3
)

// dhclientProtocol verifies a local DHCP client socket. A DHCP client does not
// expose a request/response service like dhcpd does; it receives offers on UDP
// port 68. The probe therefore checks the kernel UDP socket table directly.
type dhclientProtocol struct{}

func (dhclientProtocol) Name() string       { return ProtocolNameDHClient }
func (dhclientProtocol) DefaultPort() int   { return dhcpClientPort }
func (dhclientProtocol) RequiresUser() bool { return false }

func (dhclientProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	port := cfg.Port
	if port == 0 {
		port = dhcpClientPort
	}
	sock, err := findUDP4Socket(procNetUDPPath, cfg.Host, port)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	extra := map[string]string{
		extraProtocol:        networkUDP,
		ExtraKeyLocalAddress: sock.localAddress,
		ExtraKeyPort:         strconv.Itoa(sock.port),
		ExtraKeyState:        sock.state,
		ExtraKeyInode:        sock.inode,
	}
	if cfg.Query != "" {
		now := time.Now().UTC()
		lease, err := readDHClientLease(cfg.Query, cfg.Interface, now)
		if err != nil {
			return Result{}, err
		}
		extra[extraLeaseFile] = cfg.Query
		extra[extraLeaseExpires] = lease.expires.Format(time.RFC3339)
		extra[extraLeaseSecondsRemaining] = strconv.FormatInt(int64(lease.expires.Sub(now).Seconds()), procUDPFormatBase)
		if lease.interfaceName != "" {
			extra[extraInterface] = lease.interfaceName
		}
		if lease.fixedAddress != "" {
			extra[extraFixedAddress] = lease.fixedAddress
		}
	}
	return Result{Extra: extra}, nil
}

type udpSocket struct {
	localAddress string
	port         int
	state        string
	inode        string
}

func findUDP4Socket(path, host string, port int) (udpSocket, error) {
	f, err := os.Open(path)
	if err != nil {
		return udpSocket{}, fmt.Errorf("dhclient: read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if sock, ok, err := parseUDP4SocketTable(f, host, port); err != nil {
		return udpSocket{}, err
	} else if ok {
		return sock, nil
	}
	want := "*"
	if host != "" {
		want = host
	}
	return udpSocket{}, fmt.Errorf("dhclient: no UDP socket bound on %s:%d", want, port)
}

func parseUDP4SocketTable(r io.Reader, host string, port int) (udpSocket, bool, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < procUDPMinFields || fields[procUDPHeaderIndex] == procUDPHeaderField {
			continue
		}
		addr, p, err := parseProcUDP4Address(fields[procUDPLocalAddressIndex])
		if err != nil {
			return udpSocket{}, false, err
		}
		if p != port || (host != "" && addr != host) {
			continue
		}
		return udpSocket{localAddress: addr, port: p, state: fields[procUDPStateIndex], inode: fields[procUDPInodeIndex]}, true, nil
	}
	if err := sc.Err(); err != nil {
		return udpSocket{}, false, fmt.Errorf("dhclient: read UDP socket table: %w", err)
	}
	return udpSocket{}, false, nil
}

func parseProcUDP4Address(s string) (string, int, error) {
	addrHex, portHex, ok := strings.Cut(s, procUDPAddressSeparator)
	if !ok {
		return "", 0, fmt.Errorf("dhclient: malformed UDP address %q", s)
	}
	addr, err := strconv.ParseUint(addrHex, procUDPHexBase, procUDPIPv4Bits)
	if err != nil {
		return "", 0, fmt.Errorf("dhclient: malformed UDP address %q: %w", s, err)
	}
	port, err := strconv.ParseUint(portHex, procUDPHexBase, procUDPPortBits)
	if err != nil {
		return "", 0, fmt.Errorf("dhclient: malformed UDP port %q: %w", s, err)
	}
	var b [net.IPv4len]byte
	binary.LittleEndian.PutUint32(b[:], uint32(addr))
	ip := net.IPv4(b[ipv4Byte0], b[ipv4Byte1], b[ipv4Byte2], b[ipv4Byte3])
	return ip.String(), int(port), nil
}

type dhclientLease struct {
	interfaceName string
	fixedAddress  string
	expires       time.Time
}

func readDHClientLease(path, iface string, now time.Time) (dhclientLease, error) {
	f, err := os.Open(path)
	if err != nil {
		return dhclientLease{}, fmt.Errorf("dhclient: read lease file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	lease, ok, err := parseDHClientLeases(f, iface, now)
	if err != nil {
		return dhclientLease{}, err
	}
	if !ok {
		target := dhclientAnyInterface
		if iface != "" {
			target = dhclientLeaseInterfacePrefix + iface
		}
		return dhclientLease{}, fmt.Errorf("dhclient: no unexpired lease in %s for %s", path, target)
	}
	return lease, nil
}

func parseDHClientLeases(r io.Reader, iface string, now time.Time) (dhclientLease, bool, error) {
	sc := bufio.NewScanner(r)
	var cur dhclientLease
	inLease := false
	var best dhclientLease
	var found bool
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == dhclientLeaseBlockStart:
			inLease = true
			cur = dhclientLease{}
		case inLease && line == dhclientLeaseBlockEnd:
			if cur.expires.After(now) && (iface == "" || cur.interfaceName == iface) {
				if !found || cur.expires.After(best.expires) {
					best, found = cur, true
				}
			}
			inLease = false
		case inLease && strings.HasPrefix(line, dhclientLeaseInterfacePrefix):
			cur.interfaceName = strings.Trim(strings.TrimSuffix(strings.TrimPrefix(line, dhclientLeaseInterfacePrefix), dhclientLeaseTerminator), dhclientLeaseQuoteCutset)
		case inLease && strings.HasPrefix(line, dhclientLeaseFixedAddressPrefix):
			cur.fixedAddress = strings.TrimSuffix(strings.TrimPrefix(line, dhclientLeaseFixedAddressPrefix), dhclientLeaseTerminator)
		case inLease && strings.HasPrefix(line, dhclientLeaseExpirePrefix):
			expires, err := parseDHClientLeaseTime(line)
			if err != nil {
				return dhclientLease{}, false, err
			}
			cur.expires = expires
		}
	}
	if err := sc.Err(); err != nil {
		return dhclientLease{}, false, fmt.Errorf("dhclient: read lease file: %w", err)
	}
	return best, found, nil
}

func parseDHClientLeaseTime(line string) (time.Time, error) {
	fields := strings.Fields(strings.TrimSuffix(line, dhclientLeaseTerminator))
	if len(fields) != dhclientLeaseExpireMinFields || fields[dhclientLeaseExpireFieldIndex] != dhclientLeaseExpireField {
		return time.Time{}, fmt.Errorf("dhclient: malformed lease expiry %q", line)
	}
	t, err := time.ParseInLocation(dhclientLeaseTimeLayout, fields[dhclientLeaseExpireDateIndex]+" "+fields[dhclientLeaseExpireTimeIndex], time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("dhclient: malformed lease expiry %q: %w", line, err)
	}
	return t, nil
}
