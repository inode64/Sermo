package checks

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
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

// pressureCheck watches a kernel PSI resource against stall-percentage
// thresholds. Like disk it is a level check: OK==true means every predicate
// holds (the alert condition). PSI is the kernel's own "this host is
// struggling" signal, complementing `load` (queue depth) and `memory`
// (headroom) with actual stall time.
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
		"some_avg10": s.Some.Avg10, "some_avg60": s.Some.Avg60, "some_avg300": s.Some.Avg300,
		"full_avg10": s.Full.Avg10, "full_avg60": s.Full.Avg60, "full_avg300": s.Full.Avg300,
	}

	ok := true
	for _, p := range c.preds {
		if !compareFloat(values[p.field], p.op, p.value) {
			ok = false
		}
	}

	res := c.result(ok, fmt.Sprintf("pressure %s some %.2f/%.2f/%.2f full %.2f/%.2f/%.2f",
		c.resource, s.Some.Avg10, s.Some.Avg60, s.Some.Avg300, s.Full.Avg10, s.Full.Avg60, s.Full.Avg300), start)
	res.Data = map[string]any{
		"resource":    c.resource,
		"some_avg10":  s.Some.Avg10,
		"some_avg60":  s.Some.Avg60,
		"some_avg300": s.Some.Avg300,
		"full_avg10":  s.Full.Avg10,
		"full_avg60":  s.Full.Avg60,
		"full_avg300": s.Full.Avg300,
	}
	// value is the first predicate's reading, so a hook sees the breaching number.
	res.Data["value"] = s.Some.Avg10
	if len(c.preds) > 0 {
		res.Data["value"] = values[c.preds[0].field]
	}
	return res
}

// defaultPressureSampler reads and parses /proc/pressure/<resource>.
func defaultPressureSampler(resource string) (PressureSample, error) {
	data, err := os.ReadFile("/proc/pressure/" + resource)
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
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		var avgs *PressureAverages
		switch fields[0] {
		case "some":
			avgs = &s.Some
		case "full":
			avgs = &s.Full
		default:
			continue
		}
		seen = true
		for _, kv := range fields[1:] {
			key, val, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				continue
			}
			switch key {
			case "avg10":
				avgs.Avg10 = f
			case "avg60":
				avgs.Avg60 = f
			case "avg300":
				avgs.Avg300 = f
			}
		}
	}
	if !seen {
		return s, fmt.Errorf("unrecognized PSI format")
	}
	return s, nil
}
