package checks

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	fileNRAllocatedIndex = 0
	fileNRMaxIndex       = 2
	fileNRMinFields      = fileNRMaxIndex + 1
)

// FdsSample is one observation of the system-wide open file descriptors: the
// number currently allocated and the kernel maximum (fs.file-max).
type FdsSample struct {
	Allocated uint64
	Max       uint64
}

// FdsSamplerFunc reads the current fd sample. Injected for tests; the default
// reads the kernel file-nr sysctl.
type FdsSamplerFunc func() (FdsSample, error)

// fdsCheck is a level check for system-wide file descriptor exhaustion.
type fdsCheck struct {
	base
	preds   []levelPred
	sampler FdsSamplerFunc
}

func (c fdsCheck) Run(_ context.Context) Result {
	return runSampledLevelCount(c.base, c.preds, samplerOr(c.sampler, defaultFdsSampler),
		func(s FdsSample) (uint64, uint64) { return s.Allocated, s.Max },
		"fds", "allocated", DataKeyAllocated)
}

// defaultFdsSampler reads allocated (field 1) and max (field 3). The middle
// field (free handles) is always 0 on modern kernels, so allocated is the
// in-use count.
func defaultFdsSampler() (FdsSample, error) {
	data, err := os.ReadFile(procFileNRPath)
	if err != nil {
		return FdsSample{}, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < fileNRMinFields {
		return FdsSample{}, fmt.Errorf(malformedFileFormat, procFileNRPath)
	}
	alloc, e1 := strconv.ParseUint(fields[fileNRAllocatedIndex], numericBaseDecimal, numericBits64)
	maxFds, e3 := strconv.ParseUint(fields[fileNRMaxIndex], numericBaseDecimal, numericBits64)
	if e1 != nil || e3 != nil {
		return FdsSample{}, fmt.Errorf(malformedFileFormat, procFileNRPath)
	}
	return FdsSample{Allocated: alloc, Max: maxFds}, nil
}
