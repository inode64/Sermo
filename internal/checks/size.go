package checks

import (
	"context"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/dustin/go-humanize"

	"sermo/internal/cfgval"
	"sermo/internal/execx"
)

// SizeSamplerFunc measures a file or directory in bytes.
type SizeSamplerFunc func(ctx context.Context, path string, includeHidden bool) (int64, error)

// sizeSample is one timestamped size observation.
type sizeSample struct {
	t    time.Time
	size int64
}

// sizeState stores samples across cycles for one built check.
type sizeState struct {
	samples []sizeSample
}

// sizeCheck is stateful: OK means path grew by growBy within window.
type sizeCheck struct {
	base
	path          string
	growBy        int64
	window        time.Duration
	includeHidden bool
	sampler       SizeSamplerFunc
	clock         func() time.Time
	state         *sizeState
}

func (c *sizeCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	sampler := c.sampler
	if sampler == nil {
		sampler = dirOrFileSize
	}
	clock := c.clock
	if clock == nil {
		clock = time.Now
	}

	size, err := sampler(ctx, c.path, c.includeHidden)
	if err != nil {
		return c.result(false, fmt.Sprintf("size %s: %s", c.path, execx.ContextFailure(err, c.timeout)), start)
	}
	now := clock()

	cutoff := now.Add(-c.window)
	c.state.samples = pruneWindow(c.state.samples, cutoff, func(s sizeSample) time.Time { return s.t })
	c.state.samples = append(c.state.samples, sizeSample{t: now, size: size})

	baseline := c.state.samples[0]
	growth := size - baseline.size
	ok := growth >= c.growBy // only increases trip the check

	span := now.Sub(baseline.t)
	msg := fmt.Sprintf("%s grew %s in %s (limit %s/%s)",
		c.path, HumanizeSignedBytes(growth), span.Round(time.Second),
		humanize.IBytes(uint64(c.growBy)), c.window)
	res := c.result(ok, msg, start)
	res.Data = map[string]any{
		DataKeyPath:           c.path,
		CheckKeyIncludeHidden: c.includeHidden,
		DataKeyCurrentBytes:   size,
		DataKeyBaselineBytes:  baseline.size,
		DataKeyGrowthBytes:    growth,
		DataKeyWindow:         c.window.String(),
		DataKeyValue:          growth,
	}
	return res
}

// HumanizeSignedBytes renders a possibly-negative byte delta in IEC binary
// units (KiB/MiB/GiB), the display convention for every byte value.
func HumanizeSignedBytes(n int64) string {
	if n < 0 {
		return "-" + humanize.IBytes(uint64(-n))
	}
	return humanize.IBytes(uint64(n))
}

// dirOrFileSize returns the size of a regular file, or the recursive sum of
// regular-file sizes under a directory.
func dirOrFileSize(ctx context.Context, path string, includeHidden bool) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return info.Size(), nil
	}
	var total int64
	err = filepath.WalkDir(path, func(entryPath string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return err
		}
		if !includeHidden && IsHiddenDescendant(path, entryPath, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
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

// parseSize parses a human byte size with an explicit suffix ("1G", "500M",
// "2GiB") through the shared config grammar (cfgval.ByteSize): binary units,
// unitless values rejected — the same rules as every other size field
// (free_bytes, expand.by).
func parseSize(s string) (int64, error) {
	n, ok := cfgval.ByteSize(s)
	if !ok || n == 0 || n > math.MaxInt64 {
		return 0, fmt.Errorf("size %q must be positive with a K/M/G/T suffix (e.g. 1G, 500M)", s)
	}
	return int64(n), nil
}
