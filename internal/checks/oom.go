package checks

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const oomVMStatKillPrefix = "oom_kill "

// OomSamplerFunc reads the cumulative count of kernel OOM kills, reporting ok =
// false when the counter is unavailable (kernels before the oom_kill vmstat
// field). Injected for tests; the default reads the kernel vmstat file.
type OomSamplerFunc func() (uint64, bool)

// oomCheck fires when the kernel OOM killer has reaped processes since the last
// cycle. It tracks the cumulative oom_kill counter and compares the per-cycle
// delta to a threshold (default > 0: any kill). Stateful, so a pointer type; a
// watch ticks sequentially on its own goroutine. OK==true means "fire".
type oomCheck struct {
	base
	op      string
	value   float64
	sampler OomSamplerFunc

	primed    bool
	lastCount uint64
}

func (c *oomCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultOomSampler
	}
	count, ok := sampler()
	if !ok {
		return c.result(false, "oom: oom_kill counter unavailable", start)
	}
	if !c.primed {
		c.primed, c.lastCount = true, count
		res := c.result(false, fmt.Sprintf("oom baseline %d kills", count), start)
		res.Data = map[string]any{DataKeyValue: uint64(0), DataKeyTotal: count}
		return res
	}
	delta := deltaOrZero(count, c.lastCount)
	c.lastCount = count
	met := compareFloat(float64(delta), c.op, c.value)
	res := c.result(met, fmt.Sprintf("oom kills +%d (total %d)", delta, count), start)
	res.Data = map[string]any{DataKeyValue: delta, DataKeyTotal: count}
	return res
}

// SampleOom returns the cumulative kernel OOM-kill counter using the default
// vmstat reader. ok is false when the counter is unavailable.
func SampleOom() (count uint64, ok bool) { return defaultOomSampler() }

// defaultOomSampler reads the cumulative oom_kill counter from vmstat.
func defaultOomSampler() (uint64, bool) {
	data, err := os.ReadFile(procVMStatPath)
	if err != nil {
		return 0, false
	}
	for line := range strings.SplitSeq(string(data), checkLineSeparator) {
		if v, ok := strings.CutPrefix(line, oomVMStatKillPrefix); ok {
			n, err := strconv.ParseUint(strings.TrimSpace(v), numericBaseDecimal, numericBits64)
			return n, err == nil
		}
	}
	return 0, false
}
