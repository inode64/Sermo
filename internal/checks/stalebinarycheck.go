package checks

import (
	"context"
	"fmt"
	"strings"
	"time"

	"sermo/internal/process"
	"sermo/internal/strutil"
)

// StaleBinariesFunc reports a service's processes whose binary was replaced or
// removed on disk.
type StaleBinariesFunc func() []process.StaleBinary

// StaleBinaryRestartHint is the remedy for a replaced executable. Both this
// check and the process check (whose exe selector stops matching such a
// process) end with it, so the two never word the same fix differently.
const StaleBinaryRestartHint = "restart to pick up the installed version"

// StaleBinaryRemovedHint is the remedy when no file is left at the path: a
// restart would stop the daemon and find nothing to start.
const StaleBinaryRemovedHint = "reinstall the package or stop the service; a restart cannot start it"

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
	removed, replaced := splitRemovedBinaries(stale)
	var parts []string
	if len(removed) > 0 {
		parts = append(parts, fmt.Sprintf("%d process(es) run a binary no longer installed (%s); %s",
			len(removed), strings.Join(staleBinaryPaths(removed), ", "), StaleBinaryRemovedHint))
	}
	if len(replaced) > 0 {
		parts = append(parts, fmt.Sprintf("%d process(es) run a replaced binary (%s); %s",
			len(replaced), strings.Join(staleBinaryPaths(replaced), ", "), StaleBinaryRestartHint))
	}
	message := "no replaced binaries"
	if len(parts) > 0 {
		message = strings.Join(parts, "; ")
	}

	res := c.result(len(stale) == 0, message, start)
	// A restart is the remedy only while the new version is installed: with the
	// package gone it would stop a serving daemon for good, so the reading
	// still alerts but drives no operation.
	res.ActionBlocked = len(removed) > 0
	res.Data = map[string]any{
		DataKeyType:  CheckTypeStaleBinary,
		DataKeyValue: float64(len(stale)),
	}
	if len(stale) > 0 {
		res.Data[DataKeyPIDs] = joinedPIDs(stale, func(stale process.StaleBinary) int { return stale.PID })
		res.Data[DataKeyPath] = strings.Join(paths, ",")
	}
	return res
}

// staleBinaryPaths lists each replaced binary once, in a stable order, so the
// message and the alert do not reshuffle between cycles.
func splitRemovedBinaries(stale []process.StaleBinary) (removed, replaced []process.StaleBinary) {
	for _, s := range stale {
		if s.Removed {
			removed = append(removed, s)
		} else {
			replaced = append(replaced, s)
		}
	}
	return removed, replaced
}

func staleBinaryPaths(stale []process.StaleBinary) []string {
	values := make([]string, 0, len(stale))
	for _, s := range stale {
		values = append(values, s.Path)
	}
	return strutil.SortedUnique(values)
}
