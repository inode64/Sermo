package checks

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"sermo/internal/cfgval"
)

// parsePatternPath reads the `path` of a check that accepts a glob (log,
// size): required, absolute and cleaned. A pattern must also be well formed,
// so a typo fails at load instead of making the check unavailable on every
// cycle; filepath.Match on an empty name is the same syntax check
// filepath.Glob runs before it reads anything.
func parsePatternPath(entry map[string]any, checkType string) (string, error) {
	path := cfgval.String(entry[CheckKeyPath])
	switch {
	case path == "":
		return "", fmt.Errorf("%s check requires a path", checkType)
	case !filepath.IsAbs(path):
		return "", fmt.Errorf("%s check path must be absolute", checkType)
	}
	if cfgval.IsGlobPattern(path) {
		if _, err := filepath.Match(path, ""); err != nil {
			return "", fmt.Errorf("%s check path %q is not a valid glob: %w", checkType, path, err)
		}
	}
	return filepath.Clean(path), nil
}

// expandPathGlob resolves a configured path for the checks that accept a
// pattern (log, size): the sorted matches of a glob, where no match is a valid
// answer, or the literal path itself. glob reports which, so a caller can tell
// a match that vanished before its stat from a literal path that is missing.
//
// filepath.Glob ignores I/O errors, so a missing or wrong directory would read
// as "nothing matches" and hide a misconfigured path forever. The fixed
// directory before the first wildcard must therefore exist and be a directory;
// otherwise the expansion fails and the check reports unavailable.
func expandPathGlob(path string) (paths []string, glob bool, err error) {
	if !cfgval.IsGlobPattern(path) {
		return []string{path}, false, nil
	}
	base := globBase(path)
	info, err := os.Stat(base)
	if err != nil {
		return nil, true, fmt.Errorf("glob: %w", err)
	}
	if !info.IsDir() {
		return nil, true, fmt.Errorf("glob: %s: %w", base, errGlobBaseNotDir)
	}
	paths, err = filepath.Glob(path)
	if err != nil {
		return nil, true, fmt.Errorf("glob: %w", err)
	}
	slices.Sort(paths)
	return paths, true, nil
}

// errGlobBaseNotDir reports a pattern whose fixed directory is not a directory.
var errGlobBaseNotDir = errors.New("not a directory")

// globBase is the fixed directory of a pattern: its longest leading path with
// no wildcard ("/var/lib/mysql" for "/var/lib/mysql/*bin*.[0-9]*").
func globBase(pattern string) string {
	dir := filepath.Dir(pattern)
	for cfgval.IsGlobPattern(dir) {
		dir = filepath.Dir(dir)
	}
	return dir
}

// IsHiddenDescendant reports whether entry is a hidden path discovered below
// root. A hidden root named explicitly by the operator is not a descendant and
// remains included.
func IsHiddenDescendant(root, path string, entry fs.DirEntry) bool {
	return path != root && strings.HasPrefix(entry.Name(), ".")
}
