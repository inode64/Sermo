package checks

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// MemorySample is one observation of system RAM: total bytes and the kernel's
// MemAvailable estimate (what new allocations can claim without swapping).
type MemorySample struct {
	TotalBytes     uint64
	AvailableBytes uint64
}

// MemorySamplerFunc reads the current memory sample. Injected for tests; the
// default reads /proc/meminfo.
type MemorySamplerFunc func() (MemorySample, error)

// memoryCheck is a level check: OK means every predicate holds. It uses
// MemAvailable, so page cache and reclaimable buffers do not read as used.
type memoryCheck struct {
	base
	preds   []levelPred
	sampler MemorySamplerFunc
}

func (c memoryCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultMemorySampler
	}
	s, err := sampler()
	if err != nil {
		return c.result(false, "memory: "+err.Error(), start)
	}
	if s.TotalBytes == 0 {
		// An unreadable/odd meminfo must never fire (the level check is an AND
		// over known values), mirroring the swapless-host guard in swap.
		return c.result(false, "memory: total size unknown", start)
	}
	avail := min(s.AvailableBytes, s.TotalBytes)
	usedPct := float64(s.TotalBytes-avail) / float64(s.TotalBytes) * 100
	availPct := float64(avail) / float64(s.TotalBytes) * 100
	values := map[string]float64{
		fieldUsedPct:        usedPct,
		fieldAvailablePct:   availPct,
		fieldAvailableBytes: float64(avail),
	}

	ok := levelPredsHold(c.preds, values)

	res := c.result(ok, fmt.Sprintf("memory used %.1f%% available %.1f%% (%d bytes)", usedPct, availPct, avail), start)
	res.Data = map[string]any{
		fieldTotalBytes:     s.TotalBytes,
		fieldAvailableBytes: avail,
		fieldUsedPct:        usedPct,
		fieldAvailablePct:   availPct,
	}
	res.Data["value"] = firstPredValue(c.preds, values, usedPct)
	return res
}

// defaultMemorySampler reads MemTotal/MemAvailable from /proc/meminfo.
func defaultMemorySampler() (MemorySample, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return MemorySample{}, err
	}
	var s MemorySample
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			s.TotalBytes = parseMeminfoKB(v)
		} else if v, ok := strings.CutPrefix(line, "MemAvailable:"); ok {
			s.AvailableBytes = parseMeminfoKB(v)
		}
	}
	return s, nil
}
