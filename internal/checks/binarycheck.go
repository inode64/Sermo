package checks

import (
	"context"
	"os"
	"time"
)

const binaryExecutableModeMask = 0o111

// binaryCheck passes when a path exists and is an executable file.
type binaryCheck struct {
	base
	path string
}

func (c binaryCheck) Run(_ context.Context) Result {
	start := time.Now()
	info, err := os.Stat(c.path)
	if err != nil {
		return c.result(false, c.path+" not found", start)
	}
	if info.IsDir() {
		return c.result(false, c.path+" is a directory", start)
	}
	if info.Mode().Perm()&binaryExecutableModeMask == 0 {
		return c.result(false, c.path+" is not executable", start)
	}
	return c.result(true, c.path+" is executable", start)
}
