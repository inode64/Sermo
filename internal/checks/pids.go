package checks

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	procLoadavgEntitiesIndex = 3
	procLoadavgMinFields     = procLoadavgEntitiesIndex + 1
	procLoadavgEntitiesSep   = "/"
)

// PidsSample is one observation of the kernel PID table: the total scheduling
// entities alive (threads — each consumes a PID) and the ceiling that binds them.
type PidsSample struct {
	Threads uint64
	Max     uint64
}

// PidsSamplerFunc reads the current PID-table sample. Injected for tests; the
// default reads loadavg and the binding ceiling.
type PidsSamplerFunc func() (PidsSample, error)

// pidsCheck is a level check for PID table exhaustion.
type pidsCheck struct {
	base
	preds   []levelPred
	sampler PidsSamplerFunc
}

func (c pidsCheck) Run(_ context.Context) Result {
	return runSampledLevelCount(c.base, c.preds, samplerOr(c.sampler, defaultPidsSampler),
		func(s PidsSample) (uint64, uint64) { return s.Threads, s.Max },
		"pids", "in use", DataKeyCount)
}

// defaultPidsSampler reads the total scheduling entities from the fourth loadavg
// field ("running/total") and the ceiling from bindingThreadCeiling.
func defaultPidsSampler() (PidsSample, error) {
	data, err := os.ReadFile(procLoadavgPath)
	if err != nil {
		return PidsSample{}, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < procLoadavgMinFields {
		return PidsSample{}, fmt.Errorf(malformedFileFormat, procLoadavgPath)
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
	s.Max = bindingThreadCeiling()
	return s, nil
}

// bindingThreadCeiling is the smaller of kernel.pid_max and kernel.threads-max.
//
// Both bind the number this check counts: every thread takes a slot in the PID
// number space, and threads-max caps the total independently. Dividing by pid_max
// alone reports whichever is looser — on a host with pid_max 4194304 and
// threads-max 1027204 that understates utilisation fourfold, so a table a quarter
// full reads as one-sixteenth full. A ceiling is only a ceiling if it is the one
// you actually hit first.
//
// An unreadable file contributes nothing rather than zero: one limit missing
// leaves the other in force, and neither readable leaves no ceiling at all, which
// levelCountResult reports as such instead of inventing a percentage.
func bindingThreadCeiling() uint64 {
	ceiling := uint64(0)
	for _, path := range [...]string{procPidMaxPath, procThreadsMaxPath} {
		v, err := readProcUint(path)
		if err != nil || v == 0 {
			continue
		}
		if ceiling == 0 || v < ceiling {
			ceiling = v
		}
	}
	return ceiling
}
