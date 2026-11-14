package checks

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

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
	// coredumpStorageDir is where systemd-coredump keeps the dumps it stores
	// (Storage=external, its default).
	coredumpStorageDir = "/var/lib/systemd/coredump"
	// maxNamedCoredumps bounds how many crashes one message names; the count
	// still says how many there were.
	maxNamedCoredumps = 5
	// coredumpNameSlack widens the window a stored dump must fall in: its file
	// name dates it to the second, the previous sample to the nanosecond.
	coredumpNameSlack = time.Second
)

// coredumpFileName parses systemd-coredump's file name,
// core.COMM.UID.BOOTID.PID.USEC[.COMPRESSION]; COMM may itself hold dots.
var coredumpFileName = regexp.MustCompile(`^core\.(.+)\.[0-9]+\.[0-9a-f]{32}\.([0-9]+)\.([0-9]+)(?:\.[a-z0-9]+)?$`)

// CoredumpSamplerFunc reads the cumulative number of core dumps the host's
// collector has taken in. Injected for tests; the default asks systemd-coredump.
type CoredumpSamplerFunc func(ctx context.Context) (uint64, error)

// CoredumpListerFunc lists the file names of the dumps systemd-coredump stored.
// Injected for tests; the default reads its storage directory.
type CoredumpListerFunc func() ([]string, error)

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
	lister  CoredumpListerFunc
	clock   func() time.Time

	primed    bool
	lastCount uint64
	lastAt    time.Time
}

func (c *coredumpCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start

	sampler := c.sampler
	if sampler == nil {
		sampler = defaultCoredumpSampler
	}
	now := windowClock(c.clock)()
	count, err := sampler(ctx)
	if err != nil {
		return c.unavailableResult(coredumpUnavailableHead+execx.FormatContextOrError(err, c.timeout), start)
	}
	if !c.primed {
		c.primed, c.lastCount, c.lastAt = true, count, now
		res := c.result(false, fmt.Sprintf("coredump baseline %d dumps", count), start)
		res.Data = map[string]any{DataKeyValue: uint64(0), DataKeyDumps: uint64(0), DataKeyTotal: count}
		return res
	}
	delta := deltaOrZero(count, c.lastCount)
	since := c.lastAt
	c.lastCount, c.lastAt = count, now
	msg := fmt.Sprintf("core dumps +%d (total %d)", delta, count)
	if delta > 0 {
		if crashed := c.crashedSince(since); crashed != "" {
			msg += ": " + crashed
		}
	}
	met := cfgval.CompareFloat(float64(delta), c.op, c.value)
	res := c.gradeValue(c.result(met, msg, start), CheckKeyDelta, float64(delta))
	res.Data = map[string]any{DataKeyValue: delta, DataKeyDumps: delta, DataKeyTotal: count}
	return res
}

// crashedSince names the processes whose stored dumps are dated from since on,
// oldest first, as "COMM (pid PID)". The socket counter that detected the crash
// knows no process, and the journal is out of reach without a client, so the
// stored files are the only native record. A dump still being written, or one
// systemd-coredump did not store (Storage=none, over its size limit), stays
// unnamed; a listing that fails costs the names, never the alert.
func (c *coredumpCheck) crashedSince(since time.Time) string {
	lister := c.lister
	if lister == nil {
		lister = defaultCoredumpLister
	}
	names, err := lister()
	if err != nil {
		return ""
	}
	type crash struct {
		at, pid int64
		comm    string
	}
	threshold := since.Add(-coredumpNameSlack).UnixMicro()
	var crashes []crash
	for _, name := range names {
		m := coredumpFileName.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		at, atErr := strconv.ParseInt(m[3], 10, 64)
		pid, pidErr := strconv.ParseInt(m[2], 10, 64)
		if atErr != nil || pidErr != nil || at < threshold {
			continue
		}
		crashes = append(crashes, crash{at: at, pid: pid, comm: m[1]})
	}
	slices.SortFunc(crashes, func(a, b crash) int {
		return cmp.Or(cmp.Compare(a.at, b.at), cmp.Compare(a.pid, b.pid))
	})
	labels := make([]string, 0, min(len(crashes), maxNamedCoredumps))
	for _, cr := range crashes[:min(len(crashes), maxNamedCoredumps)] {
		labels = append(labels, fmt.Sprintf("%s (pid %d)", cr.comm, cr.pid))
	}
	if extra := len(crashes) - len(labels); extra > 0 {
		labels = append(labels, fmt.Sprintf("%d more", extra))
	}
	return strings.Join(labels, ", ")
}

func defaultCoredumpLister() ([]string, error) {
	entries, err := hostfs.ReadDir(coredumpStorageDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", coredumpStorageDir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			names = append(names, entry.Name())
		}
	}
	return names, nil
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
	return &coredumpCheck{base: b, op: op, value: value, sampler: deps.CoredumpSampler, lister: deps.CoredumpLister}, ""
}
