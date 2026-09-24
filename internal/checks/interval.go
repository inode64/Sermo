package checks

import (
	"fmt"
	"math"
	"time"
)

// ResolveInterval rounds a check interval to worker cycles, with a minimum of
// one. Scheduling, snapshot freshness and diagnostics share this policy. An
// absent interval or invalid resolution uses one cycle without a warning.
func ResolveInterval(interval, resolution time.Duration) (int, string) {
	if interval <= 0 || resolution <= 0 {
		return 1, ""
	}
	n := int(math.Round(float64(interval) / float64(resolution)))
	switch {
	case n < 1:
		return 1, fmt.Sprintf("interval %s is below the %s resolution; running every cycle", interval, resolution)
	case time.Duration(n)*resolution != interval:
		return n, fmt.Sprintf("interval %s is not a multiple of the %s resolution; running every %s", interval, resolution, time.Duration(n)*resolution)
	default:
		return n, ""
	}
}
