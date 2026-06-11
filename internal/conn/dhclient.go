package conn

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

func init() { Register(dhclientProtocol{}, "dhcp-client") }

// dhclientProtocol verifies a local DHCP client socket. A DHCP client does not
// expose a request/response service like dhcpd does; it receives offers on UDP
// port 68. The probe therefore checks the kernel UDP socket table directly.
type dhclientProtocol struct{}

func (dhclientProtocol) Name() string       { return "dhclient" }
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
	sock, err := findUDP4Socket("/proc/net/udp", cfg.Host, port)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	extra := map[string]string{
		"protocol":      "udp",
		"local_address": sock.localAddress,
		"port":          strconv.Itoa(sock.port),
		"state":         sock.state,
		"inode":         sock.inode,
	}
	if cfg.Query != "" {
		now := time.Now().UTC()
		lease, err := readDHClientLease(cfg.Query, cfg.Interface, now)
		if err != nil {
			return Result{}, err
		}
		extra["lease_file"] = cfg.Query
		extra["lease_expires_at"] = lease.expires.Format(time.RFC3339)
		extra["lease_seconds_remaining"] = strconv.FormatInt(int64(lease.expires.Sub(now).Seconds()), 10)
		if lease.interfaceName != "" {
			extra["interface"] = lease.interfaceName
		}
		if lease.fixedAddress != "" {
			extra["fixed_address"] = lease.fixedAddress
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
		if len(fields) < 10 || fields[0] == "sl" {
			continue
		}
		addr, p, err := parseProcUDP4Address(fields[1])
		if err != nil {
			return udpSocket{}, false, err
		}
		if p != port || (host != "" && addr != host) {
			continue
		}
		return udpSocket{localAddress: addr, port: p, state: fields[3], inode: fields[9]}, true, nil
	}
	if err := sc.Err(); err != nil {
		return udpSocket{}, false, fmt.Errorf("dhclient: read UDP socket table: %w", err)
	}
	return udpSocket{}, false, nil
}

func parseProcUDP4Address(s string) (string, int, error) {
	addrHex, portHex, ok := strings.Cut(s, ":")
	if !ok {
		return "", 0, fmt.Errorf("dhclient: malformed UDP address %q", s)
	}
	addr, err := strconv.ParseUint(addrHex, 16, 32)
	if err != nil {
		return "", 0, fmt.Errorf("dhclient: malformed UDP address %q: %w", s, err)
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return "", 0, fmt.Errorf("dhclient: malformed UDP port %q: %w", s, err)
	}
	ip := net.IPv4(byte(addr), byte(addr>>8), byte(addr>>16), byte(addr>>24))
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
		target := "any interface"
		if iface != "" {
			target = "interface " + iface
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
		case line == "lease {":
			inLease = true
			cur = dhclientLease{}
		case inLease && line == "}":
			if cur.expires.After(now) && (iface == "" || cur.interfaceName == iface) {
				if !found || cur.expires.After(best.expires) {
					best, found = cur, true
				}
			}
			inLease = false
		case inLease && strings.HasPrefix(line, "interface "):
			cur.interfaceName = strings.Trim(strings.TrimSuffix(strings.TrimPrefix(line, "interface "), ";"), `"`)
		case inLease && strings.HasPrefix(line, "fixed-address "):
			cur.fixedAddress = strings.TrimSuffix(strings.TrimPrefix(line, "fixed-address "), ";")
		case inLease && strings.HasPrefix(line, "expire "):
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
	fields := strings.Fields(strings.TrimSuffix(line, ";"))
	if len(fields) != 4 || fields[0] != "expire" {
		return time.Time{}, fmt.Errorf("dhclient: malformed lease expiry %q", line)
	}
	t, err := time.ParseInLocation("2006/01/02 15:04:05", fields[2]+" "+fields[3], time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("dhclient: malformed lease expiry %q: %w", line, err)
	}
	return t, nil
}
