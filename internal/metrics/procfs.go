package metrics

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sermo/internal/hostfs"
	"strconv"
	"strings"
	"time"

	"sermo/internal/process"
	"sermo/internal/units"
)

// LinuxClockTicks is the conventional kernel USER_HZ on Linux. The Go runtime does
// not expose sysconf(SC_CLK_TCK); 100 is correct on virtually all Linux builds.
const LinuxClockTicks = 100.0

// pageSize is used to convert statm resident pages to bytes.
var pageSize = uint64(os.Getpagesize())

const (
	procRoot          = "/proc"
	procLineSeparator = "\n"
)

// procfs file names read directly under /proc.
const (
	procFileLoadavg = "loadavg"
	procFileMeminfo = "meminfo"
	procFileStat    = "stat"
)

const (
	procStatUTimeIndex            = 11
	procStatSTimeIndex            = 12
	procStatmResidentPagesIndex   = 1
	procStatCPULabelIndex         = 0
	procStatCPUValuesStartIndex   = procStatCPULabelIndex + 1
	procStatAggregateMinFields    = 5
	procStatCPUPrefix             = "cpu"
	procStatBootTimePrefix        = "btime "
	procStatIdleValueOffset       = 3
	procStatIOWaitValueOffset     = 4
	procLoadAvg1Index             = 0
	procLoadAvg5Index             = 1
	procLoadAvg15Index            = 2
	procLoadAvgMinFields          = 3
	procMeminfoMemTotalPrefix     = "MemTotal:"
	procMeminfoMemAvailablePrefix = "MemAvailable:"
	procMeminfoSwapTotalPrefix    = "SwapTotal:"
	procMeminfoSwapFreePrefix     = "SwapFree:"
	procStatusVMSwapPrefix        = "VmSwap:"
	procIOReadBytesPrefix         = "read_bytes:"
	procIOWriteBytesPrefix        = "write_bytes:"
	meminfoValueIndex             = 1
	procDecimalBase               = 10
	procUintBits                  = 64
	procFloatBits                 = 64
)

func procPath(name string) string {
	return filepath.Join(procRoot, name)
}

// OSReader reads metrics from the host /proc filesystem.
type OSReader struct{}

// ProcessCPU sums utime (field 14) and stime (field 15) of /proc/<pid>/stat.
func (OSReader) ProcessCPU(pid int) (uint64, bool) {
	fields, ok := process.StatFields(pid)
	if !ok {
		return 0, false
	}
	return cpuTicks(fields)
}

// ProcessThreadCPU sums utime+stime for every thread of pid, keyed by tid, from
// /proc/<pid>/task/<tid>/stat.
//
// This is the only way to attribute CPU to a single core: procfs exposes no
// per-core breakdown per process, so the busiest thread's rate is what stands in
// for "the most a single core was used by this process". Callers must keep it
// behind a threshold — a process with hundreds of threads costs one read each,
// every cycle (see CPUThreadSampleFloorPercent).
//
// A thread that exits between listing the directory and its read is skipped, the
// same way a vanished process is; ok is false only when the task directory itself
// cannot be read.
func (OSReader) ProcessThreadCPU(pid int) (map[int]uint64, bool) {
	entries, err := hostfs.ReadDir(process.PIDPath(pid, process.ProcFileTask))
	if err != nil {
		return nil, false
	}
	out := make(map[int]uint64, len(entries))
	for _, entry := range entries {
		tid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		fields, ok := process.ThreadStatFields(pid, tid)
		if !ok {
			continue
		}
		if ticks, ok := cpuTicks(fields); ok {
			out[tid] = ticks
		}
	}
	return out, true
}

// cpuTicks sums utime (field 14, index 11 post-comm) and stime (field 15, index
// 12) from already-split stat fields, so the process and per-thread readers share
// one decoder.
func cpuTicks(fields []string) (uint64, bool) {
	if len(fields) <= procStatSTimeIndex {
		return 0, false
	}
	utime, err1 := strconv.ParseUint(fields[procStatUTimeIndex], procDecimalBase, procUintBits)
	stime, err2 := strconv.ParseUint(fields[procStatSTimeIndex], procDecimalBase, procUintBits)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return utime + stime, true
}

// ProcessStartTime reads field 22 of /proc/<pid>/stat and converts it to a wall
// clock timestamp using the system boot time from /proc/stat.
func (r OSReader) ProcessStartTime(pid int) (time.Time, bool) {
	_, at, ok := r.ProcessStart(pid)
	return at, ok
}

// ProcessStart reads a process's start time from a single /proc/<pid>/stat read,
// as both raw clock ticks since boot and a wall clock timestamp.
//
// The two are not interchangeable. The wall clock value answers "how old is this
// process", but it is re-derived from the boot time, which the kernel itself
// re-derives from the wall clock — so a clock step (chronyd, and sermo's own
// `then.makestep`) moves it for a process that never restarted. The tick count is
// taken against the monotonic boot clock and never moves, so it is what callers
// must compare to decide whether a PID still holds the same process.
func (OSReader) ProcessStart(pid int) (uint64, time.Time, bool) {
	startTicks, ok := process.StartTicks(pid)
	if !ok {
		return 0, time.Time{}, false
	}
	boot, ok := procBootTime()
	if !ok {
		return 0, time.Time{}, false
	}
	startSeconds := float64(startTicks) / LinuxClockTicks
	whole := int64(startSeconds)
	nsec := int64((startSeconds - float64(whole)) * float64(time.Second))
	return startTicks, time.Unix(boot+whole, nsec), true
}

// procStatCacheTTL bounds CPU hotplug and boot-time changes after clock steps.
// These nearly static fields share one /proc/stat read across process samples.
const procStatCacheTTL = 5 * time.Second

type procStatObservation struct {
	bootTime int64
	bootOK   bool
	numCPU   int
}

var hostProcStat cachedSample[procStatObservation]

func procStatSample() procStatObservation {
	var sample procStatObservation
	_ = hostProcStat.readInto(time.Now(), procStatCacheTTL, func() (procStatObservation, bool, error) {
		data, err := hostfs.ReadFile(procPath(procFileStat))
		if err != nil {
			return procStatObservation{}, false, fmt.Errorf("read proc stat: %w", err)
		}
		boot, ok := procBootTimeValue(ScanUintField(string(data), procStatBootTimePrefix))
		count := countCPULines(data)
		return procStatObservation{bootTime: boot, bootOK: ok, numCPU: count}, ok && count > 0, nil
	}, &sample)
	return sample
}

// procBootTime returns the system boot time in seconds since the epoch.
func procBootTime() (int64, bool) {
	sample := procStatSample()
	return sample.bootTime, sample.bootOK
}

func procBootTimeValue(sec uint64, ok bool) (int64, bool) {
	if !ok || sec > math.MaxInt64 {
		return 0, false
	}
	return int64(sec), true
}

// ScanUintField scans newline-separated procfs/sysfs text for the first line
// with the given prefix and parses the remainder as an unsigned decimal. It
// reports false when the prefix is absent or the value does not parse.
func ScanUintField(data, prefix string) (uint64, bool) {
	for line := range strings.SplitSeq(data, procLineSeparator) {
		if v, ok := strings.CutPrefix(line, prefix); ok {
			n, err := strconv.ParseUint(strings.TrimSpace(v), procDecimalBase, procUintBits)
			return n, err == nil
		}
	}
	return 0, false
}

// ProcessRSS reads resident pages (field 2 of /proc/<pid>/statm) as bytes.
func (OSReader) ProcessRSS(pid int) (uint64, bool) {
	data, err := hostfs.ReadFile(process.PIDPath(pid, process.ProcFileStatm))
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) <= procStatmResidentPagesIndex {
		return 0, false
	}
	pages, err := strconv.ParseUint(fields[procStatmResidentPagesIndex], procDecimalBase, procUintBits)
	if err != nil {
		return 0, false
	}
	return pages * pageSize, true
}

// ProcessSwap reads VmSwap (swapped-out anonymous memory) from
// /proc/<pid>/status as bytes. A process with nothing swapped reports 0; a
// process without a VmSwap line (e.g. a kernel thread) also reports 0, true. ok
// is false only when the file cannot be read.
func (OSReader) ProcessSwap(pid int) (uint64, bool) {
	data, err := hostfs.ReadFile(process.PIDPath(pid, process.ProcFileStatus))
	if err != nil {
		return 0, false
	}
	for line := range strings.SplitSeq(string(data), procLineSeparator) {
		if strings.HasPrefix(line, procStatusVMSwapPrefix) {
			return parseMeminfoKB(line)
		}
	}
	return 0, true // no VmSwap line -> nothing swapped
}

// ProcessIO reads read_bytes and write_bytes (actual block-layer I/O) from
// /proc/<pid>/io. Reading another user's io requires privilege, so ok is false
// when the file cannot be read.
func (OSReader) ProcessIO(pid int) (read, write uint64, ok bool) {
	data, err := hostfs.ReadFile(process.PIDPath(pid, process.ProcFileIO))
	if err != nil {
		return 0, 0, false
	}
	return parseProcIO(string(data))
}

func parseProcIO(data string) (read, write uint64, ok bool) {
	read, haveR := ScanUintField(data, procIOReadBytesPrefix)
	write, haveW := ScanUintField(data, procIOWriteBytesPrefix)
	if !haveR || !haveW {
		return 0, 0, false
	}
	return read, write, true
}

// ProcessFDs counts the entries in /proc/<pid>/fd (open file descriptors).
// Reading another user's fd dir requires privilege, so ok is false when it
// cannot be read.
func (OSReader) ProcessFDs(pid int) (uint64, bool) {
	return processEntryCount(pid, process.ProcFileFD)
}

const (
	// procLimitsOpenFilesPrefix labels the RLIMIT_NOFILE row of /proc/<pid>/limits.
	procLimitsOpenFilesPrefix = "Max open files"
	// procLimitsSoftIndex is the soft-limit column once the label is stripped.
	procLimitsSoftIndex = 0
	procLimitsUnlimited = "unlimited"
)

// ProcessFDLimit reads the process's soft RLIMIT_NOFILE from /proc/<pid>/limits:
// the ceiling its own open-descriptor count is measured against. ok is false
// when the file is unreadable or the limit is unlimited.
func (OSReader) ProcessFDLimit(pid int) (uint64, bool) {
	data, err := hostfs.ReadFile(process.PIDPath(pid, process.ProcFileLimits))
	if err != nil {
		return 0, false
	}
	return parseProcLimitsOpenFiles(string(data))
}

func parseProcLimitsOpenFiles(data string) (uint64, bool) {
	for line := range strings.Lines(data) {
		rest, found := strings.CutPrefix(line, procLimitsOpenFilesPrefix)
		if !found {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) <= procLimitsSoftIndex || fields[procLimitsSoftIndex] == procLimitsUnlimited {
			return 0, false
		}
		limit, err := strconv.ParseUint(fields[procLimitsSoftIndex], procDecimalBase, procUintBits)
		if err != nil || limit == 0 {
			return 0, false
		}
		return limit, true
	}
	return 0, false
}

// ProcessThreads counts the entries in /proc/<pid>/task (the process's threads).
func (OSReader) ProcessThreads(pid int) (uint64, bool) {
	return processEntryCount(pid, process.ProcFileTask)
}

func processEntryCount(pid int, name string) (uint64, bool) {
	entries, err := hostfs.ReadDir(process.PIDPath(pid, name))
	if err != nil {
		return 0, false
	}
	return uint64(len(entries)), true
}

// MemoryTotals reads memory and swap counters from a single /proc/meminfo sample.
func (OSReader) MemoryTotals(maxAge time.Duration) MemoryTotals { return readProcMeminfoTotals(maxAge) }

func readProcMeminfoTotals(maxAge time.Duration) MemoryTotals {
	m, err := ReadMeminfo(maxAge)
	if err != nil {
		return MemoryTotals{}
	}
	return meminfoTotals(m)
}

var hostMeminfo cachedSample[Meminfo]

// ReadMeminfo shares complete memory observations across collectors and checks.
// maxAge is enforced per caller; failed or incomplete reads are never cached.
func ReadMeminfo(maxAge time.Duration) (Meminfo, error) {
	var sample Meminfo
	err := hostMeminfo.readInto(time.Now(), maxAge, func() (Meminfo, bool, error) {
		data, err := hostfs.ReadFile(procPath(procFileMeminfo))
		if err != nil {
			return Meminfo{}, false, fmt.Errorf("read meminfo: %w", err)
		}
		m := ParseMeminfo(data)
		totals := meminfoTotals(m)
		return m, totals.MemoryOK && totals.SwapOK, nil
	}, &sample)
	return sample, err
}

// Meminfo is the /proc/meminfo subset Sermo reads, in bytes. Each Have* field
// reports whether the kernel published that line: a missing or malformed field
// leaves its value zero, which a caller must not mistake for a real reading.
type Meminfo struct {
	MemTotal     uint64
	MemAvailable uint64
	SwapTotal    uint64
	SwapFree     uint64

	HaveMemTotal     bool
	HaveMemAvailable bool
	HaveSwapTotal    bool
	HaveSwapFree     bool
}

// ParseMeminfo extracts the MemTotal, MemAvailable, SwapTotal and SwapFree
// values from raw /proc/meminfo content. It is the single scanner shared by the
// metrics collector (which derives used = total - available) and the checks
// package (which needs the raw MemAvailable/SwapFree readings).
func ParseMeminfo(data []byte) Meminfo {
	var m Meminfo
	for line := range strings.SplitSeq(string(data), procLineSeparator) {
		switch {
		case strings.HasPrefix(line, procMeminfoMemTotalPrefix):
			m.MemTotal, m.HaveMemTotal = parseMeminfoKB(line)
		case strings.HasPrefix(line, procMeminfoMemAvailablePrefix):
			m.MemAvailable, m.HaveMemAvailable = parseMeminfoKB(line)
		case strings.HasPrefix(line, procMeminfoSwapTotalPrefix):
			m.SwapTotal, m.HaveSwapTotal = parseMeminfoKB(line)
		case strings.HasPrefix(line, procMeminfoSwapFreePrefix):
			m.SwapFree, m.HaveSwapFree = parseMeminfoKB(line)
		}
	}
	return m
}

func meminfoTotals(m Meminfo) MemoryTotals {
	var totals MemoryTotals
	totals.MemoryTotal, totals.MemoryOK = m.MemTotal, m.HaveMemTotal
	totals.SwapTotal, totals.SwapOK = m.SwapTotal, m.HaveSwapTotal
	if !totals.MemoryOK || !m.HaveMemAvailable || totals.MemoryTotal == 0 || totals.MemoryTotal < m.MemAvailable {
		totals.MemoryOK = false
		totals.MemoryTotal = 0
	} else {
		totals.MemoryUsed = totals.MemoryTotal - m.MemAvailable
	}
	if !totals.SwapOK || !m.HaveSwapFree || totals.SwapTotal < m.SwapFree {
		totals.SwapOK = false
		totals.SwapTotal = 0
	} else {
		totals.SwapUsed = totals.SwapTotal - m.SwapFree
	}
	return totals
}

// SystemCPU reads the aggregate cpu line of /proc/stat. busy excludes idle and
// iowait; total is the sum of all fields.
func (OSReader) SystemCPU() (busy, total uint64, ok bool) {
	data, err := hostfs.ReadFile(procPath(procFileStat))
	if err != nil {
		return 0, 0, false
	}
	line := data
	if before, _, ok := bytes.Cut(data, []byte{'\n'}); ok {
		line = before
	}
	fields := strings.Fields(string(line))
	if len(fields) < procStatAggregateMinFields || fields[procStatCPULabelIndex] != procStatCPUPrefix {
		return 0, 0, false
	}
	var sum, idle uint64
	for i, f := range fields[procStatCPUValuesStartIndex:] {
		v, err := strconv.ParseUint(f, procDecimalBase, procUintBits)
		if err != nil {
			continue
		}
		sum += v
		if i == procStatIdleValueOffset || i == procStatIOWaitValueOffset {
			idle += v
		}
	}
	return sum - idle, sum, true
}

// LoadAverages reads the first three fields of /proc/loadavg.
func (OSReader) LoadAverages() (l1, l5, l15 float64, ok bool) {
	data, err := hostfs.ReadFile(procPath(procFileLoadavg))
	if err != nil {
		return 0, 0, 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) < procLoadAvgMinFields {
		return 0, 0, 0, false
	}
	l1, e1 := strconv.ParseFloat(fields[procLoadAvg1Index], procFloatBits)
	l5, e5 := strconv.ParseFloat(fields[procLoadAvg5Index], procFloatBits)
	l15, e15 := strconv.ParseFloat(fields[procLoadAvg15Index], procFloatBits)
	if e1 != nil || e5 != nil || e15 != nil {
		return 0, 0, 0, false
	}
	return l1, l5, l15, true
}

// NumCPU returns the number of logical CPUs (hardware threads) on the host. It
// counts the per-CPU "cpuN" lines in /proc/stat so the count reflects the whole
// server, not this process's CPU affinity: runtime.NumCPU() honours the affinity
// mask and would undercount when Sermo is pinned (taskset/cpuset/systemd
// CPUAffinity/container limits), which would inflate the service CPU%. Falls back
// to runtime.NumCPU() when /proc/stat is unavailable.
func (OSReader) NumCPU() int {
	if n := procStatCPUCount(); n > 0 {
		return n
	}
	return runtime.NumCPU()
}

// procStatCPUCount counts the per-CPU "cpuN" lines in /proc/stat. Returns 0 when
// /proc/stat cannot be read.
func procStatCPUCount() int { return procStatSample().numCPU }

// countCPULines counts the per-CPU "cpuN" lines in /proc/stat content (the
// aggregate "cpu" line, which has no digit after the prefix, is excluded).
func countCPULines(data []byte) int {
	n := 0
	for line := range strings.SplitSeq(string(data), procLineSeparator) {
		if len(line) > len(procStatCPUPrefix) && strings.HasPrefix(line, procStatCPUPrefix) && line[len(procStatCPUPrefix)] >= '0' && line[len(procStatCPUPrefix)] <= '9' {
			n++
		}
	}
	return n
}

// ClockTicks returns the kernel USER_HZ.
func (OSReader) ClockTicks() float64 { return LinuxClockTicks }

func parseMeminfoKB(line string) (uint64, bool) {
	fields := strings.Fields(line)
	if len(fields) <= meminfoValueIndex {
		return 0, false
	}
	kb, err := strconv.ParseUint(fields[meminfoValueIndex], procDecimalBase, procUintBits)
	if err != nil {
		return 0, false
	}
	return kb * units.BytesPerKiB, true
}
