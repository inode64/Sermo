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
	memTotal, memAvailable, swapTotal, swapFree, _, _, _, _ := metrics.ParseMeminfo([]byte(data))
	return meminfoSample{
		memoryTotalBytes:     memTotal,
		memoryAvailableBytes: memAvailable,
		swapTotalBytes:       swapTotal,
		swapFreeBytes:        swapFree,
	}
}
