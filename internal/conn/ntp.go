package conn

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"
)

func init() { Register(ntpProtocol{}) }

// ntpEpochOffset is the seconds between the NTP epoch (1900-01-01) and the Unix
// epoch (1970-01-01).
const ntpEpochOffset = 2208988800

// ntpProtocol probes an NTP server natively (RFC 5905): it sends a client
// request over UDP and verifies the server answers with a usable time. No auth.
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

	c, err := BindDialer(cfg.Interface).DialContext(ctx, "udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()
	applyDeadline(ctx, c)

	t1 := time.Now()
	if _, err := c.Write(buildNTPRequest()); err != nil {
		return Result{}, err
	}
	buf := make([]byte, 48)
	n, err := c.Read(buf)
	if err != nil {
		return Result{}, err
	}
	t4 := time.Now()

	mode, stratum, t2, t3, err := parseNTPResponse(buf[:n])
	if err != nil {
		return Result{}, err
	}
	if !ntpHealthy(mode, stratum) {
		return Result{}, fmt.Errorf("server not serving time (mode %d, stratum %d)", mode, stratum)
	}

	// Clock offset = ((T2 - T1) + (T3 - T4)) / 2 (RFC 5905).
	t1u := float64(t1.UnixNano()) / 1e9
	t4u := float64(t4.UnixNano()) / 1e9
	offset := ((t2 - t1u) + (t3 - t4u)) / 2

	extra := ntpExtraFields(buf[:n], stratum)
	extra["stratum"] = strconv.Itoa(stratum)
	extra["offset_seconds"] = strconv.FormatFloat(offset, 'f', 6, 64)
	return Result{Extra: extra}, nil
}

// ntpExtraFields decodes the diagnostic fields RFC 5905 carries alongside the
// timestamps: the leap indicator, clock precision and the root delay/dispersion
// (the server's estimated distance and error to the reference clock), plus the
// reference identifier. b must be a full 48-byte packet (parseNTPResponse
// validates the length first). These let an expect: rule assert sync quality,
// e.g. leap == none or root_dispersion_ms below a threshold.
func ntpExtraFields(b []byte, stratum int) map[string]string {
	leap := [...]string{"none", "add-second", "del-second", "unsynchronized"}[b[0]>>6]
	precision := math.Pow(2, float64(int8(b[3]))) // log2 seconds -> seconds
	rootDelay := float64(binary.BigEndian.Uint32(b[4:8])) / (1 << 16)
	rootDisp := float64(binary.BigEndian.Uint32(b[8:12])) / (1 << 16)
	return map[string]string{
		"leap":               leap,
		"precision_seconds":  strconv.FormatFloat(precision, 'g', 4, 64),
		"root_delay_ms":      strconv.FormatFloat(rootDelay*1000, 'f', 3, 64),
		"root_dispersion_ms": strconv.FormatFloat(rootDisp*1000, 'f', 3, 64),
		"reference_id":       ntpRefID(b[12:16], stratum),
	}
}

// ntpRefID renders the 4-byte reference identifier: an ASCII refclock label
// (e.g. "GPS", "PPS") for a stratum-1 server, otherwise the dotted IPv4 of the
// upstream server it syncs from.
func ntpRefID(b []byte, stratum int) string {
	if stratum <= 1 {
		return strings.TrimRight(string(b), "\x00 ")
	}
	return net.IP(b).String()
}

// buildNTPRequest builds a 48-byte NTPv4 client request (LI=0, VN=4, Mode=3).
func buildNTPRequest() []byte {
	req := make([]byte, 48)
	req[0] = 0x23
	return req
}

// parseNTPResponse extracts the mode, stratum and the server receive (T2) and
// transmit (T3) timestamps (as Unix seconds) from an NTP packet.
func parseNTPResponse(b []byte) (mode, stratum int, t2, t3 float64, err error) {
	if len(b) < 48 {
		return 0, 0, 0, 0, errors.New("short NTP response")
	}
	mode = int(b[0] & 0x07)
	stratum = int(b[1])
	t2 = ntpTimeToUnix(b[32:40])
	t3 = ntpTimeToUnix(b[40:48])
	return mode, stratum, t2, t3, nil
}

// ntpTimeToUnix converts an 8-byte NTP timestamp (32-bit seconds since 1900 +
// 32-bit fraction) to Unix seconds as a float.
func ntpTimeToUnix(b []byte) float64 {
	sec := binary.BigEndian.Uint32(b[0:4])
	frac := binary.BigEndian.Uint32(b[4:8])
	return float64(sec) - ntpEpochOffset + float64(frac)/(1<<32)
}

// ntpHealthy reports whether the reply is a server-mode (4) packet from a
// synchronized server (stratum 1..15); stratum 0 is kiss-o'-death and 16 is
// unsynchronized.
func ntpHealthy(mode, stratum int) bool {
	return mode == 4 && stratum >= 1 && stratum <= 15
}
