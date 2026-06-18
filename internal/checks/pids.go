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

// pidsCheck is a level check for PID table exhaustion.
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
	return levelCountResult(c.base, c.preds, "pids", "in use", "count", s.Threads, s.Max, start)
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
