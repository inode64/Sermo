package checks

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// OomSamplerFunc reads the cumulative count of kernel OOM kills, reporting ok =
// false when the counter is unavailable (kernels before the /proc/vmstat
// oom_kill field). Injected for tests; the default reads /proc/vmstat.
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
		res.Data = map[string]any{"value": uint64(0), "total": count}
		return res
	}
	var delta uint64
	if count > c.lastCount {
		delta = count - c.lastCount
	}
	c.lastCount = count
	met := compareFloat(float64(delta), c.op, c.value)
	res := c.result(met, fmt.Sprintf("oom kills +%d (total %d)", delta, count), start)
	res.Data = map[string]any{"value": delta, "total": count}
	return res
}

// defaultOomSampler reads the cumulative oom_kill counter from /proc/vmstat.
func defaultOomSampler() (uint64, bool) {
	data, err := os.ReadFile("/proc/vmstat")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "oom_kill "); ok {
			n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}
