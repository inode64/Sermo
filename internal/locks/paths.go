package locks

import (
	"fmt"
	"path/filepath"
	"strings"
)

const (
	runtimeDirLocks = "locks"
	runtimeDirOps   = "ops"
)

// RuntimeLocksDir is the named runtime lock directory under paths.runtime.
func RuntimeLocksDir(runtimeDir string) string {
	return filepath.Join(runtimeDir, runtimeDirLocks)
}

// RuntimeOpsDir is the operation lock directory under paths.runtime.
func RuntimeOpsDir(runtimeDir string) string {
	return filepath.Join(runtimeDir, runtimeDirOps)
}

// lockPath joins a single lock filename to the operator-configured directory.
// Validate before Join: it cleans away traversal components. Keep this boundary
// explicit even though callers also validate service and lock identifiers.
// Backslashes are allowed here because they encode named locks on Linux.
func lockPath(dir, filename string) (string, error) {
	if !filepath.IsLocal(filename) || filename == "." || filepath.Base(filename) != filename || strings.ContainsRune(filename, 0) {
		return "", fmt.Errorf("invalid lock filename %q: must be a local filename", filename)
	}
	return filepath.Join(dir, filename), nil
}
