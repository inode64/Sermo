package metrics

import (
	"sync"
	"time"
)

// Service metric names: the per-service Snapshot keys the collector emits and
// that checks/rules reference. Centralized so the vocabulary cannot drift.
const (
	MetricMemory       = "memory"
	MetricSwap         = "swap"
	MetricProcessCount = "process_count"
	MetricFds          = "fds"
	MetricThreads      = "threads"
	MetricCPU          = "cpu"
	MetricCPUThread    = "cpu_thread"
	MetricIORead       = "io_read"
	MetricIOWrite      = "io_write"
	MetricIO           = "io"
)

// System metric names: the host-scope Snapshot keys (whole-machine totals and
// load averages).
const (
	MetricTotalCPU    = "total_cpu"
	MetricTotalMemory = "total_memory"
	MetricTotalSwap   = "total_swap"
	MetricLoad1       = "load1"
	MetricLoad5       = "load5"
	MetricLoad15      = "load15"
)

const defaultSystemFreshness = 2 * time.Second

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
// sample to compute rates. It is safe for concurrent use by service
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
		SystemFreshness:  defaultSystemFreshness,
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
	// Track how many processes actually contributed a successful read per metric,
	// so a gauge that summed nothing (every /proc read failed because the tree
	// exited or is unreadable) is reported as not-ready rather than a measured 0.
	// Otherwise a threshold like `fds < N` would fire spuriously and `fds > N`
	// could never fire. `present` (RSS-readable PIDs) is the alive-process count.
	var present, fdsOK, threadsOK int
	curTicks := make(map[int]uint64, len(pids)) // per-process CPU jiffies this cycle
	for _, pid := range pids {
		if v, ok := c.Reader.ProcessRSS(pid); ok {
			rss += v
			present++
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
			fdsOK++
		}
		if v, ok := c.Reader.ProcessThreads(pid); ok {
			threads += v
			threadsOK++
		}
	}

	// A per-process gauge is ready when at least one process contributed, or when
	// the tree is genuinely empty (no PIDs to read — a true zero). It is not ready
	// when PIDs were requested but none could be read (measurement failure).
	measured := func(ok bool) bool { return len(pids) == 0 || ok }

	mem := Reading{Absolute: float64(rss), Unit: MetricUnitBytes, HasAbsolute: true, Ready: measured(present > 0)}
	totals := readerMemoryTotals(c.Reader, hasSwap)
	if totals.memoryOK {
		mem.Percent = float64(rss) / float64(totals.memoryTotal) * PercentScale
		mem.HasPercent = true
	}
	snap[MetricMemory] = mem

	// Per-service swap: total swapped-out memory of the process tree (bytes), and
	// — when a swap device exists — its share of total swap.
	if hasSwap {
		sw := Reading{Absolute: float64(swap), Unit: MetricUnitBytes, HasAbsolute: true, Ready: measured(present > 0)}
		if totals.swapOK && totals.swapTotal > 0 {
			sw.Percent = float64(swap) / float64(totals.swapTotal) * PercentScale
			sw.HasPercent = true
		}
		snap[MetricSwap] = sw
	}

	// process_count is the number of processes actually found alive this sample,
	// not the count of PIDs handed in (some may have exited since discovery).
	snap[MetricProcessCount] = Reading{Absolute: float64(present), HasAbsolute: true, Ready: measured(present > 0)}
	snap[MetricFds] = Reading{Absolute: float64(fds), HasAbsolute: true, Ready: measured(fdsOK > 0)}
	snap[MetricThreads] = Reading{Absolute: float64(threads), HasAbsolute: true, Ready: measured(threadsOK > 0)}

	cur := cpuSample{ticks: ticks, at: now}
	cpu := Reading{HasPercent: true}
	if prev, ok := c.prevService[service]; ok {
		cpu = cpuRate(prev, cur, c.Reader.ClockTicks(), c.Reader.NumCPU())
	}
	c.prevService[service] = cur
	snap[MetricCPU] = cpu

	// cpu_thread: the highest single-process CPU rate in the tree, normalized to a
	// single CPU thread (100% = one process saturating one core). Unlike `cpu`
	// (whole-machine), this catches a single-threaded process pegging its one
	// thread, which the machine-wide percentage would dilute across all cores.
	curProcs := procCPUSample{ticks: curTicks, at: now}
	snap[MetricCPUThread] = maxProcCPURate(c.prevServiceProcs[service], curProcs, c.Reader.ClockTicks())
	c.prevServiceProcs[service] = curProcs

	curIO := ioSample{read: ioRead, write: ioWrite, at: now}
	if prev, ok := c.prevServiceIO[service]; ok {
		snap[MetricIORead] = ioRate(prev.read, curIO.read, prev.at, curIO.at)
		snap[MetricIOWrite] = ioRate(prev.write, curIO.write, prev.at, curIO.at)
		snap[MetricIO] = ioRate(prev.read+prev.write, curIO.read+curIO.write, prev.at, curIO.at)
	} else {
		notReady := Reading{Unit: MetricUnitBytesPerSecond, HasAbsolute: true}
		snap[MetricIORead], snap[MetricIOWrite], snap[MetricIO] = notReady, notReady, notReady
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
	rates, ready := perProcCPURates(prev, cur, hz)
	if !ready {
		return out // first observation (or no time elapsed): no delta yet
	}
	out.PerProc = rates
	// cpu_thread is the busiest single process; the whole-machine rate is the sum
	// of the per-process rates spread over the cores (each rate is already a
	// percentage of one thread, so Σ/ncpu is the percentage of all of them).
	var sum, peak float64
	for _, pct := range rates {
		sum += pct
		if pct > peak {
			peak = pct
		}
	}
	out.CPUThread = Reading{Percent: peak, HasPercent: true, Ready: true}
	if ncpu > 0 {
		out.CPU = Reading{Percent: sum / float64(ncpu), HasPercent: true, Ready: true}
	}
	return out
}

// ioRate computes a bytes/second rate from two cumulative samples. A drop in the
// total (a counter reset, or a child leaving the process set between cycles)
// clamps to 0 rather than underflowing.
func ioRate(prevBytes, curBytes uint64, prevAt, curAt time.Time) Reading {
	wall := curAt.Sub(prevAt).Seconds()
	if wall <= 0 {
		return Reading{Unit: MetricUnitBytesPerSecond, HasAbsolute: true, Ready: false}
	}
	var rate float64
	if curBytes > prevBytes {
		rate = float64(curBytes-prevBytes) / wall
	}
	return Reading{Absolute: rate, Unit: MetricUnitBytesPerSecond, HasAbsolute: true, Ready: true}
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
	totals := readerMemoryTotals(c.Reader, true)
	if totals.memoryOK {
		r := Reading{Absolute: float64(totals.memoryUsed), Unit: MetricUnitBytes, HasAbsolute: true, Ready: true}
		r.Percent = float64(totals.memoryUsed) / float64(totals.memoryTotal) * PercentScale
		r.HasPercent = true
		r.Total, r.HasTotal = float64(totals.memoryTotal), true
		snap[MetricTotalMemory] = r
	}

	if busy, total, ok := c.Reader.SystemCPU(); ok {
		cpu := Reading{HasPercent: true}
		// Require both counters to advance. A backward jump (a counter reset)
		// would underflow these unsigned deltas into a huge bogus rate that could
		// spuriously trip a total_cpu threshold — the same guard ioRate and the
		// per-process samplers already apply.
		if c.prevSystem != nil && busy >= c.prevSystem.busy && total > c.prevSystem.total {
			dBusy := busy - c.prevSystem.busy
			dTotal := total - c.prevSystem.total
			cpu.Percent = float64(dBusy) / float64(dTotal) * PercentScale
			cpu.Ready = true
		}
		c.prevSystem = &sysSample{busy: busy, total: total}
		snap[MetricTotalCPU] = cpu
	}

	if l1, l5, l15, ok := c.Reader.LoadAverages(); ok {
		snap[MetricLoad1] = Reading{Absolute: l1, HasAbsolute: true, Ready: true}
		snap[MetricLoad5] = Reading{Absolute: l5, HasAbsolute: true, Ready: true}
		snap[MetricLoad15] = Reading{Absolute: l15, HasAbsolute: true, Ready: true}
	}

	// Swap is optional: only readers that implement TotalSwap contribute it, and
	// only when a swap device exists (total > 0). Percent is always computed so a
	// 0%-used swap still reports a value.
	if totals.swapOK && totals.swapTotal > 0 {
		snap[MetricTotalSwap] = Reading{
			Absolute:    float64(totals.swapUsed),
			Unit:        MetricUnitBytes,
			HasAbsolute: true,
			Percent:     float64(totals.swapUsed) / float64(totals.swapTotal) * PercentScale,
			HasPercent:  true,
			Total:       float64(totals.swapTotal),
			HasTotal:    true,
			Ready:       true,
		}
	}

	c.lastSystem = snap
	c.lastSystemA = now
	return snap
}

type memoryTotals struct {
	memoryTotal uint64
	memoryUsed  uint64
	memoryOK    bool
	swapTotal   uint64
	swapUsed    uint64
	swapOK      bool
}

// readerMemoryTotals returns host memory totals and, when requested and
// supported, swap totals. Readers can implement TotalMemoryAndSwap to supply
// both from one underlying probe; older readers still use the separate methods.
func readerMemoryTotals(r Reader, needSwap bool) memoryTotals {
	if mr, has := r.(interface {
		TotalMemoryAndSwap() (memoryTotal, memoryUsed, swapTotal, swapUsed uint64, memoryOK, swapOK bool)
	}); has {
		memoryTotal, memoryUsed, swapTotal, swapUsed, memoryOK, swapOK := mr.TotalMemoryAndSwap()
		if !needSwap {
			swapTotal, swapUsed, swapOK = 0, 0, false
		}
		return memoryTotals{
			memoryTotal: memoryTotal,
			memoryUsed:  memoryUsed,
			memoryOK:    memoryOK,
			swapTotal:   swapTotal,
			swapUsed:    swapUsed,
			swapOK:      swapOK,
		}
	}
	memoryTotal, memoryUsed, memoryOK := r.TotalMemory()
	totals := memoryTotals{memoryTotal: memoryTotal, memoryUsed: memoryUsed, memoryOK: memoryOK}
	if !needSwap {
		return totals
	}
	if sr, has := r.(interface {
		TotalSwap() (total, used uint64, ok bool)
	}); has {
		totals.swapTotal, totals.swapUsed, totals.swapOK = sr.TotalSwap()
	}
	return totals
}

// Reset clears a service's CPU history.
func (c *Collector) Reset(service string) {
	c.mu.Lock()
	delete(c.prevService, service)
	delete(c.prevServiceProcs, service)
	delete(c.prevServiceIO, service)
	c.mu.Unlock()
}

// perProcCPURates returns each PID's CPU rate as a percentage of ONE CPU thread
// (Δticks / hz / Δwall * 100; 100% = a process pegging a full core, and a
// multi-threaded process may exceed it) between two per-process samples. Only
// PIDs present in both contribute (a process needs a baseline), and a lower
// current tick count (PID reuse / restart) is skipped. ready is false until a
// usable previous sample exists, so callers can mark their Reading not-ready.
func perProcCPURates(prev, cur procCPUSample, hz float64) (rates map[int]float64, ready bool) {
	if prev.ticks == nil || prev.at.IsZero() {
		return nil, false
	}
	wall := cur.at.Sub(prev.at).Seconds()
	if wall <= 0 || hz <= 0 {
		return nil, false
	}
	rates = make(map[int]float64, len(cur.ticks))
	for pid, curT := range cur.ticks {
		prevT, ok := prev.ticks[pid]
		if !ok || curT < prevT {
			continue
		}
		rates[pid] = float64(curT-prevT) / hz / wall * PercentScale
	}
	return rates, true
}

// maxProcCPURate is the highest single-process CPU rate between two per-process
// samples (the service cpu_thread metric). Not ready until there is a previous
// sample.
func maxProcCPURate(prev, cur procCPUSample, hz float64) Reading {
	rates, ready := perProcCPURates(prev, cur, hz)
	if !ready {
		return Reading{HasPercent: true}
	}
	peak := 0.0
	for _, pct := range rates {
		if pct > peak {
			peak = pct
		}
	}
	return Reading{Percent: peak, HasPercent: true, Ready: true}
}

// cpuRate computes CPU% = Δticks / hz / (Δwall * ncpu) * 100. A drop
// in the cumulative tick count — a worker restarting, or a busy PID leaving the
// matched set and being replaced by a fresh one starting at zero — clamps to 0
// rather than underflowing the unsigned subtraction into a bogus huge rate (the
// same guard ioRate and perProcCPURates apply).
func cpuRate(prev, cur cpuSample, hz float64, ncpu int) Reading {
	wall := cur.at.Sub(prev.at).Seconds()
	if wall <= 0 || ncpu <= 0 || hz <= 0 {
		return Reading{HasPercent: true, Ready: false}
	}
	var cpuSeconds float64
	if cur.ticks > prev.ticks {
		cpuSeconds = float64(cur.ticks-prev.ticks) / hz
	}
	pct := cpuSeconds / (wall * float64(ncpu)) * PercentScale
	return Reading{Percent: pct, HasPercent: true, Ready: true}
}
