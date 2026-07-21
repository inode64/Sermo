package checks

import (
	"context"
	"os"
)

// socketCheck passes when any configured candidate exists and is a Unix socket.
type socketCheck struct {
	base
	paths []string
}

func (c socketCheck) Run(_ context.Context) Result {
	return pathMatchResult(c.base, c.paths, socketCandidate, CheckTypeSocket)
}

func socketCandidate(path string, info os.FileInfo) pathMatch {
	if info.Mode()&os.ModeSocket == 0 {
		return pathMatch{failure: path + " is not a socket"}
	}
	return pathMatch{
		message: path + " is a socket",
		data:    map[string]any{DataKeyPath: path},
	}
}
