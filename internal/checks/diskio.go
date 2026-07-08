package checks

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	diskIOSectorBytes = 512

	diskStatsMinFields            = 13
	diskStatsDeviceFieldIndex     = 2
	diskStatsReadsCompletedIndex  = 3
	diskStatsSectorsReadIndex     = 5
	diskStatsReadTicksMsIndex     = 6
	diskStatsWritesCompletedIndex = 7
	diskStatsSectorsWrittenIndex  = 9
	diskStatsWriteTicksMsIndex    = 10
	diskStatsIOTicksMsIndex       = 12
	diskStatsReadsCompletedField  = "reads_completed"
	diskStatsSectorsReadField     = "sectors_read"
	diskStatsReadTicksMsField     = "read_ticks_ms"
	diskStatsWritesCompletedField = "writes_completed"
	diskStatsSectorsWrittenField  = "sectors_written"
	diskStatsWriteTicksMsField    = "write_ticks_ms"
	diskStatsIOTicksMsField       = "io_ticks_ms"
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

// diskIOCheck is a stateful level check for per-cycle /proc/diskstats deltas.
// The first cycle only baselines; one watch ticks sequentially, so no lock.
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
		fieldUtilPct:    rates.UtilPct,
		fieldReadBytes:  rates.ReadBytes,
		fieldWriteBytes: rates.WriteBytes,
		fieldAwaitMs:    rates.AwaitMs,
	}

	ok := levelPredsHold(c.preds, values)

	res := c.result(ok, fmt.Sprintf("diskio %s util %.1f%% read %.0fB/s write %.0fB/s await %.1fms",
		c.device, rates.UtilPct, rates.ReadBytes, rates.WriteBytes, rates.AwaitMs), start)
	res.Data = map[string]any{
		DataKeyDevice:   c.device,
		fieldUtilPct:    rates.UtilPct,
		fieldReadBytes:  rates.ReadBytes,
		fieldWriteBytes: rates.WriteBytes,
		fieldAwaitMs:    rates.AwaitMs,
	}
	res.Data[DataKeyValue] = firstPredValue(c.preds, values, rates.UtilPct)
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
		UtilPct:    min(percentScale, float64(ioTicks)/float64(elapsedMs)*percentScale),
		ReadBytes:  float64(deltaOrZero(cur.SectorsRead, prev.SectorsRead)*diskIOSectorBytes) / elapsed.Seconds(),
		WriteBytes: float64(deltaOrZero(cur.SectorsWritten, prev.SectorsWritten)*diskIOSectorBytes) / elapsed.Seconds(),
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
	data, err := os.ReadFile(procDiskstatsPath)
	if err != nil {
		return DiskIOSample{}, err
	}
	for _, line := range strings.Split(string(data), checkLineSeparator) {
		fields := strings.Fields(line)
		if len(fields) < diskStatsMinFields || fields[diskStatsDeviceFieldIndex] != device {
			continue
		}
		sample, err := parseDiskIOSample(fields)
		if err != nil {
			return DiskIOSample{}, fmt.Errorf("device %q: %w", device, err)
		}
		return sample, nil
	}
	return DiskIOSample{}, fmt.Errorf("device %q not in /proc/diskstats", device)
}

func parseDiskIOSample(fields []string) (DiskIOSample, error) {
	if len(fields) < diskStatsMinFields {
		return DiskIOSample{}, fmt.Errorf("diskstats line has %d fields, want at least %d", len(fields), diskStatsMinFields)
	}
	readsCompleted, err := diskIOUint(fields, diskStatsReadsCompletedIndex, diskStatsReadsCompletedField)
	if err != nil {
		return DiskIOSample{}, err
	}
	sectorsRead, err := diskIOUint(fields, diskStatsSectorsReadIndex, diskStatsSectorsReadField)
	if err != nil {
		return DiskIOSample{}, err
	}
	readTicksMs, err := diskIOUint(fields, diskStatsReadTicksMsIndex, diskStatsReadTicksMsField)
	if err != nil {
		return DiskIOSample{}, err
	}
	writesCompleted, err := diskIOUint(fields, diskStatsWritesCompletedIndex, diskStatsWritesCompletedField)
	if err != nil {
		return DiskIOSample{}, err
	}
	sectorsWritten, err := diskIOUint(fields, diskStatsSectorsWrittenIndex, diskStatsSectorsWrittenField)
	if err != nil {
		return DiskIOSample{}, err
	}
	writeTicksMs, err := diskIOUint(fields, diskStatsWriteTicksMsIndex, diskStatsWriteTicksMsField)
	if err != nil {
		return DiskIOSample{}, err
	}
	ioTicksMs, err := diskIOUint(fields, diskStatsIOTicksMsIndex, diskStatsIOTicksMsField)
	if err != nil {
		return DiskIOSample{}, err
	}
	return DiskIOSample{
		ReadsCompleted:  readsCompleted,
		SectorsRead:     sectorsRead,
		ReadTicksMs:     readTicksMs,
		WritesCompleted: writesCompleted,
		SectorsWritten:  sectorsWritten,
		WriteTicksMs:    writeTicksMs,
		IOTicksMs:       ioTicksMs,
	}, nil
}

func diskIOUint(fields []string, index int, name string) (uint64, error) {
	n, err := strconv.ParseUint(fields[index], numericBaseDecimal, numericBits64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return n, nil
}
