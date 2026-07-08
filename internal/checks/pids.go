package checks

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	procLoadavgEntitiesIndex = 3
	procLoadavgMinFields     = procLoadavgEntitiesIndex + 1
	procLoadavgEntitiesSep   = "/"
)

// PidsSample is one observation of the kernel PID table: the total scheduling
// entities alive (threads — each consumes a PID) and the kernel.pid_max limit.
type PidsSample struct {
	Threads uint64
	Max     uint64
}

// PidsSamplerFunc reads the current PID-table sample. Injected for tests; the
// default reads loadavg and kernel.pid_max.
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
	return levelCountResult(c.base, c.preds, "pids", "in use", DataKeyCount, s.Threads, s.Max, start)
}

// SamplePids returns one live PID-table observation (count/max) using the
// default /proc/loadavg + kernel.pid_max reader. Exposed so callers like the
// web backend can render a PID-table gauge without running a full pids check.
func SamplePids() (PidsSample, error) { return defaultPidsSampler() }

// defaultPidsSampler reads the total scheduling entities from the fourth loadavg
// field ("running/total") and the limit from kernel.pid_max.
func defaultPidsSampler() (PidsSample, error) {
	data, err := os.ReadFile(procLoadavgPath)
	if err != nil {
		return PidsSample{}, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < procLoadavgMinFields {
		return PidsSample{}, fmt.Errorf("malformed %s", procLoadavgPath)
	}
	entities := fields[procLoadavgEntitiesIndex]
	_, total, ok := strings.Cut(entities, procLoadavgEntitiesSep)
	if !ok {
		return PidsSample{}, fmt.Errorf("malformed %s entities field %q", procLoadavgPath, entities)
	}
	var s PidsSample
	if s.Threads, err = strconv.ParseUint(total, numericBaseDecimal, numericBits64); err != nil {
		return PidsSample{}, fmt.Errorf("malformed thread count %q", total)
	}
	if v, err := readProcUint(procPidMaxPath); err == nil {
		s.Max = v
	}
	return s, nil
}
