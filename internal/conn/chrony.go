package conn

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func init() { Register(chronyProtocol{}, protocolAliasChronyd) }

// DefaultChronySocket is chronyd's command socket on a stock install: the
// default target of a clock watch's `then.makestep` action.
const DefaultChronySocket = "/run/chrony/chronyd.sock"

// candm wire framing (chrony's candm.h). All fields are big-endian.
const (
	chronyProtoVersion       = 6
	chronyPktTypeRequest     = 1
	chronyPktTypeReply       = 2
	chronyRequestHeaderBytes = 20
	chronyReplyHeaderBytes   = 28
	chronyStatusSuccess      = 0
	// chronyStatusFailed is STT_FAILED: the command was recognised and authorized
	// but the daemon could not carry it out — what a chronyd started with -x
	// reports for a step, having had control of the system clock disabled.
	chronyStatusFailed     = 1
	chronyReplyBufferBytes = 512
	// chronyStaleReplyLimit bounds how many mismatched datagrams a single
	// exchange skips before giving up. chronyd answers one reply per request, so
	// a mismatch means a late reply from an earlier command on the same socket.
	chronyStaleReplyLimit = 4
	// chronyOptionalTimeout bounds the two best-effort commands. chronyd is
	// local, so a reply that has not arrived in this long is not coming — and
	// waiting out the check's whole budget for it would report success while
	// pinning a worker and inflating the measured latency.
	chronyOptionalTimeout = 250 * time.Millisecond
	// chronyMakeStepTimeout bounds the one mutating exchange. chronyd is local
	// and acknowledges immediately; a watch action must not pin a worker for the
	// whole cycle budget waiting for a reply that is not coming.
	chronyMakeStepTimeout = 3 * time.Second
)

// Request header field offsets. The attempt counter at offset 6 is left at the
// zero make() already wrote: the probe never retries within one exchange.
const (
	chronyReqOffVersion  = 0
	chronyReqOffPktType  = 1
	chronyReqOffCommand  = 4
	chronyReqOffSequence = 8
)

// Reply header field offsets.
const (
	chronyRepOffVersion  = 0
	chronyRepOffPktType  = 1
	chronyRepOffCommand  = 4
	chronyRepOffReply    = 6
	chronyRepOffStatus   = 8
	chronyRepOffSequence = 16
)

// chronyCommand describes one candm request/reply pair.
type chronyCommand struct {
	name    string
	request uint16
	reply   uint16
	payload int
}

// candm request codes (REQ_*), the reply codes they produce (RPY_*) and the
// payload each reply carries.
const (
	chronyReqNSources = 14
	chronyReqTracking = 33
	chronyReqMakeStep = 43
	chronyReqActivity = 44

	// chronyRpyNull is the empty acknowledgement a state-changing command
	// answers with, and the reply chronyd sends when it refuses one.
	chronyRpyNull     = 1
	chronyRpyNSources = 2
	chronyRpyTracking = 5
	chronyRpyActivity = 12

	chronyPayloadNull     = 0
	chronyPayloadNSources = 4
	chronyPayloadTracking = 76
	chronyPayloadActivity = 20
)

var (
	chronyCmdNSources = chronyCommand{
		name: "n_sources", request: chronyReqNSources, reply: chronyRpyNSources, payload: chronyPayloadNSources,
	}
	chronyCmdTracking = chronyCommand{
		name: "tracking", request: chronyReqTracking, reply: chronyRpyTracking, payload: chronyPayloadTracking,
	}
	chronyCmdActivity = chronyCommand{
		name: "activity", request: chronyReqActivity, reply: chronyRpyActivity, payload: chronyPayloadActivity,
	}
	chronyCmdMakeStep = chronyCommand{
		name: "makestep", request: chronyReqMakeStep, reply: chronyRpyNull, payload: chronyPayloadNull,
	}
)

// requestBytes is chronyd's anti-amplification floor: it answers RPY_NULL with
// STT_BADPKTLENGTH unless the request datagram is at least as long as the reply
// it would produce. Our requests carry no payload of their own, so the floor is
// the full reply length and every request is zero-padded up to it.
func (c chronyCommand) requestBytes() int { return chronyReplyHeaderBytes + c.payload }

// candm status codes (chrony's candm.h STT_*).
var chronyStatusNames = map[uint16]string{
	0:  "success",
	1:  "failed",
	2:  "unauthorized",
	3:  "invalid",
	4:  "no-such-source",
	5:  "invalid-timestamp",
	6:  "not-enabled",
	7:  "bad-subnet",
	8:  "access-allowed",
	9:  "access-denied",
	10: "no-host-access",
	11: "source-already-known",
	12: "too-many-sources",
	13: "no-rtc",
	14: "bad-rtc-file",
	15: "inactive",
	16: "bad-sample",
	17: "invalid-af",
	18: "bad-pkt-version",
	19: "bad-pkt-length",
}

// RPY_Tracking payload field offsets.
const (
	chronyTrkOffRefID              = 0
	chronyTrkOffIPAddr             = 4
	chronyTrkOffStratum            = 24
	chronyTrkOffLeap               = 26
	chronyTrkOffRefTime            = 28
	chronyTrkOffCurrentCorrection  = 40
	chronyTrkOffLastOffset         = 44
	chronyTrkOffRMSOffset          = 48
	chronyTrkOffFreqPPM            = 52
	chronyTrkOffResidFreqPPM       = 56
	chronyTrkOffSkewPPM            = 60
	chronyTrkOffRootDelay          = 64
	chronyTrkOffRootDispersion     = 68
	chronyTrkOffLastUpdateInterval = 72
)

// chronyActivityKeys names the five int32 counters of an RPY_Activity payload,
// in wire order.
var chronyActivityKeys = [...]string{
	chronyExtraKeySourcesOnline,
	chronyExtraKeySourcesOffline,
	chronyExtraKeySourcesBurstOnline,
	chronyExtraKeySourcesBurstOffline,
	chronyExtraKeySourcesUnresolved,
}

// IPAddr layout inside a reply: a 16-byte address union followed by the address
// family and padding (chrony's addressing.h).
const (
	chronyIPAddrBytes        = 20
	chronyIPAddrFamilyOffset = 16
	chronyIPAddrFamilyInet4  = 1
	chronyIPAddrFamilyInet6  = 2
)

// Timespec layout inside a reply: seconds split high/low, then nanoseconds.
const (
	chronyTimespecSecHighOffset = 0
	chronyTimespecSecLowOffset  = 4
	chronyTimespecNsecOffset    = 8
	// chronyTimespecNoHighSec marks an unset high word (chrony's TV_NOHIGHSEC).
	chronyTimespecNoHighSec = 0x7fffffff
	chronyTimespecSecShift  = 32
)

// chrony's on-wire Float: a sign-extended coefficient scaled by a sign-extended
// exponent (chrony's util.c, FLOAT_COEF_BITS / FLOAT_EXP_BITS).
const (
	chronyFloatCoefBits = 25
	chronyFloatExpBits  = 7
)

const (
	chronyPPMPrecision      = 3
	chronyIntervalPrecision = 3
	// chronyInt32Bytes and chronyInt32Bits are the width of signed counters in
	// a reply payload.
	chronyInt32Bytes = 4
	chronyInt32Bits  = 32
	// chronySunPathMax is the sockaddr_un sun_path capacity; the bound the client
	// socket path must respect, leaving room for the terminating NUL.
	chronySunPathMax = 108
	// chronyClientSocketMode lets the unprivileged chronyd (it drops to its own
	// user) write the reply back to our client socket. This is what chronyc does.
	chronyClientSocketMode = 0o666
	chronyClientSocketFmt  = "sermo-chrony.%d.%08x.sock"
)

// chrony-only Result.Extra keys. The fields chrony shares with NTP (stratum,
// offset_seconds, leap, root_delay_ms, root_dispersion_ms, reference_id) use the
// shared extra* keys so an expect: rule reads identically against either probe.
const (
	chronyExtraKeyFrequencyPPM            = "frequency_ppm"
	chronyExtraKeyFrequencyAbsPPM         = "frequency_abs_ppm"
	chronyExtraKeyLastOffsetSeconds       = "last_offset_seconds"
	chronyExtraKeyReferenceAddress        = "reference_address"
	chronyExtraKeyReferenceAgeSeconds     = "reference_age_seconds"
	chronyExtraKeyReferenceTime           = "reference_time"
	chronyExtraKeyResidualFrequencyPPM    = "residual_frequency_ppm"
	chronyExtraKeyResidualFrequencyAbsPPM = "residual_frequency_abs_ppm"
	chronyExtraKeyRMSOffsetSeconds        = "rms_offset_seconds"
	chronyExtraKeySkewPPM                 = "skew_ppm"
	chronyExtraKeySources                 = "sources"
	chronyExtraKeySourcesBurstOffline     = "sources_burst_offline"
	chronyExtraKeySourcesBurstOnline      = "sources_burst_online"
	chronyExtraKeySourcesOffline          = "sources_offline"
	chronyExtraKeySourcesOnline           = "sources_online"
	chronyExtraKeySourcesUnresolved       = "sources_unresolved"
	chronyExtraKeySynchronized            = "synchronized"
	chronyExtraKeyUpdateIntervalSeconds   = "update_interval_seconds"
)

// chronyProtocol probes a local chronyd over candm, chrony's command and
// monitoring protocol (default UDP port 323, or chronyd's Unix command socket).
// It is the counterpart of the ntp probe for hosts running chrony: a chrony
// client-only configuration serves no NTP on port 123 at all, so the ntp probe
// cannot see it, while candm reports the daemon's own view of the clock.
//
// The probe reads REQ_TRACKING (mandatory — the liveness proof) plus
// REQ_ACTIVITY and REQ_N_SOURCES for the source counts. It never issues a
// command that changes state: the one mutating command Sermo speaks —
// REQ_MAKESTEP, in MakeStep below — is deliberately kept out of this probe and
// out of every check, reachable only from a watch's `then.makestep` action and
// only over the Unix command socket. TestChronyProbeNeverMutates pins that
// separation. The default UDP transport is chronyd's monitoring-only channel,
// which refuses privileged commands outright. No auth.
type chronyProtocol struct{}

func (chronyProtocol) Name() string       { return ProtocolNameChrony }
func (chronyProtocol) DefaultPort() int   { return defaultPortChrony }
func (chronyProtocol) RequiresUser() bool { return false }

func (chronyProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	c, err := chronyDial(ctx, cfg)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Close() }()

	payload, err := chronyExchange(c, chronyCmdTracking)
	if err != nil {
		return Result{}, err
	}
	extra, err := chronyTrackingFields(payload, time.Now())
	if err != nil {
		return Result{}, err
	}

	// The source counts are best-effort. A chronyd that refuses them (an older
	// build, or a restricted command socket) still reports tracking, and that is
	// a live daemon — failing the whole probe would report it as down. An
	// expect: rule naming a missing field fails on its own with conn's
	// "field not available", which is the accurate signal.
	//
	// They get a short deadline of their own so a *dropped* reply costs a few
	// milliseconds instead of the check's whole budget: chronyd rate-limits by
	// discarding command responses rather than refusing them, and blocking on
	// the shared deadline would report success while pinning the worker and
	// inflating the reported latency to the full timeout.
	chronyLimitOptional(ctx, c)
	if p, aerr := chronyExchange(c, chronyCmdActivity); aerr == nil {
		chronyActivityFields(p, extra)
	}
	if p, serr := chronyExchange(c, chronyCmdNSources); serr == nil {
		extra[chronyExtraKeySources] = chronyCount(p)
	}
	return Result{Extra: extra}, nil
}

// chronyLimitOptional caps the wait for the best-effort commands at
// chronyOptionalTimeout, never extending a deadline the caller set tighter.
// Nothing reads after them, so the original deadline needs no restoring.
func chronyLimitOptional(ctx context.Context, c net.Conn) {
	limit := time.Now().Add(chronyOptionalTimeout)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(limit) {
		limit = deadline
	}
	_ = c.SetReadDeadline(limit)
}

// MakeStep asks the local chronyd to correct the system clock immediately: the
// REQ_MAKESTEP command `chronyc makestep` sends. It is the only candm command
// Sermo issues that changes state, it is deliberately not part of any probe or
// check, and it is reached only from a clock watch's `then.makestep` action.
//
// It is socket-only by construction. chronyd's UDP command port is its
// monitoring-only channel and answers a privileged command with STT_UNAUTH
// (verified against chronyd 4.8), so there is no host/port form to offer.
// An empty socket means DefaultChronySocket.
//
// Success means chronyd accepted the command and applied the correction it had
// already computed — a clock *discontinuity*, not a slew. The caller owns the
// policy deciding that is warranted; conn only carries the command.
func MakeStep(ctx context.Context, socket string) error {
	if socket == "" {
		socket = DefaultChronySocket
	}
	ctx, cancel := context.WithTimeout(ctx, chronyMakeStepTimeout)
	defer cancel()

	c, err := chronyDialUnix(ctx, socket)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	_, err = chronyExchange(c, chronyCmdMakeStep)
	return err
}

// chronyDial opens the command channel: cfg.Socket selects chronyd's Unix
// command socket, otherwise the UDP command port.
func chronyDial(ctx context.Context, cfg Config) (net.Conn, error) {
	if cfg.Socket != "" {
		return chronyDialUnix(ctx, cfg.Socket)
	}
	return dialUDPDeadline(ctx, cfg, defaultPortChrony)
}

// chronyDialUnix opens a datagram channel to chronyd's command socket. Unlike
// every other Unix-socket probe here, chronyd's socket is SOCK_DGRAM and it
// replies with sendto() to the client's bound address, so the client must bind
// a named socket of its own — the stream dialUnix cannot reach it and an
// unnamed autobind gives chronyd nowhere to answer. This mirrors chronyc, which
// creates its own socket next to chronyd's, relaxes the mode so the
// unprivileged chronyd can write the reply, and unlinks it afterwards.
//
// An AF_UNIX socket has no egress link, so cfg.Interface (SO_BINDTODEVICE) does
// not apply on this path — the same rule dialUnix follows for acpid, fail2ban,
// docker and libvirt. Use the UDP transport when a probe must be pinned to an
// interface.
func chronyDialUnix(ctx context.Context, socket string) (net.Conn, error) {
	local := filepath.Join(filepath.Dir(socket), fmt.Sprintf(chronyClientSocketFmt, os.Getpid(), randXID32()))
	if len(local) >= chronySunPathMax {
		return nil, fmt.Errorf("chrony client socket path %q exceeds %d bytes; use a shorter socket directory", local, chronySunPathMax-1)
	}
	c, err := net.DialUnix(networkUnixgram,
		&net.UnixAddr{Name: local, Net: networkUnixgram},
		&net.UnixAddr{Name: socket, Net: networkUnixgram})
	if err != nil {
		// The local address is bound before the peer is resolved, so a failed
		// dial usually leaves the pathname on disk — but not when the bind
		// itself failed because something else already holds that name, which
		// we must not delete.
		if !errors.Is(err, syscall.EADDRINUSE) {
			_ = os.Remove(local)
		}
		return nil, probeErr(ProtocolNameChrony, stepChronyClientSocket, err)
	}
	// chronyd drops privileges to its own user and cannot write a reply to a
	// socket only we may write. The socket lives in chronyd's own run directory,
	// which is not world-accessible; a forged reply is additionally rejected by
	// the per-request random sequence we echo-check.
	if err := os.Chmod(local, chronyClientSocketMode); err != nil {
		_ = c.Close()
		_ = os.Remove(local)
		return nil, probeErr(ProtocolNameChrony, stepChronyClientSocketMode, err)
	}
	applyDeadline(ctx, c)
	return &unlinkOnCloseConn{Conn: c, path: local}, nil
}

// unlinkOnCloseConn removes a client socket's pathname when the connection
// closes. Go unlinks a bound address only for ListenUnix with SetUnlinkOnClose,
// never for DialUnix, so without this every probe cycle would leave a socket
// file behind in chronyd's run directory.
type unlinkOnCloseConn struct {
	net.Conn
	path   string
	closed bool
}

func (c *unlinkOnCloseConn) Close() error {
	if c.closed {
		return net.ErrClosed
	}
	c.closed = true
	err := c.Conn.Close()
	_ = os.Remove(c.path)
	if err != nil {
		return probeErr(ProtocolNameChrony, stepClose, err)
	}
	return nil
}

// chronyExchange sends one command and returns its reply payload.
func chronyExchange(c net.Conn, cmd chronyCommand) ([]byte, error) {
	sequence := randXID32()
	if _, err := c.Write(buildChronyRequest(cmd, sequence)); err != nil {
		return nil, probeErr(ProtocolNameChrony, stepRequest, err)
	}
	buf := make([]byte, chronyReplyBufferBytes)
	for range chronyStaleReplyLimit {
		n, err := c.Read(buf)
		if err != nil {
			return nil, probeErr(ProtocolNameChrony, stepReply, err)
		}
		payload, err := parseChronyReply(buf[:n], cmd, sequence)
		if errors.Is(err, errChronyStaleReply) {
			continue // a late reply to an earlier command on this socket
		}
		return payload, err
	}
	return nil, fmt.Errorf("chrony %s: no matching reply", cmd.name)
}

// errChronyStaleReply marks a reply that answers a different request than the
// one being awaited, so an exchange keeps reading instead of failing.
var errChronyStaleReply = errors.New("reply belongs to another request")

// buildChronyRequest renders a command as a zero-padded request datagram.
func buildChronyRequest(cmd chronyCommand, sequence uint32) []byte {
	b := make([]byte, cmd.requestBytes())
	b[chronyReqOffVersion] = chronyProtoVersion
	b[chronyReqOffPktType] = chronyPktTypeRequest
	binary.BigEndian.PutUint16(b[chronyReqOffCommand:], cmd.request)
	binary.BigEndian.PutUint32(b[chronyReqOffSequence:], sequence)
	return b
}

// parseChronyReply validates a reply datagram against the command that produced
// it and returns its payload. The reply's protocol version is not checked:
// chronyd answers STT_BADPKTVERSION rather than replying at all when it does
// not speak ours, so a successful reply is by definition the same version.
func parseChronyReply(b []byte, cmd chronyCommand, sequence uint32) ([]byte, error) {
	if len(b) < chronyReplyHeaderBytes {
		return nil, fmt.Errorf("chrony %s: short reply (%d bytes)", cmd.name, len(b))
	}
	if pkt := b[chronyRepOffPktType]; pkt != chronyPktTypeReply {
		return nil, fmt.Errorf("chrony %s: not a reply (packet type %d)", cmd.name, pkt)
	}
	if got := binary.BigEndian.Uint32(b[chronyRepOffSequence:]); got != sequence {
		return nil, fmt.Errorf("chrony %s: sequence %d does not match %d: %w", cmd.name, got, sequence, errChronyStaleReply)
	}
	if got := binary.BigEndian.Uint16(b[chronyRepOffCommand:]); got != cmd.request {
		return nil, fmt.Errorf("chrony %s: reply answers command %d: %w", cmd.name, got, errChronyStaleReply)
	}
	if status := binary.BigEndian.Uint16(b[chronyRepOffStatus:]); status != chronyStatusSuccess {
		return nil, fmt.Errorf("chrony %s refused: %s", cmd.name, chronyStatusName(status))
	}
	if got := binary.BigEndian.Uint16(b[chronyRepOffReply:]); got != cmd.reply {
		return nil, fmt.Errorf("chrony %s: unexpected reply type %d", cmd.name, got)
	}
	payload := b[chronyReplyHeaderBytes:]
	if len(payload) < cmd.payload {
		return nil, fmt.Errorf("chrony %s: short payload (%d of %d bytes)", cmd.name, len(payload), cmd.payload)
	}
	return payload[:cmd.payload], nil
}

func chronyStatusName(status uint16) string {
	return codeName(status, chronyStatusNames, "status %d")
}

// chronyTrackingFields decodes an RPY_Tracking payload into Result.Extra. now is
// the reference for the age of chronyd's last clock update.
func chronyTrackingFields(b []byte, now time.Time) (map[string]string, error) {
	if len(b) < chronyCmdTracking.payload {
		return nil, fmt.Errorf("chrony tracking: short payload (%d bytes)", len(b))
	}
	stratum := int(binary.BigEndian.Uint16(b[chronyTrkOffStratum:]))
	leap := leapName(int(binary.BigEndian.Uint16(b[chronyTrkOffLeap:])))
	refID := binary.BigEndian.Uint32(b[chronyTrkOffRefID:])
	frequency := chronyFloat(b, chronyTrkOffFreqPPM)
	residual := chronyFloat(b, chronyTrkOffResidFreqPPM)

	extra := map[string]string{
		extraStratum:          strconv.Itoa(stratum),
		extraLeap:             leap,
		extraReferenceID:      chronyRefID(refID, stratum),
		extraOffsetSeconds:    secondsString(chronyFloat(b, chronyTrkOffCurrentCorrection)),
		extraRootDelayMS:      msString(chronyFloat(b, chronyTrkOffRootDelay)),
		extraRootDispersionMS: msString(chronyFloat(b, chronyTrkOffRootDispersion)),

		chronyExtraKeySynchronized:            strconv.FormatBool(Synchronized(stratum, leap)),
		chronyExtraKeyLastOffsetSeconds:       secondsString(chronyFloat(b, chronyTrkOffLastOffset)),
		chronyExtraKeyRMSOffsetSeconds:        secondsString(chronyFloat(b, chronyTrkOffRMSOffset)),
		chronyExtraKeyFrequencyPPM:            chronyPPM(frequency),
		chronyExtraKeyFrequencyAbsPPM:         chronyPPM(math.Abs(frequency)),
		chronyExtraKeyResidualFrequencyPPM:    chronyPPM(residual),
		chronyExtraKeyResidualFrequencyAbsPPM: chronyPPM(math.Abs(residual)),
		chronyExtraKeySkewPPM:                 chronyPPM(chronyFloat(b, chronyTrkOffSkewPPM)),
		chronyExtraKeyUpdateIntervalSeconds:   chronyInterval(chronyFloat(b, chronyTrkOffLastUpdateInterval)),
	}
	if addr := chronyRefAddress(b[chronyTrkOffIPAddr:]); addr != "" {
		extra[chronyExtraKeyReferenceAddress] = addr
	}
	if ref := chronyTimespec(b[chronyTrkOffRefTime:]); !ref.IsZero() {
		extra[chronyExtraKeyReferenceTime] = ref.UTC().Format(time.RFC3339)
		extra[chronyExtraKeyReferenceAgeSeconds] = chronyInterval(math.Max(0, now.Sub(ref).Seconds()))
	}
	return extra, nil
}

// chronyActivityFields adds the RPY_Activity source counters to extra.
func chronyActivityFields(b []byte, extra map[string]string) {
	if len(b) < chronyCmdActivity.payload {
		return
	}
	for i, key := range chronyActivityKeys {
		extra[key] = chronyCount(b[i*chronyInt32Bytes:])
	}
}

// chronyCount reads a signed 32-bit counter from the head of b.
func chronyCount(b []byte) string {
	n := int64(binary.BigEndian.Uint32(b))
	if n > math.MaxInt32 {
		n -= 1 << chronyInt32Bits
	}
	return strconv.FormatInt(n, 10)
}

// chronyFloat decodes chrony's 32-bit on-wire float at offset off.
//
// The raw 7-bit exponent is sign-corrected *before* it is biased by the
// coefficient width (chrony's UTI_FloatNetworkToHost). Doing it the other way
// round agrees for most values but is wrong by 2^128 for raw exponents in
// 64..88 — every magnitude below about 4.5e-13, which a converged chronyd
// reports routinely for its residual frequency and RMS offset.
func chronyFloat(b []byte, off int) float64 {
	u := binary.BigEndian.Uint32(b[off:])

	exp := int32(u >> chronyFloatCoefBits)
	if exp >= 1<<(chronyFloatExpBits-1) {
		exp -= 1 << chronyFloatExpBits
	}
	exp -= chronyFloatCoefBits

	coef := int32(u & (1<<chronyFloatCoefBits - 1))
	if coef >= 1<<(chronyFloatCoefBits-1) {
		coef -= 1 << chronyFloatCoefBits
	}
	// Ldexp is coefficient × 2^exp exactly, which is the operation on the wire.
	return math.Ldexp(float64(coef), int(exp))
}

// chronyTimespec decodes chrony's split-seconds timestamp, returning the zero
// time when the daemon has no reference time yet.
func chronyTimespec(b []byte) time.Time {
	high := binary.BigEndian.Uint32(b[chronyTimespecSecHighOffset:])
	if high == chronyTimespecNoHighSec {
		high = 0
	}
	sec := uint64(high)<<chronyTimespecSecShift | uint64(binary.BigEndian.Uint32(b[chronyTimespecSecLowOffset:]))
	// A never-synced daemon sends 0. Anything past the int64 epoch range is a
	// corrupt or hostile field: taking it would wrap negative and report a
	// 19th-century reference whose age reads as a century.
	if sec == 0 || sec > math.MaxInt64 {
		return time.Time{}
	}
	return time.Unix(int64(sec), int64(binary.BigEndian.Uint32(b[chronyTimespecNsecOffset:])))
}

// chronyRefID renders the reference identifier the way chronyc does: an ASCII
// refclock label (e.g. "GPS", "PPS") for a stratum-1 source, matching the ntp
// probe, otherwise the raw 8-hex identifier.
//
// Hex rather than a dotted address because chrony derives the identifier of an
// IPv6 peer by *hashing* it — rendering that as an IP would invent a host that
// does not exist. For an IPv4 peer chrony does use the address verbatim, so
// this is the one shared key the two probes may render differently for the same
// 32-bit value (ntp: "192.168.1.10", chrony: "C0A8010A"). Match on
// reference_address instead when a rule must work against either probe.
func chronyRefID(id uint32, stratum int) string {
	if stratum == primaryStratum {
		if label := refIDLabel(id); label != "" {
			return label
		}
	}
	return fmt.Sprintf("%08X", id)
}

// chronyRefAddress decodes the peer address chronyd is synchronized to, or ""
// when it has none (a refclock, or an unsynchronized daemon).
func chronyRefAddress(b []byte) string {
	if len(b) < chronyIPAddrBytes {
		return ""
	}
	switch binary.BigEndian.Uint16(b[chronyIPAddrFamilyOffset:]) {
	case chronyIPAddrFamilyInet4:
		return net.IP(b[:net.IPv4len]).String()
	case chronyIPAddrFamilyInet6:
		return net.IP(b[:net.IPv6len]).String()
	default:
		return ""
	}
}

// chronyPPM renders a frequency error, chronyInterval a duration whose
// millisecond resolution is enough (an update interval, an age). Both round to
// what chronyc prints; offsets go through the shared secondsString instead.
func chronyPPM(v float64) string            { return fixedString(v, chronyPPMPrecision) }
func chronyInterval(seconds float64) string { return fixedString(seconds, chronyIntervalPrecision) }
