package checks

import (
	"context"
	"os"
)

// lockfileCheck passes when any configured candidate exists and is a regular
// file. It is for runtime lock artifacts created by the monitored service.
type lockfileCheck struct {
	base
	paths []string
}

func (c lockfileCheck) Run(_ context.Context) Result {
	return pathMatchResult(c.base, c.paths, lockfileCandidate, CheckTypeLockfile)
}
func lockfileCandidate(path string, info os.FileInfo) pathMatch {
	if !info.Mode().IsRegular() {
		return pathMatch{failure: path + " is not a regular file"}
	}
	return pathMatch{
		message: path + " is a regular lockfile",
		data:    map[string]any{DataKeyPath: path, DataKeySize: info.Size()},
	}
}
