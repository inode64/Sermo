package checks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/execx"
)

// countCheck is condition-style: OK means the entry count or count growth
// matches the configured predicate.
type countCheck struct {
	base
	path       string
	kind       string
	recursive  bool
	op         string
	value      float64
	deltaOp    string
	deltaValue float64
	window     time.Duration
	clock      func() time.Time
	state      *countState
}

type countSample struct {
	t     time.Time
	count int
}

type countState struct {
	samples []countSample
}

func (c countCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	n, err := c.tally(ctx)
	if err != nil {
		return c.result(false, fmt.Sprintf("count %s: %s", c.path, execx.ContextFailure(err, c.timeout)), start)
	}
	if c.deltaOp != "" {
		return c.runDelta(n, start)
	}

	ok := compareFloat(float64(n), c.op, c.value)
	scope := "in"
	if c.recursive {
		scope = "under"
	}
	res := c.result(ok, fmt.Sprintf("%d %s entries %s %s (want %s %s)",
		n, c.kind, scope, c.path, c.op, strconv.FormatFloat(c.value, floatFormatFixed, floatPrecisionAuto, numericBits64)), start)
	res.Data = map[string]any{
		DataKeyPath:      c.path,
		DataKeyOf:        c.kind,
		DataKeyRecursive: c.recursive,
		DataKeyCount:     n,
		DataKeyValue:     n,
	}
	return res
}

func (c countCheck) runDelta(n int, start time.Time) Result {
	clock := c.clock
	if clock == nil {
		clock = time.Now
	}
	st := c.state
	if st == nil {
		// Defensive only: delta checks are always built with a shared state.
		st = &countState{}
	}
	now := clock()
	cutoff := now.Add(-c.window)
	old := st.samples
	st.samples = st.samples[:0]
	for _, s := range old {
		if !s.t.Before(cutoff) {
			st.samples = append(st.samples, s)
		}
	}
	st.samples = append(st.samples, countSample{t: now, count: n})

	baseline := st.samples[0]
	growth := n - baseline.count
	ok := growth > 0 && compareFloat(float64(growth), c.deltaOp, c.deltaValue)

	scope := "in"
	if c.recursive {
		scope = "under"
	}
	span := now.Sub(baseline.t)
	res := c.result(ok, fmt.Sprintf("%d %s entries %s %s (%+d in %s, want %s %s)",
		n, c.kind, scope, c.path, growth, span.Round(time.Second),
		c.deltaOp, strconv.FormatFloat(c.deltaValue, floatFormatFixed, floatPrecisionAuto, numericBits64)), start)
	res.Data = map[string]any{
		DataKeyPath:          c.path,
		DataKeyOf:            c.kind,
		DataKeyRecursive:     c.recursive,
		DataKeyCount:         n,
		DataKeyBaselineCount: baseline.count,
		DataKeyGrowthCount:   growth,
		DataKeyWindow:        c.window.String(),
		DataKeyValue:         growth,
	}
	return res
}

// tally excludes the root path itself.
func (c countCheck) tally(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if c.recursive {
		return c.tallyRecursive(ctx)
	}
	entries, err := os.ReadDir(c.path)
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if c.matches(e.Type()) {
			n++
		}
	}
	return n, nil
}

func (c countCheck) tallyRecursive(ctx context.Context) (int, error) {
	n := 0
	err := filepath.WalkDir(c.path, func(path string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			// A failure to open the root is fatal; an unreadable subdirectory is
			// skipped so the count covers everything that could be read.
			if path == c.path {
				return err
			}
			return nil
		}
		if path == c.path {
			return nil // never count the root directory itself
		}
		if c.matches(d.Type()) {
			n++
		}
		return nil
	})
	return n, err
}

// matches applies the configured lstat-kind filter.
func (c countCheck) matches(typ fs.FileMode) bool {
	switch c.kind {
	case CountKindAny:
		return true
	case CountKindSymlink:
		return typ&fs.ModeSymlink != 0
	case CountKindDir:
		return typ&fs.ModeSymlink == 0 && typ.IsDir()
	case CountKindFile:
		return typ&fs.ModeSymlink == 0 && typ.IsRegular()
	default:
		return false
	}
}

// TallyEntries counts path entries matching kind (any, file, dir, symlink). The
// root path itself is never included. Used by the web UI for live count-watch
// readings without re-running the full check. timeout bounds the probe context
// and is used for operator-facing timeout messages.
func TallyEntries(ctx context.Context, path, kind string, recursive bool, timeout time.Duration) (int, error) {
	if kind == "" {
		kind = CountKindAny
	}
	c := countCheck{base: base{timeout: timeout}, path: path, kind: kind, recursive: recursive, op: cfgval.CompareOpGreaterEqual, value: 0}
	n, err := c.tally(ctx)
	if err != nil {
		return 0, errors.New(execx.ContextFailure(err, timeout))
	}
	return n, nil
}

// validCountKind reports whether s is a supported `of` value.
func validCountKind(s string) bool {
	switch s {
	case CountKindAny, CountKindFile, CountKindDir, CountKindSymlink:
		return true
	default:
		return false
	}
}
