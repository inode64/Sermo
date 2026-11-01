package checks

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/hostfs"
)

// pathRunGrace outlasts a run's own timeout. A walk that is merely slow stops at
// its context's deadline and reports that itself; only a run still silent after
// the grace is blocked in the kernel, where no context reaches it.
const pathRunGrace = 250 * time.Millisecond

// pathRuns holds the check runs whose path reads have not returned. A stat, open
// or walk on a hard network mount whose server is gone blocks in the kernel, so
// without it the run would keep its worker and its slot in the shared budget for
// as long as the mount stays hung, and enough of them would starve every other
// service's checks. It outlives a config reload, which rebuilds the checks but
// must not stack another blocked run on the same path.
var pathRuns = hostfs.NewInFlight[Result]("read", hostfs.ErrHung, pathRunGrace)

// pathGuardCheck runs a check that reads operator-named paths under pathRuns.
// The whole run goes under one guard, however many candidates it stats or files
// it walks: one goroutine per run, and nothing to discard but the result. The
// run keeps the check's state to itself, because every later cycle is refused
// until it returns; what it measured late still lands there, so a log line it
// read is counted by the next cycle rather than lost with its result.
type pathGuardCheck struct {
	Check
	key, target string
	timeout     time.Duration
}

func withPathGuard(check Check, key, target string, timeout time.Duration) Check {
	return pathGuardCheck{Check: check, key: key, target: target, timeout: timeout}
}

// guardPathReads wraps a check of a type that reads the paths its entry names.
// Without a timeout there is nothing to bound it by.
func guardPathReads(check Check, typ, name, service string, entry map[string]any, timeout time.Duration) Check {
	paths := slices.Concat(cfgval.StringList(entry[CheckKeyPath]), cfgval.StringList(entry[CheckKeyBinary]))
	if !checkSpecByName[typ].readsPaths || len(paths) == 0 || timeout <= 0 {
		return check
	}
	return withPathGuard(check, pathRunKey(service, name, entry), strings.Join(paths, ", "), timeout)
}

// pathRunKey identifies a run by its check and whole configuration, not by its
// path alone: callers of one key share a result, which two differently configured
// checks of one path must not, and a monitoring check and the preflight built
// again from the same entry may.
func pathRunKey(service, name string, entry map[string]any) string {
	return fmt.Sprintf("%s\x00%s\x00%v", service, name, entry)
}

func (c pathGuardCheck) resultMetadata() Result { return checkResultMetadata(c.Check) }

func (c pathGuardCheck) Run(ctx context.Context) Result {
	start := time.Now()
	res, err := pathRuns.Do(c.key, c.timeout, func() (Result, error) { return Execute(ctx, c.Check), nil })
	if err != nil {
		res = checkResultMetadata(c.Check)
		res.Unavailable, res.Latency = true, time.Since(start)
		res.Message = fmt.Sprintf("%s: %v", c.target, err)
		return res
	}
	// Callers sharing a run each decorate its result, so none gets the map itself.
	res.Data = maps.Clone(res.Data)
	return res
}
