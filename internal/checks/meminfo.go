package checks

import (
	"os"
	"sermo/internal/metrics"
)

type meminfoSample struct {
	memoryTotalBytes     uint64
	memoryAvailableBytes uint64
	swapTotalBytes       uint64
	swapFreeBytes        uint64
}

func readMeminfo() (meminfoSample, error) {
	data, err := os.ReadFile(procMeminfoPath)
	if err != nil {
		return meminfoSample{}, err
	}
	return parseMeminfo(string(data)), nil
}

// parseMeminfo adapts the shared metrics scanner to the raw sample the checks
// package needs (MemTotal/MemAvailable/SwapTotal/SwapFree in bytes); a missing
// field stays zero.
func parseMeminfo(data string) meminfoSample {
	m := metrics.ParseMeminfo([]byte(data))
	return meminfoSample{
		memoryTotalBytes:     m.MemTotal,
		memoryAvailableBytes: m.MemAvailable,
		swapTotalBytes:       m.SwapTotal,
		swapFreeBytes:        m.SwapFree,
	}
}
