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

// NumCPU returns the number of logical CPUs.
func (OSReader) NumCPU() int { return runtime.NumCPU() }

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
