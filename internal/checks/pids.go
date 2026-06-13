package checks

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// PidsSample is one observation of the kernel PID table: the total scheduling
// entities alive (threads — each consumes a PID) and the kernel.pid_max limit.
type PidsSample struct {
	Threads uint64
	Max     uint64
}

// PidsSamplerFunc reads the current PID-table sample. Injected for tests; the
// default reads /proc/loadavg and /proc/sys/kernel/pid_max.
type PidsSamplerFunc func() (PidsSample, error)

// pidsCheck watches the kernel PID table against its maximum. Like fds it is a
// level check: OK==true means every predicate holds. A full PID table makes
// every fork()/clone() fail with EAGAIN host-wide — the end state the zombies
// check's docs warn about — so it is worth catching while there is headroom.
type pidsCheck struct {
	base
	preds   []levelPred
	sampler PidsSamplerFunc
}

func (c pidsCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultPidsSampler
	}
	s, err := sampler()
	if err != nil {
		return c.result(false, "pids: "+err.Error(), start)
	}

	values := map[string]float64{"count": float64(s.Threads)}
	// used_pct/free need the limit; an unknown limit leaves them "unknown" so a
	// predicate on them cannot hold (the level check is an AND).
	var usedPct float64
	if s.Max > 0 {
		usedPct = float64(s.Threads) / float64(s.Max) * 100
		values["used_pct"] = usedPct
		values["free"] = float64(s.Max - min(s.Threads, s.Max))
	}

	ok := levelPredsHold(c.preds, values)

	res := c.result(ok, fmt.Sprintf("pids %d/%d in use (%.1f%%)", s.Threads, s.Max, usedPct), start)
	res.Data = map[string]any{"count": s.Threads, "max": s.Max, "used_pct": usedPct}
	if s.Max > 0 {
		res.Data["free"] = s.Max - min(s.Threads, s.Max)
	}
	res.Data["value"] = firstPredValue(c.preds, values, usedPct)
	return res
}

// SamplePids returns one live PID-table observation (count/max) using the
// default /proc/loadavg + kernel.pid_max reader. Exposed so callers like the
// web backend can render a PID-table gauge without running a full pids check.
func SamplePids() (PidsSample, error) { return defaultPidsSampler() }

// defaultPidsSampler reads the total scheduling entities from the fourth
// /proc/loadavg field ("running/total") and the limit from kernel.pid_max.
func defaultPidsSampler() (PidsSample, error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return PidsSample{}, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 4 {
		return PidsSample{}, fmt.Errorf("malformed /proc/loadavg")
	}
	_, total, ok := strings.Cut(fields[3], "/")
	if !ok {
		return PidsSample{}, fmt.Errorf("malformed /proc/loadavg entities field %q", fields[3])
	}
	var s PidsSample
	if s.Threads, err = strconv.ParseUint(total, 10, 64); err != nil {
		return PidsSample{}, fmt.Errorf("malformed thread count %q", total)
	}
	if v, err := readProcUint("/proc/sys/kernel/pid_max"); err == nil {
		s.Max = v
	}
	return s, nil
}
