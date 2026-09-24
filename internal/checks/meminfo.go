package checks

import (
	"fmt"
	"sermo/internal/hostfs"
	"sermo/internal/metrics"
)

func readMeminfo() (metrics.Meminfo, error) {
	data, err := hostfs.ReadFile(procMeminfoPath)
	if err != nil {
		return metrics.Meminfo{}, fmt.Errorf("read %s: %w", procMeminfoPath, err)
	}
	return metrics.ParseMeminfo(data), nil
}
