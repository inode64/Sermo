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

// CPUThreadSampleFloorPercent is the per-process CPU rate, as a percentage of one
// core, at or above which a process's individual threads are read so cpu_thread can
// be measured rather than bounded.
//
// Below it the process rate itself is published as an upper bound: no single thread
// can exceed the whole process, so a bound never under-reports and a saturated core
// can never hide behind one. It can only over-report, and only under this floor —
// which is why the floor must stay far below any rule threshold anyone would set
// (the packaged catalog alerts at 90%). Raising it near a rule's threshold would let
// a bound trip an alert the measurement would not.
//
// The floor exists because the read is not free: one file per thread per cycle, and
// a container runtime idling with 800 threads would otherwise pay for all of them.
const CPUThreadSampleFloorPercent = 5.0

// procCPUSample remembers each process's CPU jiffies at a time, plus per-thread
// jiffies for the processes busy enough to be worth reading, so the busiest single
// thread in the tree can be measured against one core.
type procCPUSample struct {
	ticks map[int]uint64
	// threadTicks is pid -> tid -> jiffies, populated only for the pids that met
	// CPUThreadSampleFloorPercent when this sample was taken. A rate needs the same
	// pid in two consecutive samples, so a process that just became busy is bounded
	// for one cycle and measured from the next.
	threadTicks map[int]map[int]uint64
	at          time.Time
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

// ForgetService drops rate baselines for a service-like sampling key. Dynamic
// consumers such as interactive sessions call it when a target disappears so
// short-lived identities cannot grow the collector maps without bound.
func (c *Collector) ForgetService(service string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.prevService, service)
	delete(c.prevServiceProcs, service)
	delete(c.prevServiceIO, service)
}

// SampleService computes the service-scope metrics over its discovered process
// set — which includes the matched processes AND their descendants, so every
// metric below sums across the whole tree (parent + children): memory (RSS sum,
// bytes and % of RAM), swap, cpu (whole-machine rate %), process_count,
// io/io_read/io_write (rate, bytes/s), fds and threads. cpu_thread is the one
// exception — it is the busiest single thread's rate against one CPU thread, not
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
	curTicks := make(map[int]uint64, len(pids))   // per-process CPU jiffies this cycle
	curThreads := make(map[int]uint64, len(pids)) // per-process thread count, for the cpu_thread floor
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
			curThreads[pid] = v
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

	// cpu_thread: the most a single core was used on this tree's behalf — the
	// busiest individual thread, normalized so 100% is one saturated core. Unlike
	// `cpu` (whole-machine), it catches one pegged thread that the machine-wide
	// percentage dilutes across every core; and unlike a per-process maximum, it does
	// not add a multithreaded process's threads together and report more than any one
	// core could deliver.
	snap[MetricCPUThread] = c.sampleMaxCore(service, curTicks, curThreads, now).Reading

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

// maxCoreSample is one cycle's CPU result for a service's process tree: the
// cpu_thread aggregate, the per-process single-core rates it was derived from, and
// per process the busiest-thread figure plus whether that figure is a measurement or
// the process-rate upper bound.
//
// Readiness lives in Reading.Ready — the maps are nil in exactly the same case, so a
// second flag would be one more thing that can disagree with itself.
type maxCoreSample struct {
	Reading   Reading
	ProcRates map[int]float64
	MaxCore   map[int]float64
	Exact     map[int]bool
}

// sampleMaxCore advances one service's per-process and per-thread CPU state and
// returns that cycle's result.
//
// It is the single implementation behind both cpu_thread paths — the stored metric in
// SampleService and the live view in SampleServiceCPU — so the number the Web UI
// shows and the number rules fire on cannot drift apart. It returns ProcRates because
// it must compute them anyway to apply the sampling floor; recomputing them in the
// caller would read prevServiceProcs after this call already advanced it, and compare
// a sample against itself.
//
// Both paths keep their state in prevServiceProcs, which is why the two must run on
// separate collectors. The caller must hold c.mu.
func (c *Collector) sampleMaxCore(service string, curTicks, threadCounts map[int]uint64, now time.Time) maxCoreSample {
	hz := c.Reader.ClockTicks()
	prev := c.prevServiceProcs[service]
	cur := procCPUSample{ticks: curTicks, at: now}
	procRates, ready := perProcCPURates(prev, cur, hz)
	// Threads are read at this sample's instant for the processes busy enough to
	// warrant it, so the next cycle can turn them into a rate. On the first
	// observation there are no rates yet, so nothing qualifies.
	if ready {
		cur.threadTicks = readThreadTicks(c.Reader, threadSampleTargets(procRates, threadCounts))
	}
	c.prevServiceProcs[service] = cur
	if !ready {
		return maxCoreSample{Reading: Reading{HasPercent: true}}
	}
	values, exact := maxCoreRates(prev, cur, hz, procRates)
	return maxCoreSample{
		Reading:   maxCoreReading(values),
		ProcRates: procRates,
		MaxCore:   values,
		Exact:     exact,
	}
}

// ServiceCPU is the live CPU view of a service's process tree for the web UI: the
// whole-machine rate (CPU, % of all cores), the busiest single thread against one
// core (CPUThread, 100% = one saturated core), the per-process single-core rate
// (PerProc, keyed by PID) and that same busiest-thread figure per process
// (PerProcMaxCore, with PerProcMaxCoreExact saying which entries are measurements
// rather than upper bounds). NumCPU is the logical CPU count, so the UI can
// label/normalize the bars.
type ServiceCPU struct {
	CPU                 Reading
	CPUThread           Reading
	PerProc             map[int]float64
	PerProcMaxCore      map[int]float64
	PerProcMaxCoreExact map[int]bool
	NumCPU              int
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
	ncpu := c.Reader.NumCPU()
	curTicks := make(map[int]uint64, len(pids))
	curThreads := make(map[int]uint64, len(pids))
	for _, pid := range pids {
		if v, ok := c.Reader.ProcessCPU(pid); ok {
			curTicks[pid] = v
		}
		if v, ok := c.Reader.ProcessThreads(pid); ok {
			curThreads[pid] = v
		}
	}

	out := ServiceCPU{NumCPU: ncpu, CPU: Reading{HasPercent: true}, CPUThread: Reading{HasPercent: true}}
	sample := c.sampleMaxCore(service, curTicks, curThreads, now)
	if !sample.Reading.Ready {
		return out // first observation (or no time elapsed): no delta yet
	}
	out.PerProc = sample.ProcRates
	out.CPUThread = sample.Reading
	out.PerProcMaxCore, out.PerProcMaxCoreExact = sample.MaxCore, sample.Exact
	// The whole-machine rate is the sum of the per-process rates spread over the
	// cores (each rate is already a percentage of one core, so Σ/ncpu is the
	// percentage of all of them).
	var sum float64
	for _, pct := range sample.ProcRates {
		sum += pct
	}
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
		r := Reading{Absolute: float64(totals.memoryUsed), Unit: MetricUnitBytes, HasAbsolute: true, Ready: true,
			Percent:    float64(totals.memoryUsed) / float64(totals.memoryTotal) * PercentScale,
			HasPercent: true}
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

// maxCoreRates reports, per process, the highest CPU rate any one of its threads
// sustained against one core — the most a single core was used on that process's
// behalf — and whether each figure is a measurement or the process-rate upper bound.
//
// A process is measured when the same pid carries per-thread jiffies in both
// samples; otherwise its own rate stands in, which is exact for a single-threaded
// process and a true upper bound for the rest.
func maxCoreRates(prev, cur procCPUSample, hz float64, procRates map[int]float64) (map[int]float64, map[int]bool) {
	wall := cur.at.Sub(prev.at).Seconds()
	values := make(map[int]float64, len(procRates))
	exact := make(map[int]bool, len(procRates))
	for pid, procRate := range procRates {
		peak, ok := maxThreadRate(prev.threadTicks[pid], cur.threadTicks[pid], hz, wall)
		// A measured peak above the process's own rate cannot happen physically;
		// clamping keeps a mid-cycle thread churn from reporting more than the
		// process as a whole did.
		if ok && peak <= procRate {
			values[pid], exact[pid] = peak, true
			continue
		}
		// A zero bound is not an estimate: rates cannot be negative, so a process that
		// used no CPU had no thread use any. Reporting it as approximate would put a
		// "≤" on the common idle row and imply uncertainty that does not exist.
		values[pid], exact[pid] = procRate, procRate == 0
	}
	return values, exact
}

// maxThreadRate is the busiest thread's rate between two per-thread tick maps, or
// ok=false when either sample is missing. A thread absent from the previous sample,
// or whose counter went backwards (a tid recycled between cycles), is skipped the
// same way perProcCPURates skips a replaced pid.
func maxThreadRate(prev, cur map[int]uint64, hz, wall float64) (float64, bool) {
	if len(prev) == 0 || len(cur) == 0 || wall <= 0 || hz <= 0 {
		return 0, false
	}
	peak, found := 0.0, false
	for tid, curT := range cur {
		prevT, ok := prev[tid]
		if !ok || curT < prevT {
			continue
		}
		found = true
		if rate := float64(curT-prevT) / hz / wall * PercentScale; rate > peak {
			peak = rate
		}
	}
	return peak, found
}

// maxCoreReading is the service-level cpu_thread: the highest per-process figure in
// the tree. Callers handle the no-previous-sample case before reaching here, since
// that is also when there is nothing to read threads for.
func maxCoreReading(values map[int]float64) Reading {
	peak := 0.0
	for _, pct := range values {
		if pct > peak {
			peak = pct
		}
	}
	return Reading{Percent: peak, HasPercent: true, Ready: true}
}

// threadSampleTargets picks the pids whose threads to read this cycle: those at or
// above the floor. Processes with a single thread are skipped — their own rate is
// already exact.
//
// The condition is the current rate and nothing else, deliberately. Carrying the
// previous cycle's per-thread state forward as a reason to keep sampling would latch:
// each cycle repopulates that state, so one spike above the floor would pin a
// hundreds-of-threads process to a read per thread per cycle, forever — the cost the
// floor exists to bound. The price is that a single-cycle spike is bounded rather than
// measured, since a per-thread rate needs two consecutive samples. Alert rules use
// `for:` durations, so anything sustained enough to fire is sampled on both cycles.
func threadSampleTargets(procRates map[int]float64, threadCounts map[int]uint64) []int {
	var targets []int
	for pid, rate := range procRates {
		if threadCounts[pid] > 1 && rate >= CPUThreadSampleFloorPercent {
			targets = append(targets, pid)
		}
	}
	return targets
}

// threadCPUReader is the optional half of Reader that resolves cpu_thread to a
// measurement instead of an upper bound. It is optional — like ProcessSwap — so a
// reader that cannot enumerate threads still produces a correct, if bounded,
// cpu_thread rather than failing.
type threadCPUReader interface {
	// ProcessThreadCPU returns accumulated CPU jiffies per thread, keyed by tid.
	// Callers gate it behind CPUThreadSampleFloorPercent: it costs one read per
	// thread.
	ProcessThreadCPU(pid int) (map[int]uint64, bool)
}

// readThreadTicks reads per-thread jiffies for the given pids, or nothing at all
// when the reader cannot enumerate threads.
func readThreadTicks(reader Reader, pids []int) map[int]map[int]uint64 {
	threads, ok := reader.(threadCPUReader)
	if !ok || len(pids) == 0 {
		return nil
	}
	out := make(map[int]map[int]uint64, len(pids))
	for _, pid := range pids {
		if ticks, ok := threads.ProcessThreadCPU(pid); ok && len(ticks) > 0 {
			out[pid] = ticks
		}
	}
	return out
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
