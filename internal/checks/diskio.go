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

// DiskIORates is one delta-derived view of a block device's I/O activity.
type DiskIORates struct {
	UtilPct    float64
	ReadBytes  float64
	WriteBytes float64
	AwaitMs    float64
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

	rates, _ := CalculateDiskIORates(st.last, s, elapsed)
	st.t, st.last = now, s

	values := map[string]float64{
		"util_pct":    rates.UtilPct,
		"read_bytes":  rates.ReadBytes,
		"write_bytes": rates.WriteBytes,
		"await_ms":    rates.AwaitMs,
	}

	ok := levelPredsHold(c.preds, values)

	res := c.result(ok, fmt.Sprintf("diskio %s util %.1f%% read %.0fB/s write %.0fB/s await %.1fms",
		c.device, rates.UtilPct, rates.ReadBytes, rates.WriteBytes, rates.AwaitMs), start)
	res.Data = map[string]any{
		"device":      c.device,
		"util_pct":    rates.UtilPct,
		"read_bytes":  rates.ReadBytes,
		"write_bytes": rates.WriteBytes,
		"await_ms":    rates.AwaitMs,
	}
	res.Data["value"] = firstPredValue(c.preds, values, rates.UtilPct)
	return res
}

// CalculateDiskIORates derives the same per-second rates used by the diskio
// check from two cumulative /proc/diskstats samples.
func CalculateDiskIORates(prev, cur DiskIOSample, elapsed time.Duration) (DiskIORates, bool) {
	elapsedMs := elapsed.Milliseconds()
	if elapsed <= 0 || elapsedMs <= 0 {
		return DiskIORates{}, false
	}
	ioTicks := deltaOrZero(cur.IOTicksMs, prev.IOTicksMs)
	rates := DiskIORates{
		UtilPct:    min(100, float64(ioTicks)/float64(elapsedMs)*100),
		ReadBytes:  float64(deltaOrZero(cur.SectorsRead, prev.SectorsRead)*512) / elapsed.Seconds(),
		WriteBytes: float64(deltaOrZero(cur.SectorsWritten, prev.SectorsWritten)*512) / elapsed.Seconds(),
	}
	ops := deltaOrZero(cur.ReadsCompleted, prev.ReadsCompleted) + deltaOrZero(cur.WritesCompleted, prev.WritesCompleted)
	if ops > 0 {
		rates.AwaitMs = float64(deltaOrZero(cur.ReadTicksMs, prev.ReadTicksMs)+deltaOrZero(cur.WriteTicksMs, prev.WriteTicksMs)) / float64(ops)
	}
	return rates, true
}

// SampleDiskIO returns one live block-device counter observation using the
// default /proc/diskstats sampler.
func SampleDiskIO(device string) (DiskIOSample, error) { return defaultDiskIOSampler(device) }

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
