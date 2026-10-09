package conn

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sermo/internal/hostfs"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"sermo/internal/netutil"
)

// dnsProtocol probes a DNS server natively: it sends a query (over UDP, type A
// unless `qtype` says otherwise) for a configurable name (default "localhost")
// and verifies the server answers. A NOERROR or NXDOMAIN reply means the server
// is up and speaking DNS. SERVFAIL, REFUSED and the other error rcodes are a
// protocol verdict (Result.Failure): the server answered and rejected the
// lookup, so the rcode and counts stay available as evidence and the check
// fails instead of becoming unavailable. A transport error or a timeout is a
// probe error. No authentication.
// Message encoding/parsing uses golang.org/x/net/dns/dnsmessage (the package the
// standard library resolver builds on) rather than a hand-rolled wire codec.
type dnsProtocol struct{}

func (dnsProtocol) Name() string       { return ProtocolNameDNS }
func (dnsProtocol) DefaultPort() int   { return dnsDefaultPort }
func (dnsProtocol) RequiresUser() bool { return false }

// resolvConfPath is the resolver configuration consulted by `resolvconf:
// true`; a variable so tests can point it at a fixture.
var resolvConfPath = "/etc/resolv.conf"

// dnsInterfaceAddrs is a seam for tests; production uses the host's assigned
// interface addresses to avoid binding local-resolver probes to an egress NIC.
var dnsInterfaceAddrs = net.InterfaceAddrs

const (
	dnsDefaultPort       = 53
	dnsDefaultQuery      = "localhost"
	dnsLocalRouteTimeout = 100 * time.Millisecond
	dnsUDPBufferBytes    = 1500
	dnsHeaderBytes       = 12
	dnsANCountStart      = 6
	dnsANCountEnd        = 8
	resolvConfLineSep    = "\n"
	resolvConfNameserver = "nameserver"
	resolvConfKeyIndex   = 0
	resolvConfValueIndex = 1
	resolvConfMinFields  = resolvConfValueIndex + 1
)

const (
	dnsQTypeA     uint16 = 1
	dnsQTypeNS    uint16 = 2
	dnsQTypeCNAME uint16 = 5
	dnsQTypeSOA   uint16 = 6
	dnsQTypePTR   uint16 = 12
	dnsQTypeMX    uint16 = 15
	dnsQTypeTXT   uint16 = 16
	dnsQTypeAAAA  uint16 = 28
	dnsQTypeSRV   uint16 = 33
)

// dnsQTypeNames maps the supported `qtype` spellings to their wire values, in
// the order DNSQTypeSummary lists them.
var dnsQTypeNames = []struct {
	name  string
	qtype uint16
}{
	{"A", dnsQTypeA}, {"AAAA", dnsQTypeAAAA}, {"CNAME", dnsQTypeCNAME}, {"MX", dnsQTypeMX},
	{"NS", dnsQTypeNS}, {"PTR", dnsQTypePTR}, {"SOA", dnsQTypeSOA}, {"SRV", dnsQTypeSRV}, {"TXT", dnsQTypeTXT},
}

// DNSQTypeSummary lists the record types a dns check's `qtype` accepts, for
// validation messages.
var DNSQTypeSummary = func() string {
	names := make([]string, 0, len(dnsQTypeNames))
	for _, q := range dnsQTypeNames {
		names = append(names, q.name)
	}
	return strings.Join(names, ", ")
}()

// ParseDNSQType resolves a `qtype` value (case-insensitive; empty means A) to
// its wire type and canonical upper-case name.
func ParseDNSQType(value string) (uint16, string, error) {
	if value == "" {
		return dnsQTypeA, dnsQTypeNames[0].name, nil
	}
	upper := strings.ToUpper(strings.TrimSpace(value))
	for _, q := range dnsQTypeNames {
		if q.name == upper {
			return q.qtype, q.name, nil
		}
	}
	return 0, "", fmt.Errorf("dns qtype must be one of %s, got %q", DNSQTypeSummary, value)
}

const (
	dnsRCodeNoError  = 0
	dnsRCodeFormErr  = 1
	dnsRCodeServFail = 2
	dnsRCodeNXDomain = 3
	dnsRCodeNotImp   = 4
	dnsRCodeRefused  = 5
)

const (
	dnsRCodeNameFormErr  = "FORMERR"
	dnsRCodeNameServFail = "SERVFAIL"
	dnsRCodeNameNXDomain = "NXDOMAIN"
	dnsRCodeNameNotImp   = "NOTIMP"
	dnsRCodeNameRefused  = "REFUSED"
	dnsRCodeNameUnknown  = "RCODE"
)

var dnsRouteAddrs = func(host string) (net.Addr, net.Addr, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dnsLocalRouteTimeout)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(ctx, networkUDP, netutil.JoinHostPort(host, dnsDefaultPort))
	if err != nil {
		return nil, nil, probeErr(ProtocolNameDNS, stepDNSLocalRoute, err)
	}
	defer func() { _ = c.Close() }()
	return c.LocalAddr(), c.RemoteAddr(), nil
}

func (dnsProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host, port := cfg.hostPortDefaults(dnsDefaultPort)
	if cfg.Params[ParamKeyResolvconf] == ParamValueTrue {
		ns, err := firstNameserver(resolvConfPath)
		if err != nil {
			return Result{}, err
		}
		host = ns
	}
	name := cfg.Query
	if name == "" {
		name = dnsDefaultQuery
	}
	qtype, qtypeName, err := ParseDNSQType(cfg.Params[ParamKeyQType])
	if err != nil {
		return Result{}, err
	}

	id := dnsID()
	query, err := buildDNSQuery(id, name, qtype)
	if err != nil {
		return Result{}, err
	}

	c, err := BindDialer(dnsProbeInterface(host, cfg.Interface)).DialContext(ctx, networkUDP, netutil.JoinHostPort(host, port))
	if err != nil {
		return Result{}, probeErr(ProtocolNameDNS, stepDial, err)
	}
	defer func() { _ = c.Close() }()
	ApplyDeadline(ctx, c)

	if _, err := c.Write(query); err != nil {
		return Result{}, probeErr(ProtocolNameDNS, stepQuery, err)
	}
	buf := make([]byte, dnsUDPBufferBytes)
	n, err := c.Read(buf)
	if err != nil {
		return Result{}, probeErr(ProtocolNameDNS, stepReply, err)
	}
	reply, err := parseDNSReply(buf[:n])
	if err != nil {
		return Result{}, err
	}
	if reply.id != id {
		return Result{}, errors.New("DNS response id mismatch")
	}
	res := Result{Extra: map[string]string{
		ExtraKeyDNSQuery:         name,
		ExtraKeyDNSQType:         qtypeName,
		ExtraKeyDNSRCode:         rcodeName(reply.rcode),
		ExtraKeyDNSAnswers:       strconv.Itoa(reply.answers),
		ExtraKeyDNSAddresses:     strings.Join(reply.addrs, ","),
		ExtraKeyDNSAuthoritative: strconv.FormatBool(reply.authoritative),
		ExtraKeyDNSAuthenticated: strconv.FormatBool(reply.authenticated),
		ExtraKeyDNSTruncated:     strconv.FormatBool(reply.truncated),
	}}
	if !dnsResponseOK(reply.rcode) {
		res.Failure = fmt.Sprintf("DNS query for %q returned %s", name, rcodeName(reply.rcode))
	}
	return res, nil
}

// firstNameserver returns the first `nameserver` entry of a resolv.conf-style
// file — the server the system resolver would ask first (with pppd's
// usepeerdns, the provider's resolver).
func firstNameserver(path string) (string, error) {
	data, err := hostfs.ReadFile(path)
	if err != nil {
		return "", probeErr(ProtocolNameDNS, stepDNSReadResolvConf, err)
	}
	for line := range strings.SplitSeq(string(data), resolvConfLineSep) {
		fields := strings.Fields(line)
		if len(fields) >= resolvConfMinFields && fields[resolvConfKeyIndex] == resolvConfNameserver {
			return fields[resolvConfValueIndex], nil
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
func dnsResponseOK(rcode int) bool {
	return rcode == dnsRCodeNoError || rcode == dnsRCodeNXDomain
}

func rcodeName(rcode int) string {
	switch rcode {
	case dnsRCodeNoError:
		return DNSRCodeNoErrorName
	case dnsRCodeFormErr:
		return dnsRCodeNameFormErr
	case dnsRCodeServFail:
		return dnsRCodeNameServFail
	case dnsRCodeNXDomain:
		return dnsRCodeNameNXDomain
	case dnsRCodeNotImp:
		return dnsRCodeNameNotImp
	case dnsRCodeRefused:
		return dnsRCodeNameRefused
	default:
		return dnsRCodeNameUnknown + strconv.Itoa(rcode)
	}
}

func dnsID() uint16 {
	var id [xid32Bytes]byte
	binary.BigEndian.PutUint32(id[:], randXID32())
	return binary.BigEndian.Uint16(id[:])
}

// buildDNSQuery builds a standard recursive query message (header + one question)
// for name and qtype, packed with dnsmessage. The AD bit is set so a validating
// resolver reports whether it authenticated the answer (RFC 6840 §5.7); a
// resolver that does not validate ignores it.
func buildDNSQuery(id uint16, name string, qtype uint16) ([]byte, error) {
	qname, err := dnsmessage.NewName(dnsFQDN(name))
	if err != nil {
		return nil, probeErr(ProtocolNameDNS, stepDNSBuildQuery, err)
	}
	msg := dnsmessage.Message{
		ID: id, RecursionDesired: true, AuthenticData: true,
		Questions: []dnsmessage.Question{{
			Name:  qname,
			Type:  dnsmessage.Type(qtype),
			Class: dnsmessage.ClassINET,
		}},
	}
	packed, err := msg.Pack()
	if err != nil {
		return nil, probeErr(ProtocolNameDNS, stepDNSPackQuery, err)
	}
	return packed, nil
}

// dnsFQDN returns name as a fully-qualified domain name (trailing dot), the form
// dnsmessage.NewName requires. An empty name becomes the root ".".
func dnsFQDN(name string) string {
	if strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}

// dnsReply is what parseDNSReply reads from a response: the header fields a
// check can assert on and the A/AAAA addresses of the answer section.
type dnsReply struct {
	id            uint16
	rcode         int
	answers       int
	addrs         []string
	authoritative bool // AA: the answer came from the zone's own data
	authenticated bool // AD: a validating resolver verified the DNSSEC chain
	truncated     bool // TC: the UDP reply was cut; the client should retry over TCP
}

// parseDNSReply parses a DNS response with dnsmessage: the id, RCODE, the
// header's answer count and flags, and the A/AAAA addresses (sorted). It errors
// on a too-short message or a query (QR=0). The answer section is parsed
// leniently — a malformed record stops collection and yields what was read so
// far — so a truncated reply still reports liveness rather than failing the
// probe.
func parseDNSReply(b []byte) (dnsReply, error) {
	var p dnsmessage.Parser
	hdr, err := p.Start(b)
	if err != nil {
		return dnsReply{}, probeErr(ProtocolNameDNS, stepDNSParseReply, err)
	}
	if !hdr.Response {
		return dnsReply{id: hdr.ID}, errors.New("not a DNS response (QR=0)")
	}
	reply := dnsReply{
		id:            hdr.ID,
		rcode:         int(hdr.RCode),
		authoritative: hdr.Authoritative,
		authenticated: hdr.AuthenticData,
		truncated:     hdr.Truncated,
	}
	if len(b) >= dnsHeaderBytes { // guaranteed by Parser.Start; keeps the slice bound explicit.
		reply.answers = int(binary.BigEndian.Uint16(b[dnsANCountStart:dnsANCountEnd]))
	}
	// Collect A/AAAA answers; a malformed question/answer section still leaves a
	// valid header for the liveness verdict, so it is not a probe error.
	if p.SkipAllQuestions() == nil {
		reply.addrs = dnsAnswerAddrs(&p)
	}
	return reply, nil
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
