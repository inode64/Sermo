package checks

import (
	"context"
	"os"
	"time"

	"sermo/internal/hostfs"
)

const binaryExecutableModeMask = 0o111

// binaryCheck passes when a path exists and is an executable file.
type binaryCheck struct {
	base
	path         string
	resolvedPath string
}

func (c binaryCheck) Run(_ context.Context) Result {
	start := time.Now()
	info, err := hostfs.StatTarget(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return c.result(false, c.path+" not found", start)
		}
		return c.unavailableResult(c.path+": "+err.Error(), start)
	}
	if info.IsDir() {
		return c.result(false, c.path+" is a directory", start)
	}
	if info.Mode().Perm()&binaryExecutableModeMask == 0 {
		return c.result(false, c.path+" is not executable", start)
	}
	if c.resolvedPath != "" {
		resolved, err := hostfs.EvalSymlinksTarget(c.path)
		if err != nil {
			return c.unavailableResult(err.Error(), start)
		}
		return c.result(resolved == c.resolvedPath, c.path+" resolves to "+resolved+"; expected "+c.resolvedPath, start)
	}
	return c.result(true, c.path+" is executable", start)
}
