package checks

import (
	"context"
	"errors"
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
		name        string
		check       clockCheck
		wantOK      bool
		wantServer  string
		wantMessage string
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
			wantOK:      false,
			wantMessage: "all NTP servers failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := tt.check.Run(context.Background())
			if res.OK != tt.wantOK {
				t.Fatalf("OK = %v, want %v: %s", res.OK, tt.wantOK, res.Message)
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
		base:              base{name: "clock", timeout: time.Second},
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
