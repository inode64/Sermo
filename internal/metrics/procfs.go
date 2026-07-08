package metrics

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"sermo/internal/process"
)

// LinuxClockTicks is the conventional kernel USER_HZ on Linux. The Go runtime does
// not expose sysconf(SC_CLK_TCK); 100 is correct on virtually all Linux builds.
const LinuxClockTicks = 100.0

// pageSize is used to convert statm resident pages to bytes.
var pageSize = uint64(os.Getpagesize())

const (
	procRoot          = "/proc"
	procLineSeparator = "\n"
	bytesPerKiB       = 1024
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
	procStatStartTimeIndex        = 19
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
	procIntBits                   = 64
	procFloatBits                 = 64
)

func procPath(name string) string {
	return filepath.Join(procRoot, name)
}

// OSReader reads metrics from the host /proc filesystem.
type OSReader struct{}

// ProcessCPU sums utime (field 14) and stime (field 15) of /proc/<pid>/stat.
func (OSReader) ProcessCPU(pid int) (uint64, bool) {
	data, err := os.ReadFile(process.PIDPath(pid, process.ProcFileStat))
	if err != nil {
		return 0, false
	}
	stat := string(data)
	closeParen := strings.LastIndex(stat, ")")
	if closeParen < 0 {
		return 0, false
	}
	// After ')', tokens begin at field 3 (state); utime is field 14 (index 11),
	// stime field 15 (index 12).
	fields := strings.Fields(stat[closeParen+1:])
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
func (OSReader) ProcessStartTime(pid int) (time.Time, bool) {
	data, err := os.ReadFile(process.PIDPath(pid, process.ProcFileStat))
	if err != nil {
		return time.Time{}, false
	}
	startTicks, ok := parseProcStartTicks(string(data))
	if !ok {
		return time.Time{}, false
	}
	boot, ok := procBootTime()
	if !ok {
		return time.Time{}, false
	}
	startSeconds := float64(startTicks) / LinuxClockTicks
	whole := int64(startSeconds)
	nsec := int64((startSeconds - float64(whole)) * float64(time.Second))
	return time.Unix(boot+whole, nsec), true
}

func parseProcStartTicks(stat string) (uint64, bool) {
	closeParen := strings.LastIndex(stat, ")")
	if closeParen < 0 {
		return 0, false
	}
	// After ')', tokens begin at field 3 (state); starttime is field 22, so
	// index 19 in this slice.
	fields := strings.Fields(stat[closeParen+1:])
	if len(fields) <= procStatStartTimeIndex {
		return 0, false
	}
	start, err := strconv.ParseUint(fields[procStatStartTimeIndex], procDecimalBase, procUintBits)
	return start, err == nil
}

func procBootTime() (int64, bool) {
	data, err := os.ReadFile(procPath(procFileStat))
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), procLineSeparator) {
		if v, ok := strings.CutPrefix(line, procStatBootTimePrefix); ok {
			sec, err := strconv.ParseInt(strings.TrimSpace(v), procDecimalBase, procIntBits)
			return sec, err == nil
		}
	}
	return 0, false
}

// ProcessRSS reads resident pages (field 2 of /proc/<pid>/statm) as bytes.
func (OSReader) ProcessRSS(pid int) (uint64, bool) {
	data, err := os.ReadFile(process.PIDPath(pid, process.ProcFileStatm))
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
	data, err := os.ReadFile(process.PIDPath(pid, process.ProcFileStatus))
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), procLineSeparator) {
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
	data, err := os.ReadFile(process.PIDPath(pid, process.ProcFileIO))
	if err != nil {
		return 0, 0, false
	}
	return parseProcIO(string(data))
}

func parseProcIO(data string) (read, write uint64, ok bool) {
	var haveR, haveW bool
	for _, line := range strings.Split(data, procLineSeparator) {
		if v, found := strings.CutPrefix(line, procIOReadBytesPrefix); found {
			if n, err := strconv.ParseUint(strings.TrimSpace(v), procDecimalBase, procUintBits); err == nil {
				read, haveR = n, true
			} else {
				return 0, 0, false
			}
		} else if v, found := strings.CutPrefix(line, procIOWriteBytesPrefix); found {
			if n, err := strconv.ParseUint(strings.TrimSpace(v), procDecimalBase, procUintBits); err == nil {
				write, haveW = n, true
			} else {
				return 0, 0, false
			}
		}
	}
	if !haveR || !haveW {
		return 0, 0, false
	}
	return read, write, true
}

// ProcessFDs counts the entries in /proc/<pid>/fd (open file descriptors).
// Reading another user's fd dir requires privilege, so ok is false when it
// cannot be read.
func (OSReader) ProcessFDs(pid int) (uint64, bool) {
	entries, err := os.ReadDir(process.PIDPath(pid, process.ProcFileFD))
	if err != nil {
		return 0, false
	}
	return uint64(len(entries)), true
}

// ProcessThreads counts the entries in /proc/<pid>/task (the process's threads).
func (OSReader) ProcessThreads(pid int) (uint64, bool) {
	entries, err := os.ReadDir(process.PIDPath(pid, process.ProcFileTask))
	if err != nil {
		return 0, false
	}
	return uint64(len(entries)), true
}

// TotalMemory reads MemTotal and MemAvailable from /proc/meminfo.
func (OSReader) TotalMemory() (total, used uint64, ok bool) {
	totals := readProcMeminfoTotals()
	if !totals.memoryOK {
		return 0, 0, false
	}
	return totals.memoryTotal, totals.memoryUsed, true
}

// TotalSwap reads SwapTotal and SwapFree from /proc/meminfo. used = total - free.
func (OSReader) TotalSwap() (total, used uint64, ok bool) {
	totals := readProcMeminfoTotals()
	if !totals.swapOK {
		return 0, 0, false
	}
	return totals.swapTotal, totals.swapUsed, true
}

// TotalMemoryAndSwap reads memory and swap totals from /proc/meminfo with one
// file read. The collector uses it when available so system and service metric
// sampling do not reread meminfo for memory and swap separately.
func (OSReader) TotalMemoryAndSwap() (memoryTotal, memoryUsed, swapTotal, swapUsed uint64, memoryOK, swapOK bool) {
	totals := readProcMeminfoTotals()
	return totals.memoryTotal, totals.memoryUsed, totals.swapTotal, totals.swapUsed, totals.memoryOK, totals.swapOK
}

type procMeminfoTotals struct {
	memoryTotal uint64
	memoryUsed  uint64
	memoryOK    bool
	swapTotal   uint64
	swapUsed    uint64
	swapOK      bool
}

func readProcMeminfoTotals() procMeminfoTotals {
	data, err := os.ReadFile(procPath(procFileMeminfo))
	if err != nil {
		return procMeminfoTotals{}
	}
	return parseProcMeminfoTotals(data)
}

func parseProcMeminfoTotals(data []byte) procMeminfoTotals {
	var totals procMeminfoTotals
	var memoryAvailable, swapFree uint64
	var haveMemoryAvailable, haveSwapFree bool
	for _, line := range strings.Split(string(data), procLineSeparator) {
		switch {
		case strings.HasPrefix(line, procMeminfoMemTotalPrefix):
			totals.memoryTotal, totals.memoryOK = parseMeminfoKB(line)
		case strings.HasPrefix(line, procMeminfoMemAvailablePrefix):
			memoryAvailable, haveMemoryAvailable = parseMeminfoKB(line)
		case strings.HasPrefix(line, procMeminfoSwapTotalPrefix):
			totals.swapTotal, totals.swapOK = parseMeminfoKB(line)
		case strings.HasPrefix(line, procMeminfoSwapFreePrefix):
			swapFree, haveSwapFree = parseMeminfoKB(line)
		}
	}
	if !totals.memoryOK || !haveMemoryAvailable || totals.memoryTotal < memoryAvailable {
		totals.memoryOK = false
		totals.memoryTotal = 0
	} else {
		totals.memoryUsed = totals.memoryTotal - memoryAvailable
	}
	if !totals.swapOK || !haveSwapFree || totals.swapTotal < swapFree {
		totals.swapOK = false
		totals.swapTotal = 0
	} else {
		totals.swapUsed = totals.swapTotal - swapFree
	}
	return totals
}

// SystemCPU reads the aggregate cpu line of /proc/stat. busy excludes idle and
// iowait; total is the sum of all fields.
func (OSReader) SystemCPU() (busy, total uint64, ok bool) {
	data, err := os.ReadFile(procPath(procFileStat))
	if err != nil {
		return 0, 0, false
	}
	line := data
	if i := strings.IndexByte(string(data), '\n'); i >= 0 {
		line = data[:i]
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
	data, err := os.ReadFile(procPath(procFileLoadavg))
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
func procStatCPUCount() int {
	data, err := os.ReadFile(procPath(procFileStat))
	if err != nil {
		return 0
	}
	return countCPULines(data)
}

// countCPULines counts the per-CPU "cpuN" lines in /proc/stat content (the
// aggregate "cpu" line, which has no digit after the prefix, is excluded).
func countCPULines(data []byte) int {
	n := 0
	for _, line := range strings.Split(string(data), procLineSeparator) {
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
	return kb * bytesPerKiB, true
}
