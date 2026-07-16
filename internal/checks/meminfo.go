package checks

import (
	"os"
	"sermo/internal/metrics"
	"strings"
)

const (
	meminfoMemTotalPrefix     = "MemTotal:"
	meminfoMemAvailablePrefix = "MemAvailable:"
	meminfoSwapTotalPrefix    = "SwapTotal:"
	meminfoSwapFreePrefix     = "SwapFree:"
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

func parseMeminfo(data string) meminfoSample {
	var sample meminfoSample
	for line := range strings.SplitSeq(data, checkLineSeparator) {
		switch {
		case strings.HasPrefix(line, meminfoMemTotalPrefix):
			sample.memoryTotalBytes, _ = metrics.MeminfoKB(strings.TrimPrefix(line, meminfoMemTotalPrefix))
		case strings.HasPrefix(line, meminfoMemAvailablePrefix):
			sample.memoryAvailableBytes, _ = metrics.MeminfoKB(strings.TrimPrefix(line, meminfoMemAvailablePrefix))
		case strings.HasPrefix(line, meminfoSwapTotalPrefix):
			sample.swapTotalBytes, _ = metrics.MeminfoKB(strings.TrimPrefix(line, meminfoSwapTotalPrefix))
		case strings.HasPrefix(line, meminfoSwapFreePrefix):
			sample.swapFreeBytes, _ = metrics.MeminfoKB(strings.TrimPrefix(line, meminfoSwapFreePrefix))
		}
	}
	return sample
}
