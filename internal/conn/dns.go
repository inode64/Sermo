package conn

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func init() { Register(dnsProtocol{}) }

// dnsProtocol probes a DNS server natively: it sends an A query (over UDP) for a
// configurable name (default "localhost") and verifies the server answers. A
// NOERROR or NXDOMAIN reply means the server is up and speaking DNS; SERVFAIL,
// REFUSED, a transport error or a timeout fail the check. No authentication.
// Message encoding/parsing uses golang.org/x/net/dns/dnsmessage (the package the
// standard library resolver builds on) rather than a hand-rolled wire codec.
type dnsProtocol struct{}

func (dnsProtocol) Name() string       { return "dns" }
func (dnsProtocol) DefaultPort() int   { return 53 }
func (dnsProtocol) RequiresUser() bool { return false }

// resolvConfPath is the resolver configuration consulted by `resolvconf:
// true`; a variable so tests can point it at a fixture.
var resolvConfPath = "/etc/resolv.conf"

// dnsInterfaceAddrs is a seam for tests; production uses the host's assigned
// interface addresses to avoid binding local-resolver probes to an egress NIC.
var dnsInterfaceAddrs = net.InterfaceAddrs

const dnsLocalRouteTimeout = 100 * time.Millisecond

var dnsRouteAddrs = func(host string) (net.Addr, net.Addr, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dnsLocalRouteTimeout)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(ctx, networkUDP, net.JoinHostPort(host, "53"))
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = c.Close() }()
	return c.LocalAddr(), c.RemoteAddr(), nil
}

func (dnsProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if cfg.Params["resolvconf"] == "true" {
		ns, err := firstNameserver(resolvConfPath)
		if err != nil {
			return Result{}, err
		}
		host = ns
	}
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 53
	}
	name := cfg.Query
	if name == "" {
		name = "localhost"
	}

	id := dnsID()
	query, err := buildDNSQuery(id, name, 1) // QTYPE A
	if err != nil {
		return Result{}, err
	}

	c, err := BindDialer(dnsProbeInterface(host, cfg.Interface)).DialContext(ctx, networkUDP, net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	applyDeadline(ctx, c)

	if _, err := c.Write(query); err != nil {
		return Result{}, err
	}
	buf := make([]byte, 1500)
	n, err := c.Read(buf)
	if err != nil {
		return Result{}, err
	}
	rid, rcode, answers, addrs, err := parseDNSReply(buf[:n])
	if err != nil {
		return Result{}, err
	}
	if rid != id {
		return Result{}, errors.New("DNS response id mismatch")
	}
	if !dnsResponseOK(rcode) {
		return Result{}, fmt.Errorf("DNS query for %q returned %s", name, rcodeName(rcode))
	}
	return Result{Extra: map[string]string{
		"query":     name,
		"rcode":     rcodeName(rcode),
		"answers":   strconv.Itoa(answers),
		"addresses": strings.Join(addrs, ","),
	}}, nil
}

// firstNameserver returns the first `nameserver` entry of a resolv.conf-style
// file — the server the system resolver would ask first (with pppd's
// usepeerdns, the provider's resolver).
func firstNameserver(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("no nameserver entries in %s", path)
}

func dnsProbeInterface(host, iface string) string {
	if iface == "" {
		return ""
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip != nil && (ip.IsLoopback() || dnsNameserverIsLocal(ip) || dnsNameserverRoutesToSelf(ip)) {
		return ""
	}
	return iface
}

func dnsNameserverIsLocal(ip net.IP) bool {
	addrs, err := dnsInterfaceAddrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		switch a := addr.(type) {
		case *net.IPNet:
			if a.IP.Equal(ip) {
				return true
			}
		case *net.IPAddr:
			if a.IP.Equal(ip) {
				return true
			}
		}
	}
	return false
}

func dnsNameserverRoutesToSelf(ip net.IP) bool {
	local, remote, err := dnsRouteAddrs(ip.String())
	if err != nil {
		return false
	}
	localUDP, ok := local.(*net.UDPAddr)
	if !ok {
		return false
	}
	remoteUDP, ok := remote.(*net.UDPAddr)
	if !ok {
		return false
	}
	return localUDP.IP.Equal(remoteUDP.IP)
}

// dnsResponseOK reports whether an rcode means the server answered healthily: a
// successful lookup (NOERROR) or an authoritative "no such name" (NXDOMAIN).
func dnsResponseOK(rcode int) bool { return rcode == 0 || rcode == 3 }

func rcodeName(rcode int) string {
	switch rcode {
	case 0:
		return "NOERROR"
	case 1:
		return "FORMERR"
	case 2:
		return "SERVFAIL"
	case 3:
		return "NXDOMAIN"
	case 4:
		return "NOTIMP"
	case 5:
		return "REFUSED"
	default:
		return "RCODE" + strconv.Itoa(rcode)
	}
}

func dnsID() uint16 {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0x1234
	}
	return binary.BigEndian.Uint16(b[:])
}

// buildDNSQuery builds a standard recursive query message (header + one question)
// for name and qtype, packed with dnsmessage.
func buildDNSQuery(id uint16, name string, qtype uint16) ([]byte, error) {
	qname, err := dnsmessage.NewName(dnsFQDN(name))
	if err != nil {
		return nil, err
	}
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{ID: id, RecursionDesired: true},
		Questions: []dnsmessage.Question{{
			Name:  qname,
			Type:  dnsmessage.Type(qtype),
			Class: dnsmessage.ClassINET,
		}},
	}
	return msg.Pack()
}

// dnsFQDN returns name as a fully-qualified domain name (trailing dot), the form
// dnsmessage.NewName requires. An empty name becomes the root ".".
func dnsFQDN(name string) string {
	if strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}

// parseDNSReply parses a DNS response with dnsmessage: it returns the id, RCODE,
// the header's answer count and the A/AAAA addresses (sorted). It errors on a
// too-short message or a query (QR=0). The answer section is parsed leniently —
// a malformed record stops collection and yields what was read so far — so a
// truncated reply still reports liveness rather than failing the probe.
func parseDNSReply(b []byte) (id uint16, rcode int, answers int, addrs []string, err error) {
	var p dnsmessage.Parser
	hdr, err := p.Start(b)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	if !hdr.Response {
		return hdr.ID, 0, 0, nil, errors.New("not a DNS response (QR=0)")
	}
	if len(b) >= 12 { // always true after Start; makes the header read bounds-safe
		answers = int(binary.BigEndian.Uint16(b[6:8])) // ANCOUNT from the header
	}
	// Collect A/AAAA answers; a malformed question/answer section still leaves a
	// valid header for the liveness verdict, so it is not a probe error.
	if p.SkipAllQuestions() == nil {
		addrs = dnsAnswerAddrs(&p)
	}
	return hdr.ID, int(hdr.RCode), answers, addrs, nil
}

// dnsAnswerAddrs walks a parser positioned at the answer section and returns the
// A/AAAA (IN class) addresses, sorted. Other record types are skipped; a
// malformed record ends collection without error.
func dnsAnswerAddrs(p *dnsmessage.Parser) []string {
	var addrs []string
loop:
	for {
		h, err := p.AnswerHeader()
		if err != nil {
			break // ErrSectionDone or a malformed header
		}
		switch h.Type {
		case dnsmessage.TypeA:
			a, err := p.AResource()
			if err != nil {
				break loop
			}
			if h.Class == dnsmessage.ClassINET {
				addrs = append(addrs, net.IP(a.A[:]).String())
			}
		case dnsmessage.TypeAAAA:
			aaaa, err := p.AAAAResource()
			if err != nil {
				break loop
			}
			if h.Class == dnsmessage.ClassINET {
				addrs = append(addrs, net.IP(aaaa.AAAA[:]).String())
			}
		default:
			if err := p.SkipAnswer(); err != nil {
				break loop
			}
		}
	}
	slices.Sort(addrs)
	return addrs
}
