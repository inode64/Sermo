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

// procCPUSample remembers each process's CPU jiffies at a time, so the per-process
// (single-thread) CPU rate can be computed for the busiest process in the tree.
type procCPUSample struct {
	ticks map[int]uint64
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

	mu               sync.Mutex
	prevService      map[string]cpuSample
	prevServiceProcs map[string]procCPUSample
	prevServiceIO    map[string]ioSample
	prevSystem       *sysSample
	lastSystem       Snapshot
	lastSystemA      time.Time
}

// New returns a Collector over reader.
func New(reader Reader) *Collector {
	return &Collector{
		Reader:           reader,
		Now:              time.Now,
		SystemFreshness:  2 * time.Second,
		prevService:      map[string]cpuSample{},
		prevServiceProcs: map[string]procCPUSample{},
		prevServiceIO:    map[string]ioSample{},
	}
}

// SampleService computes the service-scope metrics over its discovered process
// set — which includes the matched processes AND their descendants, so every
// metric below sums across the whole tree (parent + children): memory (RSS sum,
// bytes and % of RAM), swap, cpu (whole-machine rate %), process_count,
// io/io_read/io_write (rate, bytes/s), fds and threads. cpu_thread is the one
// exception — it is the busiest single process's rate against one CPU thread, not
// a sum.
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
	curTicks := make(map[int]uint64, len(pids)) // per-process CPU jiffies this cycle
	for _, pid := range pids {
		if v, ok := c.Reader.ProcessRSS(pid); ok {
			rss += v
		}
		if v, ok := c.Reader.ProcessCPU(pid); ok {
			ticks += v
			curTicks[pid] = v
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

	// cpu_thread: the highest single-process CPU rate in the tree, normalized to a
	// single CPU thread (100% = one process saturating one core). Unlike `cpu`
	// (whole-machine), this catches a single-threaded process pegging its one
	// thread, which the machine-wide percentage would dilute across all cores.
	curProcs := procCPUSample{ticks: curTicks, at: now}
	snap["cpu_thread"] = maxProcCPURate(c.prevServiceProcs[service], curProcs, c.Reader.ClockTicks())
	c.prevServiceProcs[service] = curProcs

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

// ServiceCPU is the live CPU view of a service's process tree for the web UI:
// the whole-machine rate (CPU, % of all cores), the busiest single process
// against one core (CPUThread, 100% = one saturated core), and the per-process
// single-core rate (PerProc, keyed by PID). NumCPU is the logical CPU count, so
// the UI can label/normalize the bars.
type ServiceCPU struct {
	CPU       Reading
	CPUThread Reading
	PerProc   map[int]float64
	NumCPU    int
}

// SampleServiceCPU computes the live per-process and aggregate CPU rates for a
// service's process tree against the previous call for the same service. It is
// the web-only counterpart to SampleService's cpu/cpu_thread, adding the
// per-process breakdown the process table needs. It keeps its own prev state in
// prevServiceProcs, so it must run on a collector dedicated to live web
// sampling — never the engine's, or the two would corrupt each other's deltas.
func (c *Collector) SampleServiceCPU(service string, pids []int) ServiceCPU {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.Now()
	hz := c.Reader.ClockTicks()
	ncpu := c.Reader.NumCPU()
	curTicks := make(map[int]uint64, len(pids))
	for _, pid := range pids {
		if v, ok := c.Reader.ProcessCPU(pid); ok {
			curTicks[pid] = v
		}
	}
	cur := procCPUSample{ticks: curTicks, at: now}
	prev := c.prevServiceProcs[service]
	c.prevServiceProcs[service] = cur

	out := ServiceCPU{NumCPU: ncpu, CPU: Reading{HasPercent: true}, CPUThread: Reading{HasPercent: true}}
	if prev.ticks == nil || prev.at.IsZero() {
		return out // first observation: no delta yet
	}
	wall := now.Sub(prev.at).Seconds()
	if wall <= 0 || hz <= 0 {
		return out
	}
	out.PerProc = make(map[int]float64, len(curTicks))
	var sumDelta, maxPct float64
	for pid, curT := range curTicks {
		prevT, ok := prev.ticks[pid]
		if !ok || curT < prevT {
			continue // new process this cycle, or a counter reset: no rate
		}
		d := float64(curT - prevT)
		sumDelta += d
		pct := d / hz / wall * 100 // against one CPU thread (cpu_thread units)
		out.PerProc[pid] = pct
		if pct > maxPct {
			maxPct = pct
		}
	}
	out.CPUThread = Reading{Percent: maxPct, HasPercent: true, Ready: true}
	if ncpu > 0 {
		out.CPU = Reading{Percent: sumDelta / hz / (wall * float64(ncpu)) * 100, HasPercent: true, Ready: true}
	}
	return out
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
			r.Total, r.HasTotal = float64(total), true
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
			Total:       float64(total),
			HasTotal:    true,
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
	delete(c.prevServiceProcs, service)
	delete(c.prevServiceIO, service)
	c.mu.Unlock()
}

// maxProcCPURate computes the highest single-process CPU rate between two
// per-process samples, as a percentage of ONE CPU thread (100% = a process using
// a full core; a multi-threaded process may exceed 100%). Only PIDs present in
// both samples contribute (a process needs a baseline), and a lower current tick
// count (PID reuse / restart) is skipped. Not ready until there is a previous
// sample.
func maxProcCPURate(prev, cur procCPUSample, hz float64) Reading {
	if prev.ticks == nil || prev.at.IsZero() {
		return Reading{HasPercent: true}
	}
	wall := cur.at.Sub(prev.at).Seconds()
	if wall <= 0 || hz <= 0 {
		return Reading{HasPercent: true, Ready: false}
	}
	maxPct := 0.0
	for pid, curT := range cur.ticks {
		prevT, ok := prev.ticks[pid]
		if !ok || curT < prevT {
			continue
		}
		if pct := float64(curT-prevT) / hz / wall * 100; pct > maxPct {
			maxPct = pct
		}
	}
	return Reading{Percent: maxPct, HasPercent: true, Ready: true}
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
