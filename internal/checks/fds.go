package checks

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// FdsSample is one observation of the system-wide open file descriptors: the
// number currently allocated and the kernel maximum (fs.file-max).
type FdsSample struct {
	Allocated uint64
	Max       uint64
}

// FdsSamplerFunc reads the current fd sample. Injected for tests; the default
// reads /proc/sys/fs/file-nr.
type FdsSamplerFunc func() (FdsSample, error)

// fdsCheck watches the system-wide open file descriptors against the kernel
// maximum (fs.file-max). Like disk it is a level check: OK==true means every
// predicate holds. Catches fd exhaustion, which makes every open()/socket()/
// accept() across the host fail with EMFILE/ENFILE.
type fdsCheck struct {
	base
	preds   []levelPred
	sampler FdsSamplerFunc
}

func (c fdsCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultFdsSampler
	}
	s, err := sampler()
	if err != nil {
		return c.result(false, "fds: "+err.Error(), start)
	}
	return levelCountResult(c.base, c.preds, "fds", "allocated", "allocated", s.Allocated, s.Max, start)
}

// SampleFds returns one live system-wide fd observation (allocated/max) using
// the default /proc/sys/fs/file-nr reader. Exposed so callers like the web
// backend can render an fds gauge without running a full fds check.
func SampleFds() (FdsSample, error) { return defaultFdsSampler() }

// defaultFdsSampler reads allocated (field 1) and max (field 3) from
// /proc/sys/fs/file-nr. The middle field (free handles) is always 0 on modern
// kernels, so allocated is the in-use count.
func defaultFdsSampler() (FdsSample, error) {
	data, err := os.ReadFile("/proc/sys/fs/file-nr")
	if err != nil {
		return FdsSample{}, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return FdsSample{}, fmt.Errorf("malformed /proc/sys/fs/file-nr")
	}
	alloc, e1 := strconv.ParseUint(fields[0], 10, 64)
	maxFds, e3 := strconv.ParseUint(fields[2], 10, 64)
	if e1 != nil || e3 != nil {
		return FdsSample{}, fmt.Errorf("malformed /proc/sys/fs/file-nr")
	}
	return FdsSample{Allocated: alloc, Max: maxFds}, nil
}
