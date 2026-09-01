package checks

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/execx"
)

// countCheck is condition-style: OK means the entry count or count growth
// matches the configured predicate.
type countCheck struct {
	base
	path          string
	kind          string
	recursive     bool
	includeHidden bool
	op            string
	value         float64
	deltaOp       string
	deltaValue    float64
	window        time.Duration
	clock         func() time.Time
	state         *counterWindow
}

func (c countCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start

	n, err := c.tally(ctx)
	if err != nil {
		return c.unavailableResult(fmt.Sprintf("count %s: %s", c.path, execx.ContextFailure(err, c.timeout)), start)
	}
	if c.deltaOp != "" {
		return c.runDelta(n, start)
	}

	ok := cfgval.CompareFloat(float64(n), c.op, c.value)
	scope := "in"
	if c.recursive {
		scope = "under"
	}
	res := c.result(ok, fmt.Sprintf("%d %s entries %s %s (want %s %s)",
		n, c.kind, scope, c.path, c.op, formatThreshold(c.value)), start)
	res.Data = map[string]any{
		DataKeyPath:           c.path,
		DataKeyOf:             c.kind,
		DataKeyRecursive:      c.recursive,
		CheckKeyIncludeHidden: c.includeHidden,
		DataKeyCount:          n,
		DataKeyValue:          n,
	}
	return res
}

func (c countCheck) runDelta(n int, start time.Time) Result {
	state := c.state
	if state == nil {
		// Defensive only: delta checks are always built with a shared state.
		state = &counterWindow{}
	}
	growth, span := state.advance(windowClock(c.clock)(), n, c.window)
	ok := growth > 0 && cfgval.CompareFloat(float64(growth), c.deltaOp, c.deltaValue)

	scope := "in"
	if c.recursive {
		scope = "under"
	}
	res := c.result(ok, fmt.Sprintf("%d %s entries %s %s (%+d in %s, want %s %s)",
		n, c.kind, scope, c.path, growth, span.Round(time.Second),
		c.deltaOp, formatThreshold(c.deltaValue)), start)
	res.Data = map[string]any{
		DataKeyPath:           c.path,
		DataKeyOf:             c.kind,
		DataKeyRecursive:      c.recursive,
		CheckKeyIncludeHidden: c.includeHidden,
		DataKeyCount:          n,
		DataKeyBaselineCount:  n - growth,
		DataKeyGrowthCount:    growth,
		DataKeyWindow:         c.window.String(),
		DataKeyValue:          growth,
	}
	return res
}

// tally excludes the root path itself.
func (c countCheck) tally(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("count entries in %q: %w", c.path, err)
	}
	if c.recursive {
		return c.tallyRecursive(ctx)
	}
	entries, err := os.ReadDir(c.path)
	if err != nil {
		return 0, fmt.Errorf("read entries in %q: %w", c.path, err)
	}
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("count entries in %q: %w", c.path, err)
	}
	n := 0
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return 0, fmt.Errorf("count entries in %q: %w", c.path, err)
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
			return fmt.Errorf("count entries in %q: %w", path, ctxErr)
		}
		if err != nil {
			// A failure to open the root is fatal; an unreadable subdirectory is
			// skipped so the count covers everything that could be read.
			if path == c.path {
				return fmt.Errorf("walk entries in %q: %w", path, err)
			}
			return nil
		}
		if path == c.path {
			return nil // never count the root directory itself
		}
		if !c.includeHidden && IsHiddenDescendant(c.path, path, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if c.matches(d.Type()) {
			n++
		}
		return nil
	})
	if err != nil {
		return n, fmt.Errorf("walk entries in %q: %w", c.path, err)
	}
	return n, nil
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

// validCountKind reports whether s is a supported `of` value.
func validCountKind(s string) bool {
	switch s {
	case CountKindAny, CountKindFile, CountKindDir, CountKindSymlink:
		return true
	default:
		return false
	}
}
