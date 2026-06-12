package checks

import (
	"context"
	"fmt"
	"runtime"
	"sermo/internal/metrics"
	"time"
)

// LoadSample is one observation of the system load averages and the CPU count
// used to normalize them.
type LoadSample struct {
	Load1, Load5, Load15 float64
	NumCPU               int
}

// LoadSamplerFunc reads the current load sample. Injected for tests; the default
// reads /proc/loadavg and runtime.NumCPU().
type LoadSamplerFunc func() (LoadSample, error)

// loadCheck watches the system load averages against thresholds (like disk, a
// level check: OK==true means every predicate holds). With perCPU the loads are
// divided by the CPU count first, so a threshold expresses load per core (1.0 ==
// fully utilized) regardless of machine size.
type loadCheck struct {
	base
	preds   []levelPred
	perCPU  bool
	sampler LoadSamplerFunc
}

func (c loadCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultLoadSampler
	}
	s, err := sampler()
	if err != nil {
		return c.result(false, "load: "+err.Error(), start)
	}

	values := map[string]float64{"load1": s.Load1, "load5": s.Load5, "load15": s.Load15}
	if c.perCPU {
		if s.NumCPU <= 0 {
			return c.result(false, "load: cpu count unknown", start)
		}
		for k, v := range values {
			values[k] = v / float64(s.NumCPU)
		}
	}

	ok := true
	for _, p := range c.preds {
		if !compareFloat(values[p.field], p.op, p.value) {
			ok = false
		}
	}

	suffix := ""
	if c.perCPU {
		suffix = fmt.Sprintf(" per-cpu (/%d)", s.NumCPU)
	}
	res := c.result(ok, fmt.Sprintf("load %.2f %.2f %.2f%s", s.Load1, s.Load5, s.Load15, suffix), start)
	res.Data = map[string]any{
		"load1": s.Load1, "load5": s.Load5, "load15": s.Load15,
		"num_cpu": s.NumCPU, "per_cpu": c.perCPU,
	}
	res.Data["value"] = values["load1"]
	if len(c.preds) > 0 {
		res.Data["value"] = values[c.preds[0].field]
	}
	return res
}

// defaultLoadSampler reads the three load averages through the shared metrics
// procfs reader (one /proc/loadavg parser instead of a per-package copy).
func defaultLoadSampler() (LoadSample, error) {
	l1, l5, l15, ok := metrics.OSReader{}.LoadAverages()
	if !ok {
		return LoadSample{}, fmt.Errorf("malformed /proc/loadavg")
	}
	return LoadSample{Load1: l1, Load5: l5, Load15: l15, NumCPU: runtime.NumCPU()}, nil
}
