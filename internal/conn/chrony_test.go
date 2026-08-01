package conn

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// bk1Tracking is a byte-exact RPY_Tracking payload captured from a live chronyd
// 4.8. The `chronyc tracking` it printed for the same state was:
//
//	Reference ID    : F0DC2D5E (2001:41d0:305:2100::e3ab)
//	Stratum         : 3
//	Ref time (UTC)  : Sat Aug 01 09:46:19 2026
//	System time     : 0.000044694 seconds slow of NTP time
//	Last offset     : +0.000006764 seconds
//	RMS offset      : 0.000026514 seconds
//	Frequency       : 2.560 ppm fast
//	Residual freq   : +0.000 ppm
//	Skew            : 0.024 ppm
//	Root delay      : 0.008008859 seconds
//	Root dispersion : 0.000537106 seconds
//	Update interval : 515.8 seconds
//	Leap status     : Normal
//
// Every field the probe decodes is asserted against those numbers below, so the
// wire layout is pinned to a real daemon rather than to our reading of it.
const bk1Tracking = "f0dc2d5e200141d003052100000000000000e3ab0002000000" +
	"030000000000006a6dc06b34167688e6bb75efe0e2f5d1e4de" +
	"6a8406a3cfa0eccf3438f8c1fce0f6833797ee8ccc901680f2bd"

// chronyActivity is an RPY_Activity payload whose five counters are all
// different, so decoding them out of wire order cannot pass.
const chronyActivity = "00000004000000010000000200000003" + "00000005"

// bk1TrackingRefTime is the reference timestamp encoded in bk1Tracking.
var bk1TrackingRefTime = time.Unix(1785577579, 873887368)

func decodeHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return b
}

// mustDecodeHex decodes a compile-time fixture, for the helpers that have no
// *testing.T to fail on.
func mustDecodeHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// chronyReply frames a payload as a successful reply to req.
func chronyReply(req []byte, cmd chronyCommand, payload []byte) []byte {
	b := make([]byte, chronyReplyHeaderBytes+len(payload))
	b[chronyRepOffVersion] = chronyProtoVersion
	b[chronyRepOffPktType] = chronyPktTypeReply
	binary.BigEndian.PutUint16(b[chronyRepOffCommand:], cmd.request)
	binary.BigEndian.PutUint16(b[chronyRepOffReply:], cmd.reply)
	binary.BigEndian.PutUint16(b[chronyRepOffStatus:], chronyStatusSuccess)
	binary.BigEndian.PutUint32(b[chronyRepOffSequence:], binary.BigEndian.Uint32(req[chronyReqOffSequence:]))
	copy(b[chronyReplyHeaderBytes:], payload)
	return b
}

// chronyRefusal frames a non-success reply to req, the way chronyd answers a
// command it will not serve.
func chronyRefusal(req []byte, status uint16) []byte {
	b := make([]byte, chronyReplyHeaderBytes)
	b[chronyRepOffVersion] = chronyProtoVersion
	b[chronyRepOffPktType] = chronyPktTypeReply
	binary.BigEndian.PutUint16(b[chronyRepOffCommand:], binary.BigEndian.Uint16(req[chronyReqOffCommand:]))
	binary.BigEndian.PutUint16(b[chronyRepOffReply:], 1) // RPY_NULL
	binary.BigEndian.PutUint16(b[chronyRepOffStatus:], status)
	binary.BigEndian.PutUint32(b[chronyRepOffSequence:], binary.BigEndian.Uint32(req[chronyReqOffSequence:]))
	return b
}

// fakeChronyd answers tracking with the given payload and, when serveSources is
// set, the two source-count commands; otherwise it refuses them the way an older
// or restricted daemon does.
func fakeChronyd(tracking []byte, serveSources bool) func(req []byte) []byte {
	return func(req []byte) []byte {
		if len(req) < chronyRequestHeaderBytes {
			return nil
		}
		switch binary.BigEndian.Uint16(req[chronyReqOffCommand:]) {
		case chronyCmdTracking.request:
			return chronyReply(req, chronyCmdTracking, tracking)
		case chronyCmdActivity.request:
			if !serveSources {
				return chronyRefusal(req, 1)
			}
			return chronyReply(req, chronyCmdActivity, mustDecodeHex(chronyActivity))
		case chronyCmdNSources.request:
			if !serveSources {
				return chronyRefusal(req, 1)
			}
			return chronyReply(req, chronyCmdNSources, []byte{0, 0, 0, 7})
		}
		return nil
	}
}

func TestChronyFloatDecode(t *testing.T) {
	// The raw exponents in a real reply (3, 11, 112..124 in bk1Tracking) all sit
	// outside 64..88, where sign-correcting the exponent before or after biasing
	// it by the coefficient width agree. Only the synthetic rows below separate
	// the two orderings, by exactly 2^128.
	cases := []struct {
		name string
		word uint32
		want float64
	}{
		{"zero", 0x00000000, 0},
		{"bk1 root delay", 0xf6833797, 0.008008859120309353},
		{"bk1 frequency ppm", 0x06a3cfa0, 2.5595474243164062},
		{"bk1 skew ppm", 0xf8c1fce0, 0.023680150508880615},
		{"bk1 update interval", 0x1680f2bd, 515.7927856445312},
		{"negative coefficient", 0xe0e2f5d1, 6.763941655663075e-06},
		{"divergent band, positive", 0xacbc614e, 8.365756817754061e-14},
		{"divergent band, negative", 0xad439eb2, -8.365756817754061e-14},
		{"divergent band, low edge", 0x80000001, 1.6155871338926322e-27},
		{"divergent band, high edge", 0xb0000001, 2.710505431213761e-20},
		{"just below the band", 0x7e000001, 274877906944},
		{"just above the band", 0xb2000001, 5.421010862427522e-20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b [4]byte
			binary.BigEndian.PutUint32(b[:], tc.word)
			got := chronyFloat(b[:], 0)
			if got != tc.want {
				t.Fatalf("chronyFloat(%#08x) = %v, want %v (off by %v)", tc.word, got, tc.want, got/tc.want)
			}
		})
	}
}

func TestChronyRequestPadding(t *testing.T) {
	// chronyd answers STT_BADPKTLENGTH unless a request is at least as long as
	// the reply it would produce, so each command has its own floor.
	cases := []struct {
		cmd  chronyCommand
		want int
	}{
		{chronyCmdTracking, 104},
		{chronyCmdNSources, 32},
		{chronyCmdActivity, 48},
	}
	for _, tc := range cases {
		t.Run(tc.cmd.name, func(t *testing.T) {
			req := buildChronyRequest(tc.cmd, 0xdeadbeef)
			if len(req) != tc.want {
				t.Fatalf("request length = %d, want %d", len(req), tc.want)
			}
			if req[chronyReqOffVersion] != chronyProtoVersion {
				t.Errorf("version = %d, want %d", req[chronyReqOffVersion], chronyProtoVersion)
			}
			if req[chronyReqOffPktType] != chronyPktTypeRequest {
				t.Errorf("packet type = %d, want %d", req[chronyReqOffPktType], chronyPktTypeRequest)
			}
			if got := binary.BigEndian.Uint16(req[chronyReqOffCommand:]); got != tc.cmd.request {
				t.Errorf("command = %d, want %d", got, tc.cmd.request)
			}
			if got := binary.BigEndian.Uint32(req[chronyReqOffSequence:]); got != 0xdeadbeef {
				t.Errorf("sequence = %#x, want 0xdeadbeef", got)
			}
		})
	}
}

func TestChronyReplyRejects(t *testing.T) {
	const seq = 0x11223344
	good := func() []byte {
		return chronyReply(buildChronyRequest(chronyCmdTracking, seq), chronyCmdTracking,
			make([]byte, chronyCmdTracking.payload))
	}
	// wantStale marks the rejections an exchange may retry past: only a reply
	// that answers a *different* request. A malformed one must stop the
	// exchange rather than have it keep reading through a real protocol error.
	cases := []struct {
		name      string
		mutate    func(b []byte) []byte
		wantErr   string
		wantStale bool
	}{
		{name: "short header", mutate: func(b []byte) []byte { return b[:10] }, wantErr: "short reply"},
		{name: "not a reply", mutate: func(b []byte) []byte {
			b[chronyRepOffPktType] = chronyPktTypeRequest
			return b
		}, wantErr: "not a reply"},
		{name: "sequence mismatch", mutate: func(b []byte) []byte {
			binary.BigEndian.PutUint32(b[chronyRepOffSequence:], seq+1)
			return b
		}, wantErr: errChronyStaleReply.Error(), wantStale: true},
		{name: "command mismatch", mutate: func(b []byte) []byte {
			binary.BigEndian.PutUint16(b[chronyRepOffCommand:], chronyCmdActivity.request)
			return b
		}, wantErr: errChronyStaleReply.Error(), wantStale: true},
		{name: "refused", mutate: func(b []byte) []byte {
			binary.BigEndian.PutUint16(b[chronyRepOffStatus:], 19)
			return b
		}, wantErr: "bad-pkt-length"},
		{name: "unknown status keeps the code", mutate: func(b []byte) []byte {
			binary.BigEndian.PutUint16(b[chronyRepOffStatus:], 200)
			return b
		}, wantErr: "status 200"},
		{name: "unexpected reply type", mutate: func(b []byte) []byte {
			binary.BigEndian.PutUint16(b[chronyRepOffReply:], 9)
			return b
		}, wantErr: "unexpected reply type"},
		{name: "short payload", mutate: func(b []byte) []byte {
			return b[:chronyReplyHeaderBytes+4]
		}, wantErr: "short payload"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseChronyReply(tc.mutate(good()), chronyCmdTracking, seq)
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.wantErr)
			}
			if got := errors.Is(err, errChronyStaleReply); got != tc.wantStale {
				t.Fatalf("errors.Is(err, errChronyStaleReply) = %v, want %v", got, tc.wantStale)
			}
		})
	}
	if _, err := parseChronyReply(good(), chronyCmdTracking, seq); err != nil {
		t.Fatalf("a well-formed reply must parse: %v", err)
	}
}

func TestChronyProbeExtras(t *testing.T) {
	port := serveUDPLoop(t, fakeChronyd(decodeHex(t, bk1Tracking), true))
	assertProbeExtras(t, chronyProtocol{}, port, map[string]string{
		// Shared with the ntp probe, so an expect: rule reads the same on both.
		extraStratum:          "3",
		extraLeap:             leapNameNone,
		extraReferenceID:      "F0DC2D5E",
		extraOffsetSeconds:    "0.000044694",
		extraRootDelayMS:      "8.009",
		extraRootDispersionMS: "0.537",
		// chrony-only.
		chronyExtraKeySynchronized:            "true",
		chronyExtraKeyReferenceAddress:        "2001:41d0:305:2100::e3ab",
		chronyExtraKeyReferenceTime:           bk1TrackingRefTime.UTC().Format(time.RFC3339),
		chronyExtraKeyLastOffsetSeconds:       "0.000006764",
		chronyExtraKeyRMSOffsetSeconds:        "0.000026514",
		chronyExtraKeyFrequencyPPM:            "2.560",
		chronyExtraKeyFrequencyAbsPPM:         "2.560",
		chronyExtraKeyResidualFrequencyPPM:    "0.000",
		chronyExtraKeyResidualFrequencyAbsPPM: "0.000",
		chronyExtraKeySkewPPM:                 "0.024",
		chronyExtraKeyUpdateIntervalSeconds:   "515.793",
		chronyExtraKeySourcesOnline:           "4",
		chronyExtraKeySourcesOffline:          "1",
		chronyExtraKeySourcesUnresolved:       "5",
		chronyExtraKeySources:                 "7",
	})
}

func TestChronyProbeReferenceID(t *testing.T) {
	// A stratum-1 refclock publishes an ASCII label, exactly like ntp; anything
	// else is chrony's hash of the peer address and stays hex.
	cases := []struct {
		name    string
		id      uint32
		stratum int
		want    string
	}{
		{"stratum 1 refclock label", 0x47505300, 1, "GPS"},
		{"stratum 1 pps", 0x50505300, 1, "PPS"},
		{"stratum 1 non-ascii falls back to hex", 0x00010203, 1, "00010203"},
		{"stratum 3 peer hash stays hex", 0xf0dc2d5e, 3, "F0DC2D5E"},
		{"unsynchronized", 0x00000000, 0, "00000000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chronyRefID(tc.id, tc.stratum); got != tc.want {
				t.Fatalf("chronyRefID(%#08x, %d) = %q, want %q", tc.id, tc.stratum, got, tc.want)
			}
		})
	}
}

func TestChronyProbeUnsynchronized(t *testing.T) {
	// A chronyd that is up but not disciplining the clock reports stratum 0 and
	// a "not synchronised" leap status. That is a live daemon, so the probe must
	// succeed and report the state rather than fail as if it were down.
	// Reuse the captured layout, overwriting only the two fields that say so.
	tracking := decodeHex(t, bk1Tracking)
	binary.BigEndian.PutUint16(tracking[chronyTrkOffStratum:], 0)
	binary.BigEndian.PutUint16(tracking[chronyTrkOffLeap:], 3) // LEAP_Unsynchronised
	port := serveUDPLoop(t, fakeChronyd(tracking, true))
	assertProbeExtras(t, chronyProtocol{}, port, map[string]string{
		extraStratum:               "0",
		extraLeap:                  leapNameUnsynchronized,
		chronyExtraKeySynchronized: "false",
	})
}

func TestChronyProbeDegradesWithoutSourceCounts(t *testing.T) {
	// A daemon that refuses the source-count commands still proves it is alive
	// through tracking; the counters are simply absent, and an expect: rule that
	// names one fails on its own.
	port := serveUDPLoop(t, fakeChronyd(decodeHex(t, bk1Tracking), false))
	res, err := chronyProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra[extraStratum] != "3" {
		t.Fatalf("tracking must still be reported: %v", res.Extra)
	}
	for _, key := range []string{chronyExtraKeySourcesOnline, chronyExtraKeySources} {
		if _, ok := res.Extra[key]; ok {
			t.Fatalf("%s must be absent when the daemon refuses the command", key)
		}
	}
}

func TestChronyProbeRefusedTracking(t *testing.T) {
	// Refusing tracking itself is fatal: without it there is no liveness proof.
	port := serveUDPLoop(t, func(req []byte) []byte { return chronyRefusal(req, 2) })
	_, err := chronyProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err == nil {
		t.Fatal("a refused tracking command must fail the probe")
	}
	if !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("error = %q, want it to name the refusal", err)
	}
}

func TestChronyProbeSkipsStaleReply(t *testing.T) {
	// A reply left over from an earlier command can arrive before the one we are
	// waiting for. The probe must skip it on the sequence echo and keep reading
	// rather than treat the daemon as broken.
	tracking := decodeHex(t, bk1Tracking)
	pc, port := listenUDPLoopback(t)
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, rerr := pc.ReadFrom(buf)
			if rerr != nil {
				return
			}
			req := buf[:n]
			reply := fakeChronyd(tracking, true)(req)
			if reply == nil {
				continue
			}
			if binary.BigEndian.Uint16(req[chronyReqOffCommand:]) == chronyCmdTracking.request {
				stale := chronyReply(req, chronyCmdTracking, tracking)
				binary.BigEndian.PutUint32(stale[chronyRepOffSequence:], 0xdeadbeef)
				_, _ = pc.WriteTo(stale, addr)
			}
			_, _ = pc.WriteTo(reply, addr)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := chronyProtocol{}.Probe(ctx, Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe must skip the stale reply: %v", err)
	}
	if res.Extra[extraStratum] != "3" {
		t.Fatalf("stratum = %q, want 3", res.Extra[extraStratum])
	}
}

func TestChronyProbeRefused(t *testing.T) {
	assertProbeRefused(t, chronyProtocol{}, deadPort(t))
}

func TestChronyUnixgramRoundTrip(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "chronyd.sock")
	pc, err := net.ListenPacket(networkUnixgram, socket)
	if err != nil {
		t.Skipf("unixgram sockets unavailable: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	// The server goroutine outlives the probe, so hand the peer address over a
	// channel rather than sharing a variable with the test body.
	peers := make(chan string, 8)
	go serveDatagrams(pc, fakeChronyd(decodeHex(t, bk1Tracking), true), func(addr net.Addr) {
		select {
		case peers <- addr.String():
		default:
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := chronyProtocol{}.Probe(ctx, Config{Socket: socket})
	if err != nil {
		t.Fatalf("probe over the command socket: %v", err)
	}
	if res.Extra[extraStratum] != "3" {
		t.Fatalf("stratum = %q, want 3", res.Extra[extraStratum])
	}
	// chronyd answers with sendto() to the client's bound address, so an unnamed
	// client socket would leave it nowhere to reply.
	select {
	case peer := <-peers:
		if peer == "" || peer == "@" {
			t.Fatalf("client peer address = %q, want a bound pathname", peer)
		}
	default:
		t.Fatal("the fake daemon never saw a datagram")
	}
	// The bound socket must not outlive the probe: it sits in chronyd's own run
	// directory, and one leak per check cycle would fill it.
	leftover, err := filepath.Glob(filepath.Join(dir, "sermo-chrony.*.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftover) != 0 {
		t.Fatalf("client sockets left behind: %v", leftover)
	}
}

func TestChronyUnixgramRejectsLongPath(t *testing.T) {
	// sun_path is 108 bytes; without an explicit guard the bind fails with a
	// bare "invalid argument" that says nothing an operator can act on.
	dir := filepath.Join(t.TempDir(), strings.Repeat("d", 120))
	_, err := chronyDialUnix(context.Background(), filepath.Join(dir, "chronyd.sock"))
	if err == nil {
		t.Fatal("an over-long socket directory must be rejected")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %q, want it to explain the path length limit", err)
	}
}

func TestChronyTrackingFieldsReferenceAge(t *testing.T) {
	extra, err := chronyTrackingFields(decodeHex(t, bk1Tracking), bk1TrackingRefTime.Add(90*time.Second))
	if err != nil {
		t.Fatalf("decode tracking: %v", err)
	}
	if got := extra[chronyExtraKeyReferenceAgeSeconds]; got != "90.000" {
		t.Fatalf("reference_age_seconds = %q, want 90.000", got)
	}
	// A clock that is behind its own reference must not report a negative age.
	extra, err = chronyTrackingFields(decodeHex(t, bk1Tracking), bk1TrackingRefTime.Add(-time.Minute))
	if err != nil {
		t.Fatalf("decode tracking: %v", err)
	}
	if got := extra[chronyExtraKeyReferenceAgeSeconds]; got != "0.000" {
		t.Fatalf("reference_age_seconds = %q, want it clamped to 0.000", got)
	}
}

func TestChronyRefAddress(t *testing.T) {
	tracking := decodeHex(t, bk1Tracking)
	if got := chronyRefAddress(tracking[chronyTrkOffIPAddr:]); got != "2001:41d0:305:2100::e3ab" {
		t.Fatalf("IPv6 reference address = %q", got)
	}
	v4 := make([]byte, chronyIPAddrBytes)
	copy(v4, net.IPv4(10, 0, 0, 7).To4())
	binary.BigEndian.PutUint16(v4[chronyIPAddrFamilyOffset:], chronyIPAddrFamilyInet4)
	if got := chronyRefAddress(v4); got != "10.0.0.7" {
		t.Fatalf("IPv4 reference address = %q, want 10.0.0.7", got)
	}
	// An unsynchronized daemon has no peer; the key is then omitted entirely.
	if got := chronyRefAddress(make([]byte, chronyIPAddrBytes)); got != "" {
		t.Fatalf("unspecified reference address = %q, want empty", got)
	}
}

func TestChronyStatusName(t *testing.T) {
	// The codes below were confirmed against a live chronyd 4.8: a bad version
	// answers 18, an unknown command 3, a short request 19 and a privileged
	// command over the UDP port 2.
	runMapCases(t, "chronyStatusName", chronyStatusName, map[uint16]string{
		0:   "success",
		2:   "unauthorized",
		3:   "invalid",
		18:  "bad-pkt-version",
		19:  "bad-pkt-length",
		250: "status 250",
	})
}

func TestChronyTimespec(t *testing.T) {
	b := make([]byte, 12)
	binary.BigEndian.PutUint32(b[chronyTimespecSecHighOffset:], 0)
	binary.BigEndian.PutUint32(b[chronyTimespecSecLowOffset:], 1785577579)
	binary.BigEndian.PutUint32(b[chronyTimespecNsecOffset:], 873887368)
	if got := chronyTimespec(b); !got.Equal(bk1TrackingRefTime) {
		t.Fatalf("chronyTimespec = %v, want %v", got, bk1TrackingRefTime)
	}
	// chrony writes TV_NOHIGHSEC when it has no high word to send.
	binary.BigEndian.PutUint32(b[chronyTimespecSecHighOffset:], chronyTimespecNoHighSec)
	if got := chronyTimespec(b); !got.Equal(bk1TrackingRefTime) {
		t.Fatalf("chronyTimespec with TV_NOHIGHSEC = %v, want %v", got, bk1TrackingRefTime)
	}
	if got := chronyTimespec(make([]byte, 12)); !got.IsZero() {
		t.Fatalf("an unset reference time must decode to the zero time, got %v", got)
	}
}

func TestChronyActivityFields(t *testing.T) {
	// Five distinct counters, so decoding them out of wire order cannot pass.
	extra := map[string]string{}
	chronyActivityFields(decodeHex(t, chronyActivity), extra)
	want := map[string]string{
		chronyExtraKeySourcesOnline:       "4",
		chronyExtraKeySourcesOffline:      "1",
		chronyExtraKeySourcesBurstOnline:  "2",
		chronyExtraKeySourcesBurstOffline: "3",
		chronyExtraKeySourcesUnresolved:   "5",
	}
	for key, val := range want {
		if extra[key] != val {
			t.Errorf("%s = %q, want %q", key, extra[key], val)
		}
	}
}

func TestChronyActivityFieldsShortPayload(t *testing.T) {
	extra := map[string]string{}
	chronyActivityFields(make([]byte, 4), extra)
	if len(extra) != 0 {
		t.Fatalf("a short activity payload must add nothing, got %v", extra)
	}
}

func TestChronyFloatMatchesReferenceScaling(t *testing.T) {
	// An independent restatement of chrony's UTI_FloatNetworkToHost, used to
	// sweep the whole exponent range rather than the handful of rows above.
	reference := func(u uint32) float64 {
		exp := int32(u>>chronyFloatCoefBits) << (32 - chronyFloatExpBits) >> (32 - chronyFloatExpBits)
		coef := int32(u<<chronyFloatExpBits) >> chronyFloatExpBits
		return float64(coef) * math.Pow(2, float64(exp-chronyFloatCoefBits))
	}
	var b [4]byte
	for rawExp := range uint32(1 << chronyFloatExpBits) {
		for _, coef := range []uint32{1, 0x1ffffff, 0x1000000, 0xffffff} {
			u := rawExp<<chronyFloatCoefBits | coef
			binary.BigEndian.PutUint32(b[:], u)
			if got, want := chronyFloat(b[:], 0), reference(u); got != want {
				t.Fatalf("chronyFloat(%#08x) = %v, want %v", u, got, want)
			}
		}
	}
}

func TestChronyClientSocketIsRemovedOnDialFailure(t *testing.T) {
	// Dialing a socket nobody is listening on must not leave our client socket
	// behind either.
	dir := t.TempDir()
	_, err := chronyDialUnix(context.Background(), filepath.Join(dir, "absent.sock"))
	if err == nil {
		t.Fatal("dialing an absent command socket must fail")
	}
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed dial left files behind: %v", entries)
	}
}

func TestChronyOptionalCommandsDoNotBurnTheBudget(t *testing.T) {
	// chronyd rate-limits by DISCARDING command responses rather than refusing
	// them. Blocking on the shared deadline for a dropped best-effort reply
	// reported success while pinning the worker for the whole check timeout and
	// recording that timeout as the probe's latency.
	tracking := decodeHex(t, bk1Tracking)
	port := serveUDPLoop(t, func(req []byte) []byte {
		if binary.BigEndian.Uint16(req[chronyReqOffCommand:]) != chronyCmdTracking.request {
			return nil // silently drop activity and n_sources
		}
		return chronyReply(req, chronyCmdTracking, tracking)
	})

	budget := 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	start := time.Now()
	res, err := chronyProtocol{}.Probe(ctx, Config{Host: "127.0.0.1", Port: port})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("tracking answered, so the probe must succeed: %v", err)
	}
	if res.Extra[extraStratum] != "3" {
		t.Fatalf("stratum = %q, want 3", res.Extra[extraStratum])
	}
	if _, ok := res.Extra[chronyExtraKeySourcesOnline]; ok {
		t.Fatal("a dropped activity reply must leave the counters absent")
	}
	if limit := budget / 2; elapsed > limit {
		t.Fatalf("probe took %s of a %s budget; the optional commands must be bounded separately", elapsed, budget)
	}
}

func TestChronyOptionalTimeoutNeverExtendsTheCallerDeadline(t *testing.T) {
	// A caller deadline tighter than chronyOptionalTimeout must still win.
	tracking := decodeHex(t, bk1Tracking)
	port := serveUDPLoop(t, func(req []byte) []byte {
		if binary.BigEndian.Uint16(req[chronyReqOffCommand:]) != chronyCmdTracking.request {
			return nil
		}
		return chronyReply(req, chronyCmdTracking, tracking)
	})
	budget := chronyOptionalTimeout / 5
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	start := time.Now()
	_, _ = chronyProtocol{}.Probe(ctx, Config{Host: "127.0.0.1", Port: port})
	if elapsed := time.Since(start); elapsed > chronyOptionalTimeout {
		t.Fatalf("probe took %s, longer than the %s optional cap despite a %s caller budget",
			elapsed, chronyOptionalTimeout, budget)
	}
}

func TestChronyTimespecRejectsAnOutOfRangeHighWord(t *testing.T) {
	// int64(high)<<32 wrapped negative for high=0xffffffff, so a corrupt field
	// yielded a 19th-century reference_time and a ~136-year age instead of
	// being dropped.
	b := make([]byte, 12)
	binary.BigEndian.PutUint32(b[chronyTimespecSecHighOffset:], 0xffffffff)
	binary.BigEndian.PutUint32(b[chronyTimespecSecLowOffset:], 0)
	if got := chronyTimespec(b); !got.IsZero() {
		t.Fatalf("chronyTimespec with an out-of-range high word = %v, want the zero time", got)
	}

	// The observable effect: the reference fields are omitted rather than
	// reported as a bogus century-old timestamp.
	tracking := decodeHex(t, bk1Tracking)
	binary.BigEndian.PutUint32(tracking[chronyTrkOffRefTime+chronyTimespecSecHighOffset:], 0xffffffff)
	extra, err := chronyTrackingFields(tracking, time.Now())
	if err != nil {
		t.Fatalf("decode tracking: %v", err)
	}
	for _, key := range []string{chronyExtraKeyReferenceTime, chronyExtraKeyReferenceAgeSeconds} {
		if val, ok := extra[key]; ok {
			t.Errorf("%s = %q, want it omitted for an unusable reference time", key, val)
		}
	}
}

func TestChronyClientSocketCloseIsIdempotent(t *testing.T) {
	// A second Close must not delete a pathname a later probe may now own.
	dir := t.TempDir()
	socket := filepath.Join(dir, "chronyd.sock")
	pc, err := net.ListenPacket(networkUnixgram, socket)
	if err != nil {
		t.Skipf("unixgram sockets unavailable: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	c, err := chronyDialUnix(context.Background(), socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	// Something else takes the name before the accidental second close.
	local := c.(*unlinkOnCloseConn).path
	if err := os.WriteFile(local, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	if _, err := os.Stat(local); err != nil {
		t.Fatalf("a second Close deleted a file it no longer owns: %v", err)
	}
}
