package checks

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/dustin/go-humanize"
)

// SizeSamplerFunc returns the byte size of a file or directory. Injected for
// tests; the default uses os.Stat for a file and a recursive walk for a
// directory.
type SizeSamplerFunc func(path string) (int64, error)

// sizeSample is one timestamped size observation.
type sizeSample struct {
	t    time.Time
	size int64
}

// sizeState keeps the recent size samples within the configured window. Being a
// pointer, it survives across cycles when the check is built once (a host watch).
type sizeState struct {
	samples []sizeSample
}

// sizeCheck alerts when a file or directory grows by at least growBy within the
// window. It is condition-style (OK==true means "grew too fast"): only increases
// trip it — a steady or shrinking path passes. It is stateful, so it is meant to
// run as a host watch where the same check instance ticks each cycle.
type sizeCheck struct {
	base
	path    string
	growBy  int64
	window  time.Duration
	sampler SizeSamplerFunc
	clock   func() time.Time
	state   *sizeState
}

func (c *sizeCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = dirOrFileSize
	}
	clock := c.clock
	if clock == nil {
		clock = time.Now
	}

	size, err := sampler(c.path)
	if err != nil {
		return c.result(false, fmt.Sprintf("size %s: %v", c.path, err), start)
	}
	now := clock()

	// Drop samples older than the window, then record the current one.
	cutoff := now.Add(-c.window)
	kept := c.state.samples[:0]
	for _, s := range c.state.samples {
		if !s.t.Before(cutoff) {
			kept = append(kept, s)
		}
	}
	c.state.samples = append(kept, sizeSample{t: now, size: size})

	baseline := c.state.samples[0]
	growth := size - baseline.size
	ok := growth >= c.growBy // only increases trip the check

	span := now.Sub(baseline.t)
	msg := fmt.Sprintf("%s grew %s in %s (limit %s/%s)",
		c.path, humanizeSigned(growth), span.Round(time.Second),
		humanize.Bytes(uint64(c.growBy)), c.window)
	res := c.result(ok, msg, start)
	res.Data = map[string]any{
		"path":           c.path,
		"current_bytes":  size,
		"baseline_bytes": baseline.size,
		"growth_bytes":   growth,
		"window":         c.window.String(),
		"value":          growth,
	}
	return res
}

// humanizeSigned renders a possibly-negative byte delta for the message.
func humanizeSigned(n int64) string {
	if n < 0 {
		return "-" + humanize.Bytes(uint64(-n))
	}
	return humanize.Bytes(uint64(n))
}

// dirOrFileSize returns the size of a regular file, or the recursive sum of
// regular-file sizes under a directory.
func dirOrFileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return info.Size(), nil
	}
	var total int64
	err = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			total += fi.Size()
		}
		return nil
	})
	return total, err
}

// parseSize parses a human byte size ("1GB", "500MB", "2GiB", "1048576").
func parseSize(s string) (int64, error) {
	n, err := humanize.ParseBytes(s)
	if err != nil {
		return 0, err
	}
	return int64(n), nil //nolint:gosec // sizes are far below int64 max
}
