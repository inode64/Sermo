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
	swapMeminfoTotalPrefix   = "SwapTotal:"
	swapMeminfoFreePrefix    = "SwapFree:"
	swapVMStatPagesIn        = "pswpin"
	swapVMStatPagesOut       = "pswpout"
	swapVMStatPagesInPrefix  = swapVMStatPagesIn + " "
	swapVMStatPagesOutPrefix = swapVMStatPagesOut + " "
	swapMeminfoKBIndex       = 0
	swapBytesPerKiB          = 1024
)

// SwapSample is one observation of system swap: total/free bytes and the
// cumulative pages swapped in/out since boot (vmstat pswpin/pswpout).
type SwapSample struct {
	TotalBytes uint64
	FreeBytes  uint64
	PagesIn    uint64
	PagesOut   uint64
}

// SwapSamplerFunc reads the current swap sample. Injected for tests; the default
// reads meminfo and vmstat.
type SwapSamplerFunc func() (SwapSample, error)

// swap check metric names (the `metric:` selector of a swap check). Exported so
// config validation checks the same metric names the swap check evaluates.
const (
	SwapMetricUsage = "usage"
	SwapMetricIO    = "io"
)

// swapCheck watches one swap metric. `usage` is a level check over
// used_pct/free_pct/free_bytes (like storage); `io` is the per-cycle delta of pages
// swapped in+out (like net errors), so it is stateful and a pointer type. A watch
// ticks sequentially on its own goroutine, so the state needs no locking.
// OK==true means "fire".
type swapCheck struct {
	base
	metric  string
	preds   []levelPred
	op      string
	value   float64
	sampler SwapSamplerFunc

	primed bool
	lastIO uint64
}

func (c *swapCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultSwapSampler
	}
	s, err := sampler()
	if err != nil {
		return c.result(false, "swap: "+err.Error(), start)
	}
	data := map[string]any{fieldMetric: c.metric, fieldTotalBytes: s.TotalBytes, fieldFreeBytes: s.FreeBytes}

	switch c.metric {
	case SwapMetricUsage:
		// A swapless host can never "run out of swap": never fire, so a
		// free_bytes/free_pct predicate does not misfire on total == 0.
		if s.TotalBytes == 0 {
			res := c.result(false, "no swap configured", start)
			res.Data = data
			return res
		}
		if s.FreeBytes > s.TotalBytes {
			res := c.result(false, "swap: free bytes exceed total bytes", start)
			res.Data = data
			return res
		}
		used := s.TotalBytes - s.FreeBytes
		usedPct := float64(used) / float64(s.TotalBytes) * 100
		freePct := float64(s.FreeBytes) / float64(s.TotalBytes) * 100
		values := map[string]float64{fieldUsedPct: usedPct, fieldFreePct: freePct, fieldFreeBytes: float64(s.FreeBytes)}
		ok := levelPredsHold(c.preds, values)
		data[fieldUsedPct], data[fieldFreePct] = usedPct, freePct
		data[fieldValue] = firstPredValue(c.preds, values, usedPct)
		res := c.result(ok, fmt.Sprintf("swap used %.1f%% free %.1f%% (%d bytes free)", usedPct, freePct, s.FreeBytes), start)
		res.Data = data
		return res

	case SwapMetricIO:
		total := s.PagesIn + s.PagesOut
		if !c.primed {
			c.primed, c.lastIO = true, total
			res := c.result(false, fmt.Sprintf("swap io baseline %d pages", total), start)
			res.Data = data
			return res
		}
		delta := deltaOrZero(total, c.lastIO)
		c.lastIO = total
		data[fieldValue], data[DataKeyPages] = delta, total
		met := compareFloat(float64(delta), c.op, c.value)
		res := c.result(met, fmt.Sprintf("swap io +%d pages/cycle (total %d)", delta, total), start)
		res.Data = data
		return res

	default:
		res := c.result(false, "unknown swap metric "+c.metric, start)
		res.Data = data
		return res
	}
}

// defaultSwapSampler reads SwapTotal/SwapFree from meminfo and the pswpin/pswpout
// counters from vmstat.
func defaultSwapSampler() (SwapSample, error) {
	var s SwapSample
	mem, err := os.ReadFile(procMeminfoPath)
	if err != nil {
		return s, err
	}
	for _, line := range strings.Split(string(mem), checkLineSeparator) {
		if v, ok := strings.CutPrefix(line, swapMeminfoTotalPrefix); ok {
			s.TotalBytes = parseMeminfoKB(v)
		} else if v, ok := strings.CutPrefix(line, swapMeminfoFreePrefix); ok {
			s.FreeBytes = parseMeminfoKB(v)
		}
	}
	if vm, err := os.ReadFile(procVMStatPath); err == nil {
		pagesIn, pagesOut, err := parseSwapVMStat(string(vm))
		if err != nil {
			return s, err
		}
		s.PagesIn, s.PagesOut = pagesIn, pagesOut
	}
	return s, nil
}

func parseSwapVMStat(vm string) (pagesIn, pagesOut uint64, err error) {
	for _, line := range strings.Split(vm, checkLineSeparator) {
		if v, ok := strings.CutPrefix(line, swapVMStatPagesInPrefix); ok {
			pagesIn, err = parseSwapPageCounter(swapVMStatPagesIn, v)
			if err != nil {
				return 0, 0, err
			}
		} else if v, ok := strings.CutPrefix(line, swapVMStatPagesOutPrefix); ok {
			pagesOut, err = parseSwapPageCounter(swapVMStatPagesOut, v)
			if err != nil {
				return 0, 0, err
			}
		}
	}
	return pagesIn, pagesOut, nil
}

func parseSwapPageCounter(name, value string) (uint64, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(value), numericBaseDecimal, numericBits64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return n, nil
}

// parseMeminfoKB parses the leading kB value of a "Field:   N kB" line to bytes.
func parseMeminfoKB(s string) uint64 {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	kb, err := strconv.ParseUint(fields[swapMeminfoKBIndex], numericBaseDecimal, numericBits64)
	if err != nil {
		return 0
	}
	return kb * swapBytesPerKiB
}
