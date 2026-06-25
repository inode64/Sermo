package checks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func buildPressure(t *testing.T, entry map[string]any, sampler PressureSamplerFunc) pressureCheck {
	t.Helper()
	entry["type"] = "pressure"
	built, warns := Build(map[string]any{"psi": entry}, Deps{DefaultTimeout: time.Second, PressureSampler: sampler})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("pressure check should build: warns=%v", warns)
	}
	return built[0].Check.(pressureCheck)
}

func TestParsePressureFormat(t *testing.T) {
	s, err := parsePressure("some avg10=1.50 avg60=0.80 avg300=0.10 total=123456\n" +
		"full avg10=0.40 avg60=0.20 avg300=0.00 total=6789\n")
	if err != nil {
		t.Fatal(err)
	}
	if s.Some.Avg10 != 1.5 || s.Some.Avg60 != 0.8 || s.Some.Avg300 != 0.1 {
		t.Fatalf("some = %+v", s.Some)
	}
	if s.Full.Avg10 != 0.4 || s.Full.Avg60 != 0.2 {
		t.Fatalf("full = %+v", s.Full)
	}

	// cpu on older kernels has no full line; that still parses (full stays 0).
	cpu, err := parsePressure("some avg10=3.00 avg60=1.00 avg300=0.50 total=42\n")
	if err != nil || cpu.Some.Avg10 != 3 || cpu.Full.Avg10 != 0 {
		t.Fatalf("cpu-only sample = %+v, err %v", cpu, err)
	}

	if _, err := parsePressure("not a psi file\n"); err == nil {
		t.Fatal("garbage must not parse")
	}
}

func TestPressureCheckLevels(t *testing.T) {
	stalled := func(resource string) (PressureSample, error) {
		if resource != "memory" {
			return PressureSample{}, errors.New("unexpected resource " + resource)
		}
		return PressureSample{
			Some: PressureAverages{Avg10: 12.5, Avg60: 8, Avg300: 2},
			Full: PressureAverages{Avg10: 4, Avg60: 1, Avg300: 0.5},
		}, nil
	}

	fires := buildPressure(t, map[string]any{
		"resource":   "memory",
		"some_avg10": map[string]any{"op": ">", "value": 10},
		"full_avg60": map[string]any{"op": ">=", "value": 1},
	}, stalled)
	res := fires.Run(context.Background())
	if !res.OK || res.Data["some_avg10"].(float64) != 12.5 || res.Data["value"].(float64) != 12.5 {
		t.Fatalf("stalled host must fire: OK=%v data=%v", res.OK, res.Data)
	}
	if !strings.Contains(res.Message, "pressure memory") {
		t.Fatalf("message = %q", res.Message)
	}

	calm := buildPressure(t, map[string]any{
		"resource":   "memory",
		"some_avg10": map[string]any{"op": ">", "value": 50},
	}, stalled)
	if calm.Run(context.Background()).OK {
		t.Fatal("12.5%% stall must not cross a 50%% threshold")
	}

	// A kernel without PSI (CONFIG_PSI=n) never fires.
	noPSI := buildPressure(t, map[string]any{
		"resource":   "io",
		"some_avg10": map[string]any{"op": ">", "value": 0},
	}, func(string) (PressureSample, error) { return PressureSample{}, errors.New("no such file") })
	if res := noPSI.Run(context.Background()); res.OK || !strings.Contains(res.Message, "no such file") {
		t.Fatalf("missing PSI must never fire: %+v", res)
	}
}

func TestPressureCheckBuildErrors(t *testing.T) {
	for name, entry := range map[string]map[string]any{
		"no resource":  {"type": "pressure", "some_avg10": map[string]any{"op": ">", "value": 10}},
		"bad resource": {"type": "pressure", "resource": "disk", "some_avg10": map[string]any{"op": ">", "value": 10}},
		"no predicate": {"type": "pressure", "resource": "cpu"},
	} {
		_, warns := Build(map[string]any{"psi": entry}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 1 {
			t.Errorf("%s: warns = %v, want one", name, warns)
		}
	}
}

func TestParsePressureMinimalFields(t *testing.T) {
	// A PSI line with exactly four fields (no total=) is still valid: the guard
	// skips lines with fewer than 4 fields, not 4 or fewer.
	s, err := parsePressure("some avg10=1.50 avg60=0.80 avg300=0.10\n")
	if err != nil {
		t.Fatalf("4-field PSI line must parse: %v", err)
	}
	if s.Some.Avg10 != 1.5 || s.Some.Avg60 != 0.8 || s.Some.Avg300 != 0.1 {
		t.Fatalf("some = %+v, want 1.5/0.8/0.1", s.Some)
	}
}
