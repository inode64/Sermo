package checks

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"sermo/internal/conn"
)

func TestBuildClockCheck(t *testing.T) {
	built, warns := Build(map[string]any{
		"clock": map[string]any{
			CheckKeyType:              CheckTypeClock,
			CheckKeyServers:           []any{"time1.example", "time2.example"},
			CheckKeyMaxOffset:         "2s",
			CheckKeyMaxStratum:        4,
			CheckKeyMaxRootDispersion: "250ms",
			CheckKeyPort:              123,
		},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("clock check should build: warns=%v", warns)
	}
	chk := built[0].Check.(clockCheck)
	if chk.maxOffset != 2*time.Second || chk.maxStratum != 4 || chk.maxRootDispersion != 250*time.Millisecond {
		t.Fatalf("clock thresholds = %+v", chk)
	}
	if len(chk.servers) != 2 || chk.servers[0] != "time1.example" || chk.port != 123 {
		t.Fatalf("clock target = %+v", chk)
	}
}

func TestBuildClockCheckValidationWarnings(t *testing.T) {
	tests := []struct {
		name  string
		entry map[string]any
		want  string
	}{
		{
			name:  "servers required",
			entry: map[string]any{CheckKeyType: CheckTypeClock, CheckKeyMaxOffset: "2s"},
			want:  "requires servers",
		},
		{
			name:  "max_offset required",
			entry: map[string]any{CheckKeyType: CheckTypeClock, CheckKeyServers: []any{"time.example"}},
			want:  "requires max_offset",
		},
		{
			name: "bad stratum",
			entry: map[string]any{
				CheckKeyType:       CheckTypeClock,
				CheckKeyServers:    []any{"time.example"},
				CheckKeyMaxOffset:  "2s",
				CheckKeyMaxStratum: 16,
			},
			want: "max_stratum",
		},
		{
			name: "bad root dispersion",
			entry: map[string]any{
				CheckKeyType:              CheckTypeClock,
				CheckKeyServers:           []any{"time.example"},
				CheckKeyMaxOffset:         "2s",
				CheckKeyMaxRootDispersion: "0s",
			},
			want: "max_root_dispersion",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, warns := Build(map[string]any{"clock": tt.entry}, Deps{DefaultTimeout: time.Second})
			if len(warns) == 0 || !strings.Contains(warns[0], tt.want) {
				t.Fatalf("warns = %v, want %q", warns, tt.want)
			}
		})
	}
}

func TestClockCheckRun(t *testing.T) {
	tests := []struct {
		name            string
		check           clockCheck
		wantOK          bool
		wantUnavailable bool
		wantServer      string
		wantMessage     string
	}{
		{
			name: "within offset",
			check: testClockCheck([]string{"time.example"}, map[string]conn.Result{
				"time.example": testClockResult("0.250000", "3", "10.000"),
			}, nil),
			wantOK:     true,
			wantServer: "time.example",
		},
		{
			name: "tries next server",
			check: testClockCheck([]string{"bad.example", "time.example"}, map[string]conn.Result{
				"time.example": testClockResult("-0.125000", "2", "10.000"),
			}, map[string]error{"bad.example": errors.New("timeout")}),
			wantOK:     true,
			wantServer: "time.example",
		},
		{
			name: "offset too high uses best observed sample",
			check: testClockCheck([]string{"slow.example", "less-slow.example"}, map[string]conn.Result{
				"slow.example":      testClockResult("5.000000", "2", "10.000"),
				"less-slow.example": testClockResult("3.000000", "2", "10.000"),
			}, nil),
			wantOK:      false,
			wantServer:  "less-slow.example",
			wantMessage: "max_offset",
		},
		{
			name: "stratum too high",
			check: testClockCheck([]string{"time.example"}, map[string]conn.Result{
				"time.example": testClockResult("0.100000", "5", "10.000"),
			}, nil),
			wantOK:      false,
			wantServer:  "time.example",
			wantMessage: "max_stratum",
		},
		{
			name: "root dispersion too high",
			check: testClockCheck([]string{"time.example"}, map[string]conn.Result{
				"time.example": testClockResult("0.100000", "2", "500.000"),
			}, nil),
			wantOK:      false,
			wantServer:  "time.example",
			wantMessage: "max_root_dispersion",
		},
		{
			name: "missing offset fails",
			check: testClockCheck([]string{"time.example"}, map[string]conn.Result{
				"time.example": {Extra: map[string]string{DataKeyStratum: "2"}},
			}, nil),
			wantOK:          false,
			wantUnavailable: true,
			wantMessage:     "no usable ntp sample",
		},
		{
			name:            "all probes fail",
			check:           testClockCheck([]string{"time.example"}, nil, map[string]error{"time.example": errors.New("timeout")}),
			wantOK:          false,
			wantUnavailable: true,
			wantMessage:     "no usable ntp sample",
		},
		{
			// An unsynchronized source reports stratum 0 with an offset near
			// zero, so without an explicit guard it would look like the best
			// sample available and satisfy every threshold.
			name: "unsynchronized ntp sample fails despite max_stratum",
			check: testClockCheck([]string{"time.example"}, map[string]conn.Result{
				"time.example": testClockUnsynchronized(),
			}, nil),
			wantOK:      false,
			wantServer:  "time.example",
			wantMessage: "unsynchronized",
		},
		{
			name: "chrony sample within thresholds",
			check: testChronyClockCheck("", map[string]conn.Result{
				conn.DefaultHost: testChronyResult(),
			}),
			wantOK:     true,
			wantServer: conn.DefaultHost,
		},
		{
			name: "unsynchronized chronyd fails",
			check: testChronyClockCheck("", map[string]conn.Result{
				conn.DefaultHost: testClockUnsynchronized(),
			}),
			wantOK:      false,
			wantServer:  conn.DefaultHost,
			wantMessage: "unsynchronized",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := tt.check.Run(context.Background())
			if res.OK != tt.wantOK {
				t.Fatalf("OK = %v, want %v: %s", res.OK, tt.wantOK, res.Message)
			}
			if res.Unavailable != tt.wantUnavailable {
				t.Fatalf("Unavailable = %v, want %v: %s", res.Unavailable, tt.wantUnavailable, res.Message)
			}
			if tt.wantMessage != "" && !strings.Contains(res.Message, tt.wantMessage) {
				t.Fatalf("message = %q, want %q", res.Message, tt.wantMessage)
			}
			if tt.wantServer != "" && res.Data[DataKeyServer] != tt.wantServer {
				t.Fatalf("server = %v, want %q (data=%v)", res.Data[DataKeyServer], tt.wantServer, res.Data)
			}
			if res.OK {
				if got := res.Data[DataKeyValue]; got != res.Data[DataKeyOffsetAbsSeconds] {
					t.Fatalf("value = %v, offset_abs_seconds = %v", got, res.Data[DataKeyOffsetAbsSeconds])
				}
			}
		})
	}
}

func testClockCheck(servers []string, results map[string]conn.Result, errs map[string]error) clockCheck {
	return clockCheck{
		name: "clock", timeout: time.Second,
		source:            ClockSourceNTP,
		servers:           servers,
		port:              123,
		maxOffset:         2 * time.Second,
		maxStratum:        4,
		maxRootDispersion: 250 * time.Millisecond,
		probe: func(_ context.Context, cfg conn.Config) (conn.Result, error) {
			if err := errs[cfg.Host]; err != nil {
				return conn.Result{}, err
			}
			res, ok := results[cfg.Host]
			if !ok {
				return conn.Result{}, errors.New("unexpected host")
			}
			return res, nil
		},
	}
}

func testClockResult(offset, stratum, rootDispersion string) conn.Result {
	return conn.Result{Extra: map[string]string{
		DataKeyOffsetSeconds:    offset,
		DataKeyStratum:          stratum,
		DataKeyLeap:             "none",
		DataKeyPrecisionSeconds: "0.000001",
		DataKeyRootDelayMS:      "1.000",
		DataKeyRootDispersionMS: rootDispersion,
		DataKeyReferenceID:      "GPS",
	}}
}

// testClockUnsynchronized is the sample a time source that is running but not
// disciplining the clock returns: stratum 0, an offset near zero and a leap
// status saying so.
func testClockUnsynchronized() conn.Result {
	return conn.Result{Extra: map[string]string{
		DataKeyOffsetSeconds:    "0.000000",
		DataKeyStratum:          "0",
		DataKeyLeap:             "unsynchronized",
		DataKeyRootDispersionMS: "1.000",
	}}
}

// testChronyResult is a chronyd tracking sample, carrying the daemon-only
// diagnostics alongside the fields it shares with ntp. The offset is the one
// captured from the live daemon the chrony probe's fixtures come from.
func testChronyResult() conn.Result {
	return conn.Result{Extra: map[string]string{
		DataKeyOffsetSeconds:      "0.000044694",
		DataKeyStratum:            "3",
		DataKeyLeap:               "none",
		DataKeyRootDelayMS:        "8.009",
		DataKeyRootDispersionMS:   "0.537",
		DataKeyReferenceID:        "F0DC2D5E",
		DataKeyReferenceAddress:   "2001:41d0:305:2100::e3ab",
		DataKeySynchronized:       "true",
		DataKeySkewPPM:            "0.024",
		DataKeyFrequencyPPM:       "2.560",
		DataKeyResidualFreqPPM:    "0.000",
		DataKeyRMSOffsetSeconds:   "0.000026514",
		DataKeyLastOffsetSeconds:  "0.000006764",
		DataKeyUpdateIntervalSecs: "515.793",
		DataKeyReferenceAgeSecs:   "12.000",
		DataKeySources:            "7",
		DataKeySourcesOnline:      "4",
		DataKeySourcesOffline:     "1",
		DataKeySourcesUnresolved:  "2",
	}}
}

// testChronyClockCheck builds a clock check reading a local chronyd. A non-empty
// socket selects the command socket instead of host:port.
func testChronyClockCheck(socket string, results map[string]conn.Result) clockCheck {
	return clockCheck{
		name: "clock", timeout: time.Second,
		source:            ClockSourceChrony,
		servers:           []string{conn.DefaultHost},
		port:              323,
		socket:            socket,
		maxOffset:         time.Second,
		maxStratum:        4,
		maxRootDispersion: 250 * time.Millisecond,
		probe: func(_ context.Context, cfg conn.Config) (conn.Result, error) {
			res, ok := results[cfg.Host]
			if !ok {
				return conn.Result{}, errors.New("unexpected host")
			}
			return res, nil
		},
	}
}

// testChronySocket is chronyd's well-known command socket path.
const testChronySocket = "/run/chrony/chronyd.sock"

func TestBuildClockCheckChronySource(t *testing.T) {
	tests := []struct {
		name       string
		entry      map[string]any
		wantHost   string
		wantPort   int
		wantSocket string
	}{
		{
			name: "defaults to the local daemon on chrony's command port",
			entry: map[string]any{
				CheckKeyType: CheckTypeClock, CheckKeySource: ClockSourceChrony, CheckKeyMaxOffset: "1s",
			},
			wantHost: conn.DefaultHost,
			wantPort: 323,
		},
		{
			name: "explicit host and port",
			entry: map[string]any{
				CheckKeyType: CheckTypeClock, CheckKeySource: ClockSourceChrony, CheckKeyMaxOffset: "1s",
				CheckKeyHost: "10.0.0.2", CheckKeyPort: 3230,
			},
			wantHost: "10.0.0.2",
			wantPort: 3230,
		},
		{
			name: "command socket",
			entry: map[string]any{
				CheckKeyType: CheckTypeClock, CheckKeySource: ClockSourceChrony, CheckKeyMaxOffset: "1s",
				CheckKeySocket: testChronySocket,
			},
			wantHost:   conn.DefaultHost,
			wantPort:   323,
			wantSocket: testChronySocket,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			built, warns := Build(map[string]any{"clock": tt.entry}, Deps{DefaultTimeout: time.Second})
			if len(warns) != 0 || len(built) != 1 {
				t.Fatalf("chrony clock check should build: warns=%v", warns)
			}
			chk := built[0].Check.(clockCheck)
			if chk.source != ClockSourceChrony {
				t.Fatalf("source = %q, want %q", chk.source, ClockSourceChrony)
			}
			if len(chk.servers) != 1 || chk.servers[0] != tt.wantHost {
				t.Fatalf("servers = %v, want [%s]", chk.servers, tt.wantHost)
			}
			if chk.port != tt.wantPort || chk.socket != tt.wantSocket {
				t.Fatalf("target = %s:%d socket=%q, want %s:%d socket=%q",
					chk.servers[0], chk.port, chk.socket, tt.wantHost, tt.wantPort, tt.wantSocket)
			}
		})
	}
}

func TestBuildClockCheckSourceWarnings(t *testing.T) {
	tests := []struct {
		name  string
		entry map[string]any
		want  string
	}{
		{
			name: "unknown source",
			entry: map[string]any{
				CheckKeyType: CheckTypeClock, CheckKeySource: "timesyncd", CheckKeyMaxOffset: "1s",
			},
			want: "source must be ntp or chrony",
		},
		{
			name: "servers with the chrony source",
			entry: map[string]any{
				CheckKeyType: CheckTypeClock, CheckKeySource: ClockSourceChrony, CheckKeyMaxOffset: "1s",
				CheckKeyServers: []any{"pool.ntp.org"},
			},
			want: "servers is only valid with source: ntp",
		},
		{
			name: "socket with the ntp source",
			entry: map[string]any{
				CheckKeyType: CheckTypeClock, CheckKeyMaxOffset: "1s",
				CheckKeyServers: []any{"pool.ntp.org"}, CheckKeySocket: testChronySocket,
			},
			want: "socket is only valid with source: chrony",
		},
		{
			name: "max_offset still required for chrony",
			entry: map[string]any{
				CheckKeyType: CheckTypeClock, CheckKeySource: ClockSourceChrony,
			},
			want: "requires max_offset",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, warns := Build(map[string]any{"clock": tt.entry}, Deps{DefaultTimeout: time.Second})
			if len(warns) == 0 || !strings.Contains(warns[0], tt.want) {
				t.Fatalf("warns = %v, want %q", warns, tt.want)
			}
		})
	}
}

func TestClockCheckChronyData(t *testing.T) {
	res := testChronyClockCheck("", map[string]conn.Result{
		conn.DefaultHost: testChronyResult(),
	}).Run(context.Background())
	if !res.OK {
		t.Fatalf("chrony clock check should pass: %s", res.Message)
	}
	if res.Data[DataKeyProtocol] != ClockSourceChrony {
		t.Fatalf("protocol = %v, want %q", res.Data[DataKeyProtocol], ClockSourceChrony)
	}
	if res.Data[DataKeyServer] != conn.DefaultHost || res.Data[DataKeyPort] != 323 {
		t.Fatalf("host target not reported: %v", res.Data)
	}
	if _, ok := res.Data[DataKeySocket]; ok {
		t.Fatalf("socket must be absent when addressing host:port: %v", res.Data)
	}
	// The daemon-only numbers must reach Data as float64, not as the probe's
	// strings, so they can be graphed and compared.
	for _, key := range []string{
		DataKeySkewPPM, DataKeyFrequencyPPM, DataKeyResidualFreqPPM, DataKeyRMSOffsetSeconds,
		DataKeyLastOffsetSeconds, DataKeyUpdateIntervalSecs, DataKeyReferenceAgeSecs,
		DataKeySources, DataKeySourcesOnline, DataKeySourcesOffline, DataKeySourcesUnresolved,
	} {
		if _, ok := res.Data[key].(float64); !ok {
			t.Errorf("%s = %#v, want a float64", key, res.Data[key])
		}
	}
	for _, key := range []string{DataKeyReferenceAddress, DataKeySynchronized, DataKeyReferenceID} {
		if _, ok := res.Data[key].(string); !ok {
			t.Errorf("%s = %#v, want a string", key, res.Data[key])
		}
	}
}

func TestClockCheckChronySocketData(t *testing.T) {
	// Addressing the command socket reports it instead of a host and port, the
	// same convention the conn checks follow.
	res := testChronyClockCheck(testChronySocket, map[string]conn.Result{
		conn.DefaultHost: testChronyResult(),
	}).Run(context.Background())
	if !res.OK {
		t.Fatalf("chrony clock check over the socket should pass: %s", res.Message)
	}
	if res.Data[DataKeySocket] != testChronySocket {
		t.Fatalf("socket = %v, want %q", res.Data[DataKeySocket], testChronySocket)
	}
	for _, key := range []string{DataKeyServer, DataKeyPort} {
		if _, ok := res.Data[key]; ok {
			t.Fatalf("%s must be absent in socket mode: %v", key, res.Data)
		}
	}
	if !strings.Contains(res.Message, testChronySocket) {
		t.Fatalf("message = %q, want it to name the socket", res.Message)
	}
}

func TestClockCheckNTPDataUnchanged(t *testing.T) {
	// An ntp clock check must keep reporting exactly what it always has: the
	// chrony-only keys are absent and the target is still server plus port.
	res := testClockCheck([]string{"time.example"}, map[string]conn.Result{
		"time.example": testClockResult("0.250000", "3", "10.000"),
	}, nil).Run(context.Background())
	if !res.OK {
		t.Fatalf("ntp clock check should pass: %s", res.Message)
	}
	if res.Data[DataKeyProtocol] != ClockSourceNTP {
		t.Fatalf("protocol = %v, want %q", res.Data[DataKeyProtocol], ClockSourceNTP)
	}
	if res.Data[DataKeyServer] != "time.example" || res.Data[DataKeyPort] != 123 {
		t.Fatalf("target = %v", res.Data)
	}
	for _, key := range []string{
		DataKeySocket, DataKeySkewPPM, DataKeyFrequencyPPM, DataKeySourcesOnline,
		DataKeySynchronized, DataKeyReferenceAddress,
	} {
		if _, ok := res.Data[key]; ok {
			t.Errorf("%s must be absent for an ntp sample: %v", key, res.Data[key])
		}
	}
	if want := "clock offset 0.250s via time.example:123 (stratum 3)"; res.Message != want {
		t.Fatalf("message = %q, want %q", res.Message, want)
	}
}

// TestClockRunReportsTheBestSamplesOwnFailure pins the pairing introduced when
// Run stopped recomputing the reason from the best sample: the reported message
// must name the failure of the sample it reports, not of the last one tried.
func TestClockRunReportsTheBestSamplesOwnFailure(t *testing.T) {
	cases := []struct {
		name        string
		servers     []string
		results     map[string]conn.Result
		wantServer  string
		wantMessage string
	}{
		{
			// The closest sample is the FIRST one and fails on stratum, while a
			// later, worse sample fails on offset. Reporting the last reason
			// against the best sample would name the wrong threshold.
			name:    "best is first, later sample fails differently",
			servers: []string{"near.example", "far.example"},
			results: map[string]conn.Result{
				"near.example": testClockResult("0.100000", "9", "10.000"),
				"far.example":  testClockResult("5.000000", "2", "10.000"),
			},
			wantServer:  "near.example",
			wantMessage: "max_stratum",
		},
		{
			name:    "best is last, earlier sample fails differently",
			servers: []string{"far.example", "near.example"},
			results: map[string]conn.Result{
				"far.example":  testClockResult("5.000000", "2", "10.000"),
				"near.example": testClockResult("0.100000", "9", "10.000"),
			},
			wantServer:  "near.example",
			wantMessage: "max_stratum",
		},
		{
			// An unsynchronized sample has offset ~0, so it is always the
			// "closest" — it must be reported as unsynchronized, not as passing.
			name:    "unsynchronized sample is the closest",
			servers: []string{"drifting.example", "dead.example"},
			results: map[string]conn.Result{
				"drifting.example": testClockResult("5.000000", "2", "10.000"),
				"dead.example":     testClockUnsynchronized(),
			},
			wantServer:  "dead.example",
			wantMessage: "unsynchronized",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := testClockCheck(tc.servers, tc.results, nil).Run(context.Background())
			if res.OK {
				t.Fatalf("every sample fails a threshold, got OK: %s", res.Message)
			}
			if res.Data[DataKeyServer] != tc.wantServer {
				t.Fatalf("reported server = %v, want %q", res.Data[DataKeyServer], tc.wantServer)
			}
			if !strings.Contains(res.Message, tc.wantMessage) {
				t.Fatalf("message = %q, want it to mention %q", res.Message, tc.wantMessage)
			}
			if !strings.Contains(res.Message, tc.wantServer) {
				t.Fatalf("message = %q, want it to name the reported target %q", res.Message, tc.wantServer)
			}
		})
	}
}

// TestClockExtraCoercionMatchesStrconv pins that routing the Extra readers
// through cfgval kept strconv's exact acceptance: the probes hand these
// helpers raw protocol strings, so widening what parses would silently admit
// malformed samples.
func TestClockExtraCoercionMatchesStrconv(t *testing.T) {
	inputs := []string{
		"", " ", "  0.5  ", "0.5", "-0.5", "0", "-0", "3", "3.7", "1e-9", "+2",
		"abc", "0x10", "3,7", "1 2", "١٢٣",
	}
	for _, in := range inputs {
		res := conn.Result{Extra: map[string]string{"k": in}}
		trimmed := strings.TrimSpace(in)

		// Float: value present exactly when strconv accepts the trimmed string.
		wantFloat, floatErr := strconv.ParseFloat(trimmed, 64)
		data := map[string]any{}
		copyFloatExtra(data, res, "k")
		got, present := data["k"]
		if wantPresent := trimmed != "" && floatErr == nil; present != wantPresent {
			t.Errorf("copyFloatExtra(%q): present = %v, want %v", in, present, wantPresent)
		} else if present && got != wantFloat {
			t.Errorf("copyFloatExtra(%q) = %v, want %v", in, got, wantFloat)
		}

		gotFloat, err := requiredFloatExtra(res, "k")
		if wantOK := trimmed != "" && floatErr == nil; (err == nil) != wantOK {
			t.Errorf("requiredFloatExtra(%q): err = %v, want ok = %v", in, err, wantOK)
		} else if err == nil && gotFloat != wantFloat {
			t.Errorf("requiredFloatExtra(%q) = %v, want %v", in, gotFloat, wantFloat)
		}

		// Int: same, against strconv.Atoi.
		wantInt, intErr := strconv.Atoi(trimmed)
		gotInt, err := requiredIntExtra(res, "k")
		if wantOK := trimmed != "" && intErr == nil; (err == nil) != wantOK {
			t.Errorf("requiredIntExtra(%q): err = %v, want ok = %v", in, err, wantOK)
		} else if err == nil && gotInt != wantInt {
			t.Errorf("requiredIntExtra(%q) = %v, want %v", in, gotInt, wantInt)
		}
	}
}

// TestClockStringExtrasAreDisjointFromFloats guards the two key lists the
// sample copier walks: a key in both would be copied twice, the float write
// silently replacing the string one.
func TestClockStringExtrasAreDisjointFromFloats(t *testing.T) {
	floats := map[string]bool{}
	for _, k := range clockFloatExtras {
		if floats[k] {
			t.Errorf("clockFloatExtras lists %q twice", k)
		}
		floats[k] = true
	}
	seen := map[string]bool{}
	for _, k := range clockStringExtras {
		if seen[k] {
			t.Errorf("clockStringExtras lists %q twice", k)
		}
		seen[k] = true
		if floats[k] {
			t.Errorf("%q is in both clockStringExtras and clockFloatExtras", k)
		}
	}
	// The two required fields are read separately and must not be re-copied.
	for _, k := range append(append([]string{}, clockStringExtras...), clockFloatExtras...) {
		if k == DataKeyOffsetSeconds || k == DataKeyStratum {
			t.Errorf("%q is read as a required field and must not also be copied", k)
		}
	}
}

func TestClockCheckSocketIgnoresInterfacePin(t *testing.T) {
	// A Unix socket has no egress link. Walking the interface list would stamp
	// the result with an interface the dial never bound — and with
	// interface_match: all would report every one of them as ok.
	chk := testChronyClockCheck(testChronySocket, map[string]conn.Result{
		conn.DefaultHost: testChronyResult(),
	})
	chk.ifaces, chk.ifaceAll = []string{"eth0", "eth1"}, true

	res := chk.Run(context.Background())
	if !res.OK {
		t.Fatalf("socket probe should pass: %s", res.Message)
	}
	for _, key := range []string{DataKeyInterface, DataKeyInterfaces} {
		if val, ok := res.Data[key]; ok {
			t.Errorf("%s = %v, want it absent: the socket dial cannot bind an interface", key, val)
		}
	}
	if strings.Contains(res.Message, "eth0") {
		t.Errorf("message = %q, must not name an interface it never used", res.Message)
	}
}

func TestClockFailureCodeBelongsToTheReportedSample(t *testing.T) {
	// The reported sample is the one with the smallest offset, which is not
	// necessarily the last tried nor the one whose failure the loop saw first.
	// clock_failure gates the forced clock correction, so a code leaking from
	// another sample would let a step fire on a failure a step cannot fix.
	cases := []struct {
		name       string
		servers    []string
		results    map[string]conn.Result
		wantServer string
		wantCode   string
	}{
		{
			// Closest sample fails on stratum; a worse one fails on offset.
			name:    "best fails on stratum, another on offset",
			servers: []string{"near.example", "far.example"},
			results: map[string]conn.Result{
				"near.example": testClockResult("1.000000", "9", "10.000"),
				"far.example":  testClockResult("5.000000", "2", "10.000"),
			},
			wantServer: "near.example",
			wantCode:   ClockFailureStratum,
		},
		{
			// Same, reversed order, so it cannot pass by accident of iteration.
			name:    "best is last and fails on root dispersion",
			servers: []string{"far.example", "near.example"},
			results: map[string]conn.Result{
				"far.example":  testClockResult("5.000000", "2", "10.000"),
				"near.example": testClockResult("1.000000", "2", "900.000"),
			},
			wantServer: "near.example",
			wantCode:   ClockFailureRootDispersion,
		},
		{
			name:    "an unsynchronized sample is always the closest",
			servers: []string{"drifting.example", "dead.example"},
			results: map[string]conn.Result{
				"drifting.example": testClockResult("5.000000", "2", "10.000"),
				"dead.example":     testClockUnsynchronized(),
			},
			wantServer: "dead.example",
			wantCode:   ClockFailureUnsynchronized,
		},
		{
			name:    "a plain offset breach",
			servers: []string{"far.example"},
			results: map[string]conn.Result{
				"far.example": testClockResult("5.000000", "2", "10.000"),
			},
			wantServer: "far.example",
			wantCode:   ClockFailureOffset,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := testClockCheck(tc.servers, tc.results, nil).Run(context.Background())
			if res.OK {
				t.Fatalf("every sample fails, got OK: %s", res.Message)
			}
			if res.Data[DataKeyServer] != tc.wantServer {
				t.Fatalf("reported server = %v, want %q", res.Data[DataKeyServer], tc.wantServer)
			}
			if got := res.Data[DataKeyClockFailure]; got != tc.wantCode {
				t.Fatalf("clock_failure = %v, want %q (the reported sample's own failure)", got, tc.wantCode)
			}
		})
	}
}

func TestClockFailureCodeAbsentWhenHealthyOrUnsampled(t *testing.T) {
	// A passing check must not publish a failure code, and a check that got no
	// sample at all must leave it absent rather than empty-but-present — the
	// makestep gate reads it straight off Result.Data.
	ok := testClockCheck([]string{"time.example"}, map[string]conn.Result{
		"time.example": testClockResult("0.250000", "3", "10.000"),
	}, nil).Run(context.Background())
	if !ok.OK {
		t.Fatalf("check should pass: %s", ok.Message)
	}
	if val, present := ok.Data[DataKeyClockFailure]; present {
		t.Fatalf("a passing check published clock_failure = %v", val)
	}

	none := testClockCheck([]string{"time.example"}, nil,
		map[string]error{"time.example": errors.New("timeout")}).Run(context.Background())
	if none.OK {
		t.Fatal("a check with no usable sample must fail")
	}
	if val, present := none.Data[DataKeyClockFailure]; present {
		t.Fatalf("a check with no sample published clock_failure = %v", val)
	}
}
