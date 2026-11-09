package checks

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
	"sermo/internal/execx"
	"sermo/internal/hostfs"
)

const (
	coredumpCollector       = "systemd-coredump"
	coredumpSocketUnit      = "systemd-coredump.socket"
	systemdSocketInterface  = "org.freedesktop.systemd1.Socket"
	systemdSocketNAccepted  = "NAccepted"
	corePatternPipePrefix   = "|"
	coredumpUnavailableHead = CheckTypeCoredump + ": "
)

// CoredumpSamplerFunc reads the cumulative number of core dumps the host's
// collector has taken in. Injected for tests; the default asks systemd-coredump.
type CoredumpSamplerFunc func(ctx context.Context) (uint64, error)

// coredumpCheck fires when processes have dumped core since the last cycle. Like
// oom it consumes the delta of a cumulative counter, so a crash is true for one
// cycle and a watch keeps it visible with `clear:`; a window would hide it.
// Stateful, so a pointer type; a watch ticks sequentially on its own goroutine.
// OK==true means "fire".
type coredumpCheck struct {
	base
	op      string
	value   float64
	sampler CoredumpSamplerFunc

	primed    bool
	lastCount uint64
}

func (c *coredumpCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start

	sampler := c.sampler
	if sampler == nil {
		sampler = defaultCoredumpSampler
	}
	count, err := sampler(ctx)
	if err != nil {
		return c.unavailableResult(coredumpUnavailableHead+execx.FormatContextOrError(err, c.timeout), start)
	}
	if !c.primed {
		c.primed, c.lastCount = true, count
		res := c.result(false, fmt.Sprintf("coredump baseline %d dumps", count), start)
		res.Data = map[string]any{DataKeyValue: uint64(0), DataKeyDumps: uint64(0), DataKeyTotal: count}
		return res
	}
	delta := deltaOrZero(count, c.lastCount)
	c.lastCount = count
	met := cfgval.CompareFloat(float64(delta), c.op, c.value)
	res := c.gradeValue(c.result(met, fmt.Sprintf("core dumps +%d (total %d)", delta, count), start), CheckKeyDelta, float64(delta))
	res.Data = map[string]any{DataKeyValue: delta, DataKeyDumps: delta, DataKeyTotal: count}
	return res
}

// defaultCoredumpSampler reads how many connections systemd-coredump.socket has
// accepted since it started: the kernel opens one per dump, so the count
// includes a dump whose handler then timed out or was killed, which leaves no
// journal entry and no core file. The kernel keeps no counter of its own, and a
// core_pattern that does not pipe to systemd-coredump would read a flat 0
// forever, so that host reports the check unavailable instead.
func defaultCoredumpSampler(ctx context.Context) (uint64, error) {
	pattern, err := hostfs.ReadFile(procCorePatternPath)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", procCorePatternPath, err)
	}
	if !pipesToCoredumpCollector(string(pattern)) {
		return 0, fmt.Errorf("kernel.core_pattern %q does not pipe core dumps to %s", strings.TrimSpace(string(pattern)), coredumpCollector)
	}
	value, err := conn.SystemdUnitProperty(ctx, coredumpSocketUnit, systemdSocketInterface, systemdSocketNAccepted)
	if err != nil {
		return 0, fmt.Errorf("ask systemd for the dump count: %w", err)
	}
	accepted, ok := value.(uint32)
	if !ok {
		return 0, fmt.Errorf("%s %s is %T, want uint32", coredumpSocketUnit, systemdSocketNAccepted, value)
	}
	return uint64(accepted), nil
}

// pipesToCoredumpCollector reports whether a kernel.core_pattern hands dumps to
// systemd-coredump: a pipe whose program is that binary, wherever installed.
func pipesToCoredumpCollector(pattern string) bool {
	program, piped := strings.CutPrefix(strings.TrimSpace(pattern), corePatternPipePrefix)
	if !piped {
		return false
	}
	fields := strings.Fields(program)
	return len(fields) > 0 && filepath.Base(fields[0]) == coredumpCollector
}

// buildCoredumpCheck builds a core-dump delta check (defaults to firing on any dump).
func buildCoredumpCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	op, value := cfgval.CompareOpGreater, 0.0
	if raw, present := entry[CheckKeyDelta]; present {
		var errs string
		if op, value, errs = parseDeltaThreshold(raw, CheckTypeCoredump); errs != "" {
			return nil, errs
		}
	}
	return &coredumpCheck{base: b, op: op, value: value, sampler: deps.CoredumpSampler}, ""
}
