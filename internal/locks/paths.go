package locks

import "path/filepath"

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
