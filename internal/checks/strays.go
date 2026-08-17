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

// StraysFunc reports a service's stray processes: the members of its init unit's
// control group that no configured selector claims and that no longer hang off
// the unit's principal process.
type StraysFunc func() []process.Process

// StrayReapHint is the remedy for an accumulating stray. The check and the
// residual report of a failed stop both end with it, so the two never word the
// same fix differently.
const StrayReapHint = "`sermoctl reap` lists them and, with reap.kill_only_if declared, clears them"

// straysCheck reports processes the init backend attributes to this service that
// nothing in its configuration accounts for. It is a condition check: OK means
// there are none, so a rule acts on it with `failed:` — `active:` evaluates to the
// check's OK, which is the opposite of the condition worth alerting on.
//
// It takes no threshold on purpose. A stray is unexplained by definition, so the
// expected count is zero; a rule's `for:` window is what distinguishes a
// transient leftover from an accumulation worth waking someone for.
type straysCheck struct {
	base
	strays StraysFunc
}

func (c straysCheck) Run(_ context.Context) Result {
	start := time.Now()
	if c.strays == nil {
		return c.unavailableResult("process discovery unavailable", start)
	}
	strays := process.Strays(c.strays())
	// Name the executables: the count alone says something accumulated, the names
	// say what, which is the difference between an alert and a diagnosis.
	exes := strayExecutables(strays)
	message := "no stray processes"
	if len(strays) > 0 {
		message = fmt.Sprintf("%d stray process(es) unaccounted for by any selector (%s); %s",
			len(strays), strings.Join(exes, ", "), StrayReapHint)
	}

	res := c.result(len(strays) == 0, message, start)
	res.Data = map[string]any{
		DataKeyType:  CheckTypeStrays,
		DataKeyValue: float64(len(strays)),
		DataKeyCount: len(strays),
	}
	if len(strays) > 0 {
		pids := make([]string, 0, len(strays))
		for _, stray := range strays {
			pids = append(pids, strconv.Itoa(stray.PID))
		}
		res.Data[DataKeyPIDs] = strings.Join(pids, ",")
		res.Data[DataKeyPath] = strings.Join(exes, ",")
	}
	return res
}

// strayExecutables lists each stray's executable once, in a stable order, so the
// message and the alert do not reshuffle between cycles. A stray whose exe cannot
// be resolved falls back to its command, then to a placeholder: an unidentifiable
// process is exactly the kind this check exists to surface, so it must never
// vanish from the list.
func strayExecutables(strays []process.Process) []string {
	values := make([]string, 0, len(strays))
	for _, stray := range strays {
		values = append(values, strayExecutable(stray))
	}
	exes := strutil.MergeUnique(nil, values...)
	slices.Sort(exes)
	return exes
}

func strayExecutable(stray process.Process) string {
	if stray.ExeOK && stray.Exe != "" {
		return stray.Exe
	}
	if stray.ExePrev != "" {
		return stray.ExePrev
	}
	if cmd := strings.TrimSpace(strings.Join(stray.Cmdline, " ")); cmd != "" {
		return cmd
	}
	return strayUnknownExecutable
}

const strayUnknownExecutable = "unknown"
