package checks

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"sermo/internal/process"
	"sermo/internal/strutil"
)

// StaleBinariesFunc reports a service's processes whose binary was replaced or
// removed on disk.
type StaleBinariesFunc func() []process.StaleBinary

// staleBinaryCheck reports processes of this service still running a binary
// that was replaced or removed on disk — a package upgrade without a restart.
// It is a condition check: OK means nothing is stale, so a rule fires on it
// with `failed:` -- `active:` evaluates to the check's OK, which is the
// opposite of the condition worth acting on.
//
// It covers the two ways the condition reaches an operator today, neither of
// which is actionable on its own: a process attributed by the init backend
// keeps running with an unusable exe and nothing is reported at all, or an
// exe selector stops matching and the service merely looks like it has no
// processes.
type staleBinaryCheck struct {
	base
	stale StaleBinariesFunc
}

func (c staleBinaryCheck) Run(_ context.Context) Result {
	start := time.Now()
	if c.stale == nil {
		return c.unavailableResult("process discovery unavailable", start)
	}
	stale := c.stale()
	// Name the replaced binaries: the path is what tells the operator which
	// package to blame and what a restart would pick up.
	paths := staleBinaryPaths(stale)
	message := "no replaced binaries"
	if len(stale) > 0 {
		message = fmt.Sprintf("%d process(es) run a replaced binary (%s); restart to pick up the installed version",
			len(stale), strings.Join(paths, ", "))
	}

	res := c.result(len(stale) == 0, message, start)
	res.Data = map[string]any{
		DataKeyType:  CheckTypeStaleBinary,
		DataKeyValue: float64(len(stale)),
	}
	if len(stale) > 0 {
		pids := make([]string, 0, len(stale))
		for _, s := range stale {
			pids = append(pids, strconv.Itoa(s.PID))
		}
		res.Data[DataKeyPIDs] = strings.Join(pids, ",")
		res.Data[DataKeyPath] = strings.Join(paths, ",")
	}
	return res
}

// staleBinaryPaths lists each replaced binary once, in a stable order, so the
// message and the alert do not reshuffle between cycles.
func staleBinaryPaths(stale []process.StaleBinary) []string {
	values := make([]string, 0, len(stale))
	for _, s := range stale {
		values = append(values, s.Path)
	}
	paths := strutil.MergeUnique(nil, values...)
	slices.Sort(paths)
	return paths
}
