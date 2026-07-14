package checks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// PSI pressure resources (the `resource:` selector of a pressure check).
// Exported so config validation checks the same set the check accepts.
const (
	PressureResourceCPU    = "cpu"
	PressureResourceMemory = "memory"
	PressureResourceIO     = "io"
	// PressureResourceSummary is the user-facing list of PSI resources.
	PressureResourceSummary = PressureResourceCPU + ", " + PressureResourceMemory + " or " + PressureResourceIO
)

const (
	psiLineSome          = "some"
	psiLineFull          = "full"
	psiKeyAvg10          = "avg10"
	psiKeyAvg60          = "avg60"
	psiKeyAvg300         = "avg300"
	psiKeyValueSeparator = "="
	psiLineKindIndex     = 0
	psiMinFields         = 4
)

// PressureAverages are one PSI line's rolling stall percentages (10s/60s/300s
// windows).
type PressureAverages struct {
	Avg10, Avg60, Avg300 float64
}

// PressureSample is one observation of a /proc/pressure resource: the share of
// wall time in which *some* tasks stalled on the resource, and in which *all*
// non-idle tasks stalled (`full`; absent for cpu on older kernels).
type PressureSample struct {
	Some PressureAverages
	Full PressureAverages
}

// PressureSamplerFunc reads the current PSI sample for a resource (cpu, memory
// or io). Injected for tests; the default reads /proc/pressure/<resource>.
type PressureSamplerFunc func(resource string) (PressureSample, error)

// pressureCheck is a level check for kernel PSI stall percentages.
type pressureCheck struct {
	base
	resource string
	preds    []levelPred
	sampler  PressureSamplerFunc
}

func (c pressureCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultPressureSampler
	}
	s, err := sampler(c.resource)
	if err != nil {
		// A kernel without PSI (CONFIG_PSI=n) never fires, mirroring the
		// conntrack-without-module behavior.
		return c.result(false, "pressure "+c.resource+": "+err.Error(), start)
	}
	values := map[string]float64{
		fieldSomeAvg10: s.Some.Avg10, fieldSomeAvg60: s.Some.Avg60, fieldSomeAvg300: s.Some.Avg300,
		fieldFullAvg10: s.Full.Avg10, fieldFullAvg60: s.Full.Avg60, fieldFullAvg300: s.Full.Avg300,
	}

	ok := levelPredsHold(c.preds, values)

	res := c.result(ok, fmt.Sprintf("pressure %s some %.2f/%.2f/%.2f full %.2f/%.2f/%.2f",
		c.resource, s.Some.Avg10, s.Some.Avg60, s.Some.Avg300, s.Full.Avg10, s.Full.Avg60, s.Full.Avg300), start)
	res.Data = map[string]any{
		DataKeyResource: c.resource,
		fieldSomeAvg10:  s.Some.Avg10,
		fieldSomeAvg60:  s.Some.Avg60,
		fieldSomeAvg300: s.Some.Avg300,
		fieldFullAvg10:  s.Full.Avg10,
		fieldFullAvg60:  s.Full.Avg60,
		fieldFullAvg300: s.Full.Avg300,
	}
	res.Data[DataKeyValue] = firstPredValue(c.preds, values, s.Some.Avg10)
	return res
}

// SamplePressure returns one live PSI observation for resource using the default
// /proc/pressure/<resource> reader. Exposed so callers like the web backend can
// render pressure data without running a full pressure check.
func SamplePressure(resource string) (PressureSample, error) {
	return defaultPressureSampler(resource)
}

// defaultPressureSampler reads and parses /proc/pressure/<resource>.
func defaultPressureSampler(resource string) (PressureSample, error) {
	data, err := os.ReadFile(filepath.Join(procPressureRootPath, resource))
	if err != nil {
		return PressureSample{}, err
	}
	return parsePressure(string(data))
}

// parsePressure parses the PSI file format ("some avg10=0.12 avg60=… avg300=…
// total=…", plus an optional `full` line). Unknown lines are ignored so newer
// kernels cannot break the parse.
func parsePressure(data string) (PressureSample, error) {
	var s PressureSample
	seen := false
	for line := range strings.SplitSeq(data, checkLineSeparator) {
		fields := strings.Fields(line)
		if len(fields) < psiMinFields {
			continue
		}
		var avgs *PressureAverages
		switch fields[psiLineKindIndex] {
		case psiLineSome:
			avgs = &s.Some
		case psiLineFull:
			avgs = &s.Full
		default:
			continue
		}
		seen = true
		for _, kv := range fields[1:] {
			key, val, ok := strings.Cut(kv, psiKeyValueSeparator)
			if !ok {
				continue
			}
			f, err := strconv.ParseFloat(val, numericBits64)
			if err != nil {
				continue
			}
			switch key {
			case psiKeyAvg10:
				avgs.Avg10 = f
			case psiKeyAvg60:
				avgs.Avg60 = f
			case psiKeyAvg300:
				avgs.Avg300 = f
			}
		}
	}
	if !seen {
		return s, errors.New("unrecognized PSI format")
	}
	return s, nil
}
