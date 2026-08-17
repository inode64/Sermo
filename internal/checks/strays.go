package checks

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"sermo/internal/cfgval"
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
// nothing in its configuration accounts for. It is a condition check whose OK
// always means "healthy": a rule acts on it with `failed:`.
//
// Both thresholds are bounds above which the check fails, so that polarity holds
// in every form — the shape `clock.max_offset` and `firewall_rules.min_rules`
// already use. A level predicate (`count: {op, value}`) would have inverted it for
// a configured instance while the injected one kept `failed:`, which is exactly the
// confusion that once made the generated stale-binary rule fire on healthy hosts.
//
//   - max: fail above this many strays. The default 0 is the useful one — a stray
//     is unexplained by definition — and a service that legitimately parks
//     processes in its cgroup can raise the floor instead of going blind.
//   - maxIncrease over window: fail when the count grew by more than this within a
//     wall-clock span. This is growth over a window rather than a per-cycle delta
//     on purpose: an edge sensor that is true for one cycle cannot survive a rule's
//     `for:` window, and a per-check `interval:` would smear the delta across
//     reused cycles. Growth stays true for the whole window, so it composes.
type straysCheck struct {
	base
	strays      StraysFunc
	max         float64
	maxIncrease float64
	window      time.Duration
	clock       func() time.Time
	state       *counterWindow
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
	if c.maxIncrease > 0 {
		return c.growthResult(strays, exes, start)
	}

	ok := float64(len(strays)) <= c.max
	res := c.result(ok, c.levelMessage(len(strays), exes), start)
	res.Data = straysData(strays, exes, c.max)
	res.Data[DataKeyValue] = float64(len(strays))
	return res
}

// growthResult applies the sliding-window growth bound. The baseline is the oldest
// sample still inside the window, so a count that rose and settled stops failing
// once the window has moved past the rise.
func (c straysCheck) growthResult(strays []process.Process, exes []string, start time.Time) Result {
	state := c.state
	if state == nil {
		// Defensive only: a growth bound is always built with a shared state.
		state = &counterWindow{}
	}
	rise, covered := state.advance(windowClock(c.clock)(), len(strays), c.window)
	// A count can legitimately fall — an operator reaped, or the init cleaned the
	// cgroup — so only a rise is a growth.
	growth := max(rise, 0)
	ok := float64(growth) <= c.maxIncrease
	span := covered.Round(time.Second)

	message := fmt.Sprintf("%d stray process(es), %+d in %s (max_increase %s)",
		len(strays), growth, span, formatThreshold(c.maxIncrease))
	if !ok {
		message = fmt.Sprintf("stray processes grew by %d in %s (max_increase %s): %d now (%s); %s",
			growth, span, formatThreshold(c.maxIncrease), len(strays), strings.Join(exes, ", "), StrayReapHint)
	}

	res := c.result(ok, message, start)
	res.Data = straysData(strays, exes, c.maxIncrease)
	res.Data[DataKeyBaselineCount] = len(strays) - rise
	res.Data[DataKeyGrowthCount] = growth
	res.Data[DataKeyWindow] = c.window.String()
	res.Data[DataKeyValue] = float64(growth)
	return res
}

func (c straysCheck) levelMessage(count int, exes []string) string {
	if count == 0 {
		return "no stray processes"
	}
	if float64(count) <= c.max {
		return fmt.Sprintf("%d stray process(es) within max %s (%s)",
			count, formatThreshold(c.max), strings.Join(exes, ", "))
	}
	return fmt.Sprintf("%d stray process(es) unaccounted for by any selector (%s); %s",
		count, strings.Join(exes, ", "), StrayReapHint)
}

// straysData is the payload both forms share. op and threshold describe the bound the
// check fails above, so `${check.threshold}` in an alert message resolves and the
// dashboard renders a condition row instead of a bare number.
func straysData(strays []process.Process, exes []string, threshold float64) map[string]any {
	data := map[string]any{
		DataKeyType:      CheckTypeStrays,
		DataKeyCount:     len(strays),
		DataKeyOp:        cfgval.CompareOpGreater,
		DataKeyThreshold: threshold,
	}
	if len(strays) > 0 {
		pids := make([]string, 0, len(strays))
		for _, stray := range strays {
			pids = append(pids, strconv.Itoa(stray.PID))
		}
		data[DataKeyPIDs] = strings.Join(pids, ",")
		data[DataKeyPath] = strings.Join(exes, ",")
	}
	return data
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
	return unnamedProcess
}
