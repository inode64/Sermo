package checks

import (
	"fmt"
	"sermo/internal/metrics"
)

func readMeminfo() (metrics.Meminfo, error) {
	sample, err := metrics.ReadMeminfo(metrics.DefaultSystemFreshness)
	if err != nil {
		return metrics.Meminfo{}, fmt.Errorf("read %s: %w", procMeminfoPath, err)
	}
	return sample, nil
}
