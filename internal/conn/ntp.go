package conn

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/beevik/ntp"

	"sermo/internal/netutil"
)

func init() { Register(ntpProtocol{}) }

const (
	ntpExtraKeyLeap             = "leap"
	ntpExtraKeyPrecisionSeconds = "precision_seconds"
	ntpExtraKeyRootDelayMS      = "root_delay_ms"
	ntpExtraKeyRootDispersionMS = "root_dispersion_ms"
	ntpExtraKeyReferenceID      = "reference_id"

	ntpLeapNone           = "none"
	ntpLeapAddSecond      = "add-second"
	ntpLeapDelSecond      = "del-second"
	ntpLeapUnsynchronized = "unsynchronized"
	ntpLeapUnknown        = "unknown"
	ntpKissCodeUnknown    = "unknown"

	ntpRefIDBytes              = 4
	ntpPrimaryStratum          = 1
	ntpMinHealthyStratum       = 1
	ntpMaxHealthyStratum       = 15
	ntpMillisecondsPerSecond   = 1000
	ntpOffsetPrecision         = 6
	ntpPrecisionSignificant    = 4
	ntpRootDelayPrecision      = 3
	ntpRootDispersionPrecision = 3
	ntpFormatFixed             = 'f'
	ntpFormatCompact           = 'g'
	ntpFormatBits              = 64
)

// ntpProtocol probes an NTP server (RFC 5905) with the github.com/beevik/ntp
// client: it queries the server and verifies it answers with a usable time. The
// query is dialed through BindDialer so an `interface:` setting still pins the
// egress link (SO_BINDTODEVICE), like every other probe. No auth.
type ntpProtocol struct{}

func (ntpProtocol) Name() string       { return ProtocolNameNTP }
func (ntpProtocol) DefaultPort() int   { return defaultPortNTP }
func (ntpProtocol) RequiresUser() bool { return false }

func (ntpProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	opt := ntp.QueryOptions{
		Timeout: ntpTimeout(ctx),
		// Route the UDP query through the shared dialer so interface binding works
		// identically to the other probes; beevik would otherwise dial directly.
		Dialer: func(_, remote string) (net.Conn, error) {
			return BindDialer(cfg.Interface).DialContext(ctx, networkUDP, remote)
		},
	}
	resp, err := ntp.QueryWithOptions(cfg.addrDefaults(defaultPortNTP), opt)
	if err != nil {
		return Result{}, err
	}
	// A stratum-0 reply is a kiss-of-death: the server answered but is unable or
	// unwilling to serve time, and only its kiss code says why (STEP while it is
	// still stepping the clock, INIT before it syncs, RATE when it is rate
	// limiting us, DENY when access is refused). Report that reason; the
	// library's bare "kiss of death received" leaves an operator with nothing
	// actionable, and Validate would return exactly that.
	if resp.IsKissOfDeath() {
		return Result{}, fmt.Errorf("server not serving time (kiss code %s)", ntpKissCode(resp))
	}
	if err := resp.Validate(); err != nil {
		return Result{}, err
	}
	stratum := int(resp.Stratum)
	if !ntpHealthy(stratum) {
		return Result{}, fmt.Errorf("server not serving time (stratum %d)", stratum)
	}

	extra := ntpExtraFields(resp)
	extra[extraStratum] = strconv.Itoa(stratum)
	extra[extraOffsetSeconds] = strconv.FormatFloat(resp.ClockOffset.Seconds(), ntpFormatFixed, ntpOffsetPrecision, ntpFormatBits)
	return Result{Extra: extra}, nil
}

// ntpTimeout derives the query timeout from the context deadline, falling back to
// beevik's own default (0 means "use the library default") when none is set.
func ntpTimeout(ctx context.Context) time.Duration {
	return netutil.TimeoutFromContext(ctx, 0)
}

// ntpKissCode returns the kiss code of a stratum-0 response. RFC 5905 §7.4
// leaves the field free-form, so a server may send none at all.
func ntpKissCode(resp *ntp.Response) string {
	if code := strings.TrimSpace(resp.KissCode); code != "" {
		return code
	}
	return ntpKissCodeUnknown
}

// ntpExtraFields decodes the diagnostic fields RFC 5905 carries alongside the
// timestamps: the leap indicator, clock precision and the root delay/dispersion
// (the server's estimated distance and error to the reference clock), plus the
// reference identifier. These let an expect: rule assert sync quality, e.g.
// leap == none or root_dispersion_ms below a threshold.
func ntpExtraFields(resp *ntp.Response) map[string]string {
	leaps := [...]string{ntpLeapNone, ntpLeapAddSecond, ntpLeapDelSecond, ntpLeapUnsynchronized}
	leap := ntpLeapUnknown
	if int(resp.Leap) < len(leaps) {
		leap = leaps[resp.Leap]
	}
	return map[string]string{
		ntpExtraKeyLeap:             leap,
		ntpExtraKeyPrecisionSeconds: strconv.FormatFloat(resp.Precision.Seconds(), ntpFormatCompact, ntpPrecisionSignificant, ntpFormatBits),
		ntpExtraKeyRootDelayMS:      strconv.FormatFloat(resp.RootDelay.Seconds()*ntpMillisecondsPerSecond, ntpFormatFixed, ntpRootDelayPrecision, ntpFormatBits),
		ntpExtraKeyRootDispersionMS: strconv.FormatFloat(resp.RootDispersion.Seconds()*ntpMillisecondsPerSecond, ntpFormatFixed, ntpRootDispersionPrecision, ntpFormatBits),
		ntpExtraKeyReferenceID:      ntpRefID(resp.ReferenceID, int(resp.Stratum)),
	}
}

// ntpRefID renders the 4-byte reference identifier: an ASCII refclock label
// (e.g. "GPS", "PPS") for a stratum-1 server, otherwise the dotted IPv4 of the
// upstream server it syncs from.
func ntpRefID(id uint32, stratum int) string {
	var b [ntpRefIDBytes]byte
	binary.BigEndian.PutUint32(b[:], id)
	if stratum <= ntpPrimaryStratum {
		return strings.TrimRight(string(b[:]), "\x00 ")
	}
	return net.IP(b[:]).String()
}

// ntpHealthy reports whether the server is synchronized (stratum 1..15); stratum
// 0 is kiss-o'-death and 16 is unsynchronized.
func ntpHealthy(stratum int) bool {
	return stratum >= ntpMinHealthyStratum && stratum <= ntpMaxHealthyStratum
}
