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
	ntpExtraKeyPrecisionSeconds = "precision_seconds"

	ntpKissCodeUnknown = "unknown"

	ntpMaxHealthyStratum    = 15
	ntpPrecisionSignificant = 4
	ntpFormatCompact        = 'g'
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
	extra[extraOffsetSeconds] = secondsString(resp.ClockOffset.Seconds())
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
	return map[string]string{
		extraLeap:                   leapName(int(resp.Leap)),
		ntpExtraKeyPrecisionSeconds: strconv.FormatFloat(resp.Precision.Seconds(), ntpFormatCompact, ntpPrecisionSignificant, formatBits),
		extraRootDelayMS:            msString(resp.RootDelay.Seconds()),
		extraRootDispersionMS:       msString(resp.RootDispersion.Seconds()),
		extraReferenceID:            ntpRefID(resp.ReferenceID, int(resp.Stratum)),
	}
}

// ntpRefID renders the 4-byte reference identifier: an ASCII refclock label
// (e.g. "GPS", "PPS") for a stratum-1 server, otherwise the dotted IPv4 of the
// upstream server it syncs from. A stratum-1 identifier that does not spell a
// printable label falls back to the same dotted rendering rather than to an
// empty field, which would drop the key from the result entirely.
func ntpRefID(id uint32, stratum int) string {
	if stratum <= primaryStratum {
		if label := refIDLabel(id); label != "" {
			return label
		}
	}
	var b [refIDBytes]byte
	binary.BigEndian.PutUint32(b[:], id)
	return net.IP(b[:]).String()
}

// ntpHealthy reports whether the server is synchronized (stratum 1..15); stratum
// 0 is kiss-o'-death and 16 is unsynchronized.
func ntpHealthy(stratum int) bool {
	return stratum >= primaryStratum && stratum <= ntpMaxHealthyStratum
}
