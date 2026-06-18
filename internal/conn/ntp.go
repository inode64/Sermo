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
)

func init() { Register(ntpProtocol{}) }

// ntpProtocol probes an NTP server (RFC 5905) with the github.com/beevik/ntp
// client: it queries the server and verifies it answers with a usable time. The
// query is dialed through BindDialer so an `interface:` setting still pins the
// egress link (SO_BINDTODEVICE), like every other probe. No auth.
type ntpProtocol struct{}

func (ntpProtocol) Name() string       { return "ntp" }
func (ntpProtocol) DefaultPort() int   { return 123 }
func (ntpProtocol) RequiresUser() bool { return false }

func (ntpProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 123
	}

	opt := ntp.QueryOptions{
		Timeout: ntpTimeout(ctx),
		// Route the UDP query through the shared dialer so interface binding works
		// identically to the other probes; beevik would otherwise dial directly.
		Dialer: func(_, remote string) (net.Conn, error) {
			return BindDialer(cfg.Interface).DialContext(ctx, "udp", remote)
		},
	}
	resp, err := ntp.QueryWithOptions(net.JoinHostPort(host, strconv.Itoa(port)), opt)
	if err != nil {
		return Result{}, err
	}
	if err := resp.Validate(); err != nil {
		return Result{}, err
	}
	stratum := int(resp.Stratum)
	if !ntpHealthy(stratum) {
		return Result{}, fmt.Errorf("server not serving time (stratum %d)", stratum)
	}

	extra := ntpExtraFields(resp)
	extra["stratum"] = strconv.Itoa(stratum)
	extra["offset_seconds"] = strconv.FormatFloat(resp.ClockOffset.Seconds(), 'f', 6, 64)
	return Result{Extra: extra}, nil
}

// ntpTimeout derives the query timeout from the context deadline, falling back to
// beevik's own default (0 means "use the library default") when none is set.
func ntpTimeout(ctx context.Context) time.Duration {
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d > 0 {
			return d
		}
		return time.Nanosecond
	}
	return 0
}

// ntpExtraFields decodes the diagnostic fields RFC 5905 carries alongside the
// timestamps: the leap indicator, clock precision and the root delay/dispersion
// (the server's estimated distance and error to the reference clock), plus the
// reference identifier. These let an expect: rule assert sync quality, e.g.
// leap == none or root_dispersion_ms below a threshold.
func ntpExtraFields(resp *ntp.Response) map[string]string {
	leaps := [...]string{"none", "add-second", "del-second", "unsynchronized"}
	leap := "unknown"
	if int(resp.Leap) < len(leaps) {
		leap = leaps[resp.Leap]
	}
	return map[string]string{
		"leap":               leap,
		"precision_seconds":  strconv.FormatFloat(resp.Precision.Seconds(), 'g', 4, 64),
		"root_delay_ms":      strconv.FormatFloat(resp.RootDelay.Seconds()*1000, 'f', 3, 64),
		"root_dispersion_ms": strconv.FormatFloat(resp.RootDispersion.Seconds()*1000, 'f', 3, 64),
		"reference_id":       ntpRefID(resp.ReferenceID, int(resp.Stratum)),
	}
}

// ntpRefID renders the 4-byte reference identifier: an ASCII refclock label
// (e.g. "GPS", "PPS") for a stratum-1 server, otherwise the dotted IPv4 of the
// upstream server it syncs from.
func ntpRefID(id uint32, stratum int) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], id)
	if stratum <= 1 {
		return strings.TrimRight(string(b[:]), "\x00 ")
	}
	return net.IP(b[:]).String()
}

// ntpHealthy reports whether the server is synchronized (stratum 1..15); stratum
// 0 is kiss-o'-death and 16 is unsynchronized.
func ntpHealthy(stratum int) bool {
	return stratum >= 1 && stratum <= 15
}
