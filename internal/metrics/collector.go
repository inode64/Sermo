package metrics

import (
	"sync"
	"time"
)

// Reader abstracts the /proc and /sys reads the collector needs, so rate and
// percentage math can be tested without real processes.
type Reader interface {
	// ProcessCPU returns a process's accumulated CPU jiffies (utime+stime).
	ProcessCPU(pid int) (uint64, bool)
	// ProcessRSS returns a process's resident memory in bytes.
	ProcessRSS(pid int) (uint64, bool)
	// ProcessIO returns a process's cumulative block-layer read/write bytes.
	ProcessIO(pid int) (read, write uint64, ok bool)
	// ProcessFDs returns a process's count of open file descriptors.
	ProcessFDs(pid int) (uint64, bool)
	// ProcessThreads returns a process's thread count.
	ProcessThreads(pid int) (uint64, bool)
	// TotalMemory returns total and used machine memory in bytes.
	TotalMemory() (total, used uint64, ok bool)
	// SystemCPU returns busy and total jiffies from /proc/stat.
	SystemCPU() (busy, total uint64, ok bool)
	// LoadAverages returns the 1/5/15-minute load averages.
	LoadAverages() (l1, l5, l15 float64, ok bool)
	// NumCPU is the number of logical CPUs.
	NumCPU() int
	// ClockTicks is the kernel USER_HZ (jiffies per second).
	ClockTicks() float64
}

type cpuSample struct {
	ticks uint64
	at    time.Time
}

type ioSample struct {
	read  uint64
	write uint64
	at    time.Time
}

type sysSample struct {
	busy  uint64
	total uint64
}

// Collector samples service and system metrics, remembering the previous CPU
// sample to compute rates (section 12). It is safe for concurrent use by service
// workers; system metrics are cached briefly so concurrent workers in one cycle
// share a single system computation instead of corrupting the rate.
type Collector struct {
	Reader          Reader
	Now             func() time.Time
	SystemFreshness time.Duration

	mu            sync.Mutex
	prevService   map[string]cpuSample
	prevServiceIO map[string]ioSample
	prevSystem    *sysSample
	lastSystem    Snapshot
	lastSystemA   time.Time
}

// New returns a Collector over reader.
func New(reader Reader) *Collector {
	return &Collector{
		Reader:          reader,
		Now:             time.Now,
		SystemFreshness: 2 * time.Second,
		prevService:     map[string]cpuSample{},
		prevServiceIO:   map[string]ioSample{},
	}
}

// SampleService computes the service-scope metrics over its discovered process
// set — which includes the matched processes AND their descendants, so every
// metric below sums across the whole tree (parent + children): memory (RSS sum,
// bytes and % of RAM), cpu (rate %), process_count, io/io_read/io_write (rate,
// bytes/s), fds and threads.
func (c *Collector) SampleService(service string, pids []int) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.Now()
	snap := Snapshot{}

	// Swap is optional: only readers that implement ProcessSwap contribute a
	// per-service swap metric (summed over the tree, like RSS).
	swapReader, hasSwap := c.Reader.(interface {
		ProcessSwap(pid int) (uint64, bool)
	})

	var rss, ticks, ioRead, ioWrite, fds, threads, swap uint64
	for _, pid := range pids {
		if v, ok := c.Reader.ProcessRSS(pid); ok {
			rss += v
		}
		if v, ok := c.Reader.ProcessCPU(pid); ok {
			ticks += v
		}
		if hasSwap {
			if v, ok := swapReader.ProcessSwap(pid); ok {
				swap += v
			}
		}
		if rd, wr, ok := c.Reader.ProcessIO(pid); ok {
			ioRead += rd
			ioWrite += wr
		}
		if v, ok := c.Reader.ProcessFDs(pid); ok {
			fds += v
		}
		if v, ok := c.Reader.ProcessThreads(pid); ok {
			threads += v
		}
	}

	mem := Reading{Absolute: float64(rss), HasAbsolute: true, Ready: true}
	if total, _, ok := c.Reader.TotalMemory(); ok && total > 0 {
		mem.Percent = float64(rss) / float64(total) * 100
		mem.HasPercent = true
	}
	snap["memory"] = mem

	// Per-service swap: total swapped-out memory of the process tree (bytes), and
	// — when a swap device exists — its share of total swap.
	if hasSwap {
		sw := Reading{Absolute: float64(swap), HasAbsolute: true, Ready: true}
		if total, _, ok := readerTotalSwap(c.Reader); ok && total > 0 {
			sw.Percent = float64(swap) / float64(total) * 100
			sw.HasPercent = true
		}
		snap["swap"] = sw
	}

	snap["process_count"] = Reading{Absolute: float64(len(pids)), HasAbsolute: true, Ready: true}
	snap["fds"] = Reading{Absolute: float64(fds), HasAbsolute: true, Ready: true}
	snap["threads"] = Reading{Absolute: float64(threads), HasAbsolute: true, Ready: true}

	cur := cpuSample{ticks: ticks, at: now}
	cpu := Reading{HasPercent: true}
	if prev, ok := c.prevService[service]; ok {
		cpu = cpuRate(prev, cur, c.Reader.ClockTicks(), c.Reader.NumCPU())
	}
	c.prevService[service] = cur
	snap["cpu"] = cpu

	curIO := ioSample{read: ioRead, write: ioWrite, at: now}
	if prev, ok := c.prevServiceIO[service]; ok {
		snap["io_read"] = ioRate(prev.read, curIO.read, prev.at, curIO.at)
		snap["io_write"] = ioRate(prev.write, curIO.write, prev.at, curIO.at)
		snap["io"] = ioRate(prev.read+prev.write, curIO.read+curIO.write, prev.at, curIO.at)
	} else {
		notReady := Reading{HasAbsolute: true}
		snap["io_read"], snap["io_write"], snap["io"] = notReady, notReady, notReady
	}
	c.prevServiceIO[service] = curIO

	return snap
}

// ioRate computes a bytes/second rate from two cumulative samples. A drop in the
// total (a counter reset, or a child leaving the process set between cycles)
// clamps to 0 rather than underflowing.
func ioRate(prevBytes, curBytes uint64, prevAt, curAt time.Time) Reading {
	wall := curAt.Sub(prevAt).Seconds()
	if wall <= 0 {
		return Reading{HasAbsolute: true, Ready: false}
	}
	var rate float64
	if curBytes > prevBytes {
		rate = float64(curBytes-prevBytes) / wall
	}
	return Reading{Absolute: rate, HasAbsolute: true, Ready: true}
}

// SampleSystem computes the machine-scope metrics: total_memory (bytes and %),
// total_cpu (rate %), load1/5/15. The result is cached for SystemFreshness so
// concurrent workers share one computation per cycle.
func (c *Collector) SampleSystem() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.Now()
	if c.lastSystem != nil && now.Sub(c.lastSystemA) < c.SystemFreshness {
		return c.lastSystem
	}

	snap := Snapshot{}
	if total, used, ok := c.Reader.TotalMemory(); ok {
		r := Reading{Absolute: float64(used), HasAbsolute: true, Ready: true}
		if total > 0 {
			r.Percent = float64(used) / float64(total) * 100
			r.HasPercent = true
		}
		snap["total_memory"] = r
	}

	if busy, total, ok := c.Reader.SystemCPU(); ok {
		cpu := Reading{HasPercent: true}
		if c.prevSystem != nil {
			dBusy := busy - c.prevSystem.busy
			dTotal := total - c.prevSystem.total
			if dTotal > 0 {
				cpu.Percent = float64(dBusy) / float64(dTotal) * 100
				cpu.Ready = true
			}
		}
		c.prevSystem = &sysSample{busy: busy, total: total}
		snap["total_cpu"] = cpu
	}

	if l1, l5, l15, ok := c.Reader.LoadAverages(); ok {
		snap["load1"] = Reading{Absolute: l1, HasAbsolute: true, Ready: true}
		snap["load5"] = Reading{Absolute: l5, HasAbsolute: true, Ready: true}
		snap["load15"] = Reading{Absolute: l15, HasAbsolute: true, Ready: true}
	}

	// Swap is optional: only readers that implement TotalSwap contribute it, and
	// only when a swap device exists (total > 0). Percent is always computed so a
	// 0%-used swap still reports a value.
	if total, used, ok := readerTotalSwap(c.Reader); ok && total > 0 {
		snap["total_swap"] = Reading{
			Absolute:    float64(used),
			HasAbsolute: true,
			Percent:     float64(used) / float64(total) * 100,
			HasPercent:  true,
			Ready:       true,
		}
	}

	c.lastSystem = snap
	c.lastSystemA = now
	return snap
}

// readerTotalSwap returns the host swap totals when the reader implements the
// optional TotalSwap method (kept out of the core Reader interface so swap stays
// optional). Shared by the per-service swap metric and the system total_swap.
func readerTotalSwap(r Reader) (total, used uint64, ok bool) {
	if sr, has := r.(interface {
		TotalSwap() (total, used uint64, ok bool)
	}); has {
		return sr.TotalSwap()
	}
	return 0, 0, false
}

// Reset clears a service's CPU history (section 12: reset on reload).
func (c *Collector) Reset(service string) {
	c.mu.Lock()
	delete(c.prevService, service)
	delete(c.prevServiceIO, service)
	c.mu.Unlock()
}

// cpuRate computes CPU% = Δticks / hz / (Δwall * ncpu) * 100 (section 12).
func cpuRate(prev, cur cpuSample, hz float64, ncpu int) Reading {
	wall := cur.at.Sub(prev.at).Seconds()
	if wall <= 0 || ncpu <= 0 || hz <= 0 {
		return Reading{HasPercent: true, Ready: false}
	}
	cpuSeconds := float64(cur.ticks-prev.ticks) / hz
	pct := cpuSeconds / (wall * float64(ncpu)) * 100
	return Reading{Percent: pct, HasPercent: true, Ready: true}
}
