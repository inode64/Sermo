package checks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"sermo/internal/execx"
	"sermo/internal/servicemgr"
)

func TestFailedUnitsCheckRun(t *testing.T) {
	tests := []struct {
		name    string
		sample  FailedUnitsSample
		err     error
		wantOK  bool
		wantMsg string
	}{
		{
			name:    "no failed unit",
			sample:  FailedUnitsSample{Backend: servicemgr.BackendSystemd},
			wantOK:  false,
			wantMsg: "no failed units",
		},
		{
			// The condition is what fires, so OK == true is the alarm here.
			name:    "failed units are named",
			sample:  FailedUnitsSample{Backend: servicemgr.BackendSystemd, Units: []string{"backup_kvm.service", "cleanup.timer"}},
			wantOK:  true,
			wantMsg: "2 failed unit(s): backup_kvm.service, cleanup.timer",
		},
		{
			name:    "sampler error",
			err:     errors.New("systemctl failed"),
			wantOK:  false,
			wantMsg: "systemctl failed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := failedUnitsCheck{
				name: "watch-failed-units", timeout: time.Second,
				backend: servicemgr.BackendAuto,
				op:      ">",
				value:   0,
				sampler: func(context.Context, servicemgr.Backend, execx.Runner, time.Duration) (FailedUnitsSample, error) {
					return tc.sample, tc.err
				},
			}
			res := c.Run(context.Background())
			if res.OK != tc.wantOK {
				t.Fatalf("OK = %v, want %v (%+v)", res.OK, tc.wantOK, res)
			}
			if !strings.Contains(res.Message, tc.wantMsg) {
				t.Fatalf("message = %q, want substring %q", res.Message, tc.wantMsg)
			}
			if tc.err != nil {
				return
			}
			if got := res.Data[DataKeyCount]; got != uint64(len(tc.sample.Units)) {
				t.Fatalf("data count = %v, want %d", got, len(tc.sample.Units))
			}
			units, present := res.Data[DataKeyUnits]
			if len(tc.sample.Units) == 0 {
				if present {
					t.Fatalf("data units = %v, want absent with no failed unit", units)
				}
				return
			}
			if units != strings.Join(tc.sample.Units, ", ") {
				t.Fatalf("data units = %v, want the joined unit names", units)
			}
		})
	}
}

func TestBuildFailedUnitsCheck(t *testing.T) {
	tests := []struct {
		name      string
		entry     map[string]any
		wantOp    string
		wantValue float64
		wantErr   string
	}{
		{
			name:      "defaults to firing on any failed unit",
			entry:     map[string]any{},
			wantOp:    ">",
			wantValue: 0,
		},
		{
			name:      "explicit count predicate",
			entry:     map[string]any{CheckKeyCount: map[string]any{CheckKeyOp: ">=", CheckKeyValue: 3}},
			wantOp:    ">=",
			wantValue: 3,
		},
		{
			name:    "unknown backend",
			entry:   map[string]any{CheckKeyBackend: "upstart"},
			wantErr: "backend must be",
		},
		{
			name:    "malformed count predicate",
			entry:   map[string]any{CheckKeyCount: "many"},
			wantErr: "count",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			check, errs := buildFailedUnitsCheck(base{name: "w"}, tc.entry, nil, Deps{})
			if tc.wantErr != "" {
				if !strings.Contains(errs, tc.wantErr) {
					t.Fatalf("errs = %q, want substring %q", errs, tc.wantErr)
				}
				return
			}
			if errs != "" {
				t.Fatalf("unexpected errs %q", errs)
			}
			built, ok := check.(failedUnitsCheck)
			if !ok {
				t.Fatalf("check type = %T, want failedUnitsCheck", check)
			}
			if built.op != tc.wantOp || built.value != tc.wantValue {
				t.Fatalf("predicate = %s %v, want %s %v", built.op, built.value, tc.wantOp, tc.wantValue)
			}
			if built.backend != servicemgr.BackendAuto {
				t.Fatalf("backend = %q, want %q", built.backend, servicemgr.BackendAuto)
			}
		})
	}
}

func TestDefaultFailedUnitsSamplerRejectsUnknownBackend(t *testing.T) {
	// The builder parses the backend, so an unsupported one can only reach the
	// sampler through a hand-built check; it must still fail rather than report
	// an empty listing as "no failed units".
	if _, err := defaultFailedUnitsSampler(context.Background(), servicemgr.Backend("upstart"), nil, time.Second); err == nil {
		t.Fatal("unknown backend must fail")
	}
}
