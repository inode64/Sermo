package metrics

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// clockTicks is the conventional kernel USER_HZ on Linux. The Go runtime does
// not expose sysconf(SC_CLK_TCK); 100 is correct on virtually all Linux builds.
const clockTicks = 100.0

// pageSize is used to convert statm resident pages to bytes.
var pageSize = uint64(os.Getpagesize())

// OSReader reads metrics from the host /proc filesystem.
type OSReader struct{}

// ProcessCPU sums utime (field 14) and stime (field 15) of /proc/<pid>/stat.
func (OSReader) ProcessCPU(pid int) (uint64, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
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
	if len(fields) <= 12 {
		return 0, false
	}
	utime, err1 := strconv.ParseUint(fields[11], 10, 64)
	stime, err2 := strconv.ParseUint(fields[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return utime + stime, true
}

// ProcessRSS reads resident pages (field 2 of /proc/<pid>/statm) as bytes.
func (OSReader) ProcessRSS(pid int) (uint64, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/statm")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
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
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmSwap:") {
			return parseMeminfoKB(line)
		}
	}
	return 0, true // no VmSwap line -> nothing swapped
}

// ProcessIO reads read_bytes and write_bytes (actual block-layer I/O) from
// /proc/<pid>/io. Reading another user's io requires privilege, so ok is false
// when the file cannot be read.
func (OSReader) ProcessIO(pid int) (read, write uint64, ok bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/io")
	if err != nil {
		return 0, 0, false
	}
	var haveR, haveW bool
	for _, line := range strings.Split(string(data), "\n") {
		if v, found := strings.CutPrefix(line, "read_bytes:"); found {
			if n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64); err == nil {
				read, haveR = n, true
			}
		} else if v, found := strings.CutPrefix(line, "write_bytes:"); found {
			if n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64); err == nil {
				write, haveW = n, true
			}
		}
	}
	return read, write, haveR && haveW
}

// ProcessFDs counts the entries in /proc/<pid>/fd (open file descriptors).
// Reading another user's fd dir requires privilege, so ok is false when it
// cannot be read.
func (OSReader) ProcessFDs(pid int) (uint64, bool) {
	entries, err := os.ReadDir("/proc/" + strconv.Itoa(pid) + "/fd")
	if err != nil {
		return 0, false
	}
	return uint64(len(entries)), true
}

// ProcessThreads counts the entries in /proc/<pid>/task (the process's threads).
func (OSReader) ProcessThreads(pid int) (uint64, bool) {
	entries, err := os.ReadDir("/proc/" + strconv.Itoa(pid) + "/task")
	if err != nil {
		return 0, false
	}
	return uint64(len(entries)), true
}

// TotalMemory reads MemTotal and MemAvailable from /proc/meminfo.
func (OSReader) TotalMemory() (total, used uint64, ok bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	var memTotal, memAvail uint64
	var haveTotal, haveAvail bool
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			memTotal, haveTotal = parseMeminfoKB(line)
		case strings.HasPrefix(line, "MemAvailable:"):
			memAvail, haveAvail = parseMeminfoKB(line)
		}
	}
	if !haveTotal || !haveAvail || memTotal < memAvail {
		return 0, 0, false
	}
	return memTotal, memTotal - memAvail, true
}

// TotalSwap reads SwapTotal and SwapFree from /proc/meminfo. used = total - free.
func (OSReader) TotalSwap() (total, used uint64, ok bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	var swapTotal, swapFree uint64
	var haveTotal, haveFree bool
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "SwapTotal:"):
			swapTotal, haveTotal = parseMeminfoKB(line)
		case strings.HasPrefix(line, "SwapFree:"):
			swapFree, haveFree = parseMeminfoKB(line)
		}
	}
	if !haveTotal || !haveFree || swapTotal < swapFree {
		return 0, 0, false
	}
	return swapTotal, swapTotal - swapFree, true
}

// SystemCPU reads the aggregate cpu line of /proc/stat. busy excludes idle and
// iowait; total is the sum of all fields.
func (OSReader) SystemCPU() (busy, total uint64, ok bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	line := data
	if i := strings.IndexByte(string(data), '\n'); i >= 0 {
		line = data[:i]
	}
	fields := strings.Fields(string(line))
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	var sum, idle uint64
	for i, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			continue
		}
		sum += v
		if i == 3 || i == 4 { // idle, iowait
			idle += v
		}
	}
	return sum - idle, sum, true
}

// LoadAverages reads the first three fields of /proc/loadavg.
func (OSReader) LoadAverages() (l1, l5, l15 float64, ok bool) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, 0, false
	}
	l1, e1 := strconv.ParseFloat(fields[0], 64)
	l5, e5 := strconv.ParseFloat(fields[1], 64)
	l15, e15 := strconv.ParseFloat(fields[2], 64)
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
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	return countCPULines(data)
}

// countCPULines counts the per-CPU "cpuN" lines in /proc/stat content (the
// aggregate "cpu" line, which has no digit after the prefix, is excluded).
func countCPULines(data []byte) int {
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if len(line) > 3 && strings.HasPrefix(line, "cpu") && line[3] >= '0' && line[3] <= '9' {
			n++
		}
	}
	return n
}

// ClockTicks returns the kernel USER_HZ.
func (OSReader) ClockTicks() float64 { return clockTicks }

func parseMeminfoKB(line string) (uint64, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0, false
	}
	kb, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return kb * 1024, true
}
