package checks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path/filepath"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/execx"
	"sermo/internal/hostfs"
)

// SizeMeasure is one measurement of a path or glob: the byte total and the
// number of regular files it covers.
type SizeMeasure struct {
	Bytes int64
	Files int
}

// SizeSamplerFunc measures a file, a directory or a glob pattern in bytes.
type SizeSamplerFunc func(ctx context.Context, path string, includeHidden bool) (SizeMeasure, error)

// sizeCheck is a condition: OK means the measured total exceeds max, or grew
// by at least growBy within window. Either threshold may be absent (zero);
// the parser guarantees at least one is configured.
type sizeCheck struct {
	base
	path          string // a file, a directory or a glob pattern
	max           int64  // absolute total threshold; 0 when not configured
	growBy        int64  // growth threshold; 0 when no window is configured
	window        time.Duration
	includeHidden bool
	sampler       SizeSamplerFunc
	clock         func() time.Time
	state         *byteWindow
}

func (c *sizeCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()

	sampler := c.sampler
	if sampler == nil {
		sampler = pathSize
	}
	measure, err := sampler(ctx, c.path, c.includeHidden)
	if err != nil {
		return c.unavailableResult(fmt.Sprintf("size %s: %s", c.path, execx.ContextFailure(err, c.timeout)), run.start)
	}

	data := map[string]any{
		DataKeyPath:           c.path,
		CheckKeyIncludeHidden: c.includeHidden,
		DataKeyCurrentBytes:   measure.Bytes,
		DataKeyFiles:          measure.Files,
		DataKeyValue:          measure.Bytes,
	}
	overMax, msg := c.judgeMax(measure, data)
	parts := []string{msg}
	grew := false
	if c.window > 0 {
		grew, msg = c.judgeGrowth(windowClock(c.clock)(), measure.Bytes, data)
		parts = append(parts, msg)
	}
	res := c.result(overMax || grew, strings.Join(parts, "; "), run.start)
	res.Data = data
	return res
}

// judgeMax describes the measured total and, when max is configured, whether
// the total is over it. An absolute limit needs no baseline, so it judges the
// very first sample.
func (c *sizeCheck) judgeMax(measure SizeMeasure, data map[string]any) (over bool, msg string) {
	msg = fmt.Sprintf("%s is %s", c.path, HumanizeSignedBytes(measure.Bytes))
	if measure.Files != 1 {
		msg += fmt.Sprintf(" in %d files", measure.Files)
	}
	if c.max == 0 {
		return false, msg
	}
	data[DataKeyMaxBytes] = c.max
	if measure.Bytes > c.max {
		return true, msg + ", over max " + HumanizeSignedBytes(c.max)
	}
	return false, msg + " (max " + HumanizeSignedBytes(c.max) + ")"
}

// judgeGrowth advances the growth window with the measured total. Only an
// increase of at least grow_by trips it: a steady or shrinking path passes,
// and the first sample is a baseline. With a window, value is the growth.
func (c *sizeCheck) judgeGrowth(now time.Time, size int64, data map[string]any) (grew bool, msg string) {
	growth, span := c.state.advance(now, size, c.window)
	data[DataKeyBaselineBytes] = size - growth
	data[DataKeyGrowthBytes] = growth
	data[DataKeyWindow] = c.window.String()
	data[DataKeyValue] = growth
	return growth >= c.growBy, fmt.Sprintf("grew %s in %s (limit %s/%s)",
		HumanizeSignedBytes(growth), span.Round(time.Second), HumanizeSignedBytes(c.growBy), c.window)
}

// HumanizeSignedBytes renders a possibly-negative byte delta in IEC binary
// units (KiB/MiB/GiB) through the canonical value formatter, the display
// convention for every byte value.
func HumanizeSignedBytes(n int64) string {
	if n < 0 {
		return "-" + formatSummaryBytes(float64(-n))
	}
	return formatSummaryBytes(float64(n))
}

// pathSize measures a literal path, or the sum of every path a glob pattern
// matches. A pattern that matches nothing measures zero: the operator asked
// for "whatever is there", and an empty binlog or spool set is a healthy
// answer, unlike a literal path that is missing. A match that vanishes between
// the glob and its stat (a rotated log) is skipped the same way a vanished
// directory entry is.
func pathSize(ctx context.Context, path string, includeHidden bool) (SizeMeasure, error) {
	paths, glob, err := expandPathGlob(path)
	if err != nil {
		return SizeMeasure{}, err
	}
	var total SizeMeasure
	for _, p := range paths {
		measure, err := dirOrFileSize(ctx, p, includeHidden)
		if glob && errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return total, err
		}
		total.Bytes += measure.Bytes
		total.Files += measure.Files
	}
	return total, nil
}

// dirOrFileSize returns the size of a regular file, or the recursive sum of
// regular-file sizes under a directory, with the number of files it counted.
func dirOrFileSize(ctx context.Context, path string, includeHidden bool) (SizeMeasure, error) {
	if err := ctx.Err(); err != nil {
		return SizeMeasure{}, fmt.Errorf("measure size of %q: %w", path, err)
	}
	info, err := hostfs.StatTarget(path)
	if err != nil {
		return SizeMeasure{}, fmt.Errorf("stat size path %q: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return SizeMeasure{}, fmt.Errorf("measure size of %q: %w", path, err)
	}
	if !info.IsDir() {
		return SizeMeasure{Bytes: info.Size(), Files: 1}, nil
	}
	// WalkDir uses lstat for its root. Resolve the selected directory first so
	// a symlink measures its tree instead of silently reporting zero bytes.
	root, err := filepath.EvalSymlinks(path)
	if err != nil {
		return SizeMeasure{}, fmt.Errorf("resolve size path %q: %w", path, err)
	}
	var total SizeMeasure
	err = filepath.WalkDir(root, sizeWalkFunc(ctx, root, includeHidden, &total))
	if err != nil {
		return total, fmt.Errorf("walk size path %q: %w", path, err)
	}
	return total, nil
}

// sizeWalkFunc adds regular-file sizes under root to total. An entry that
// vanished between the directory read and its stat (spool, tmp, log
// rotation) no longer takes space, so it is skipped instead of making the
// whole sample unavailable and costing the growth window a sample; any other
// error, or root itself vanishing, still aborts the measurement.
func sizeWalkFunc(ctx context.Context, root string, includeHidden bool, total *SizeMeasure) fs.WalkDirFunc {
	return func(entryPath string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("measure size of %q: %w", entryPath, ctxErr)
		}
		if err != nil {
			if entryPath != root && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("walk size path %q: %w", entryPath, err)
		}
		if !includeHidden && IsHiddenDescendant(root, entryPath, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			fi, err := d.Info()
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("read size information for %q: %w", entryPath, err)
			}
			total.Bytes += fi.Size()
			total.Files++
		}
		return nil
	}
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
