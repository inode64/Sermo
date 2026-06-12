package checks

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// DiskIOSample is one observation of a block device's /proc/diskstats counters
// (cumulative since boot; sectors are always 512 bytes there).
type DiskIOSample struct {
	ReadsCompleted  uint64
	SectorsRead     uint64
	ReadTicksMs     uint64
	WritesCompleted uint64
	SectorsWritten  uint64
	WriteTicksMs    uint64
	IOTicksMs       uint64
}

// DiskIOSamplerFunc reads the current counters for a block device (e.g. "sda",
// "nvme0n1"). Injected for tests; the default reads /proc/diskstats.
type DiskIOSamplerFunc func(device string) (DiskIOSample, error)

// diskIOState carries the previous counters and their timestamp across cycles.
type diskIOState struct {
	primed bool
	t      time.Time
	last   DiskIOSample
}

// diskIOCheck watches a block device's I/O rates, computed from per-cycle
// /proc/diskstats deltas: utilization (share of wall time the device was busy),
// read/write throughput and average request latency. Like swap io it is
// stateful (the first cycle only baselines) and a pointer type; a watch ticks
// sequentially on its own goroutine, so the state needs no locking. OK==true
// means every predicate holds (the alert condition).
type diskIOCheck struct {
	base
	device  string
	preds   []levelPred
	sampler DiskIOSamplerFunc
	clock   func() time.Time
	state   *diskIOState
}

func (c *diskIOCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultDiskIOSampler
	}
	clock := c.clock
	if clock == nil {
		clock = time.Now
	}

	s, err := sampler(c.device)
	if err != nil {
		return c.result(false, "diskio "+c.device+": "+err.Error(), start)
	}
	now := clock()
	st := c.state
	elapsed := now.Sub(st.t)
	if !st.primed || elapsed <= 0 {
		st.primed, st.t, st.last = true, now, s
		return c.result(false, fmt.Sprintf("diskio %s baseline", c.device), start)
	}

	ioTicks := deltaOrZero(s.IOTicksMs, st.last.IOTicksMs)
	utilPct := min(100, float64(ioTicks)/float64(elapsed.Milliseconds())*100)
	readBytes := float64(deltaOrZero(s.SectorsRead, st.last.SectorsRead)*512) / elapsed.Seconds()
	writeBytes := float64(deltaOrZero(s.SectorsWritten, st.last.SectorsWritten)*512) / elapsed.Seconds()
	ops := deltaOrZero(s.ReadsCompleted, st.last.ReadsCompleted) + deltaOrZero(s.WritesCompleted, st.last.WritesCompleted)
	awaitMs := 0.0
	if ops > 0 {
		awaitMs = float64(deltaOrZero(s.ReadTicksMs, st.last.ReadTicksMs)+deltaOrZero(s.WriteTicksMs, st.last.WriteTicksMs)) / float64(ops)
	}
	st.t, st.last = now, s

	values := map[string]float64{
		"util_pct":    utilPct,
		"read_bytes":  readBytes,
		"write_bytes": writeBytes,
		"await_ms":    awaitMs,
	}

	ok := levelPredsHold(c.preds, values)

	res := c.result(ok, fmt.Sprintf("diskio %s util %.1f%% read %.0fB/s write %.0fB/s await %.1fms",
		c.device, utilPct, readBytes, writeBytes, awaitMs), start)
	res.Data = map[string]any{
		"device":      c.device,
		"util_pct":    utilPct,
		"read_bytes":  readBytes,
		"write_bytes": writeBytes,
		"await_ms":    awaitMs,
	}
	res.Data["value"] = firstPredValue(c.preds, values, utilPct)
	return res
}

// defaultDiskIOSampler finds device in /proc/diskstats. Field order after the
// device name: reads, reads-merged, sectors-read, ms-reading, writes,
// writes-merged, sectors-written, ms-writing, in-flight, io-ticks-ms, ….
func defaultDiskIOSampler(device string) (DiskIOSample, error) {
	data, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return DiskIOSample{}, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 13 || fields[2] != device {
			continue
		}
		u := func(i int) uint64 {
			n, _ := strconv.ParseUint(fields[i], 10, 64)
			return n
		}
		return DiskIOSample{
			ReadsCompleted:  u(3),
			SectorsRead:     u(5),
			ReadTicksMs:     u(6),
			WritesCompleted: u(7),
			SectorsWritten:  u(9),
			WriteTicksMs:    u(10),
			IOTicksMs:       u(12),
		}, nil
	}
	return DiskIOSample{}, fmt.Errorf("device %q not in /proc/diskstats", device)
}
