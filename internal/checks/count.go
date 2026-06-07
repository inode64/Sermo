package checks

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// count entry kinds: which directory entries a count check tallies. Entries are
// classified by their lstat type, so a symlink is always a symlink (never
// followed) and is not also counted as a file or directory.
const (
	countAny     = "any"     // every entry, regardless of type
	countFile    = "file"    // regular files only
	countDir     = "dir"     // directories only
	countSymlink = "symlink" // symbolic links only
)

// countCheck tallies the directory entries under path that match a kind filter
// and compares the total to a threshold. Like diskCheck/metricCheck it is
// condition-style: OK=true means the `op value` predicate holds (so
// `active: {check: ...}` fires when the count crosses the threshold). With
// recursive it descends the whole subtree; otherwise only the immediate entries.
type countCheck struct {
	base
	path      string
	kind      string
	recursive bool
	op        string
	value     float64
}

func (c countCheck) Run(_ context.Context) Result {
	start := time.Now()

	n, err := c.tally()
	if err != nil {
		return c.result(false, fmt.Sprintf("count %s: %v", c.path, err), start)
	}

	ok := compareFloat(float64(n), c.op, c.value)
	scope := "in"
	if c.recursive {
		scope = "under"
	}
	res := c.result(ok, fmt.Sprintf("%d %s entries %s %s (want %s %s)",
		n, c.kind, scope, c.path, c.op, strconv.FormatFloat(c.value, 'f', -1, 64)), start)
	res.Data = map[string]any{
		"path":      c.path,
		"of":        c.kind,
		"recursive": c.recursive,
		"count":     n,
		"value":     n,
	}
	return res
}

// tally counts the matching entries, either directly under path or, when
// recursive, anywhere in its subtree (excluding path itself).
func (c countCheck) tally() (int, error) {
	if c.recursive {
		return c.tallyRecursive()
	}
	entries, err := os.ReadDir(c.path)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if c.matches(e.Type()) {
			n++
		}
	}
	return n, nil
}

func (c countCheck) tallyRecursive() (int, error) {
	n := 0
	err := filepath.WalkDir(c.path, func(path string, d fs.DirEntry, err error) error {
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

// matches reports whether an entry with the given lstat type bits is counted
// under the kind filter. WalkDir/ReadDir report the entry's own type without
// following symlinks, so links are classified as links, not their targets.
func (c countCheck) matches(typ fs.FileMode) bool {
	switch c.kind {
	case countAny:
		return true
	case countSymlink:
		return typ&fs.ModeSymlink != 0
	case countDir:
		return typ&fs.ModeSymlink == 0 && typ.IsDir()
	case countFile:
		return typ&fs.ModeSymlink == 0 && typ.IsRegular()
	default:
		return false
	}
}

// validCountKind reports whether s is a supported `of` value.
func validCountKind(s string) bool {
	switch s {
	case countAny, countFile, countDir, countSymlink:
		return true
	default:
		return false
	}
}
