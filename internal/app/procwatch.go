package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/metrics"
	"sermo/internal/notify"
	"sermo/internal/process"
)

// procClockTicks is the kernel USER_HZ (jiffies/second), used to turn CPU ticks
// into seconds. 100 is correct on virtually all Linux builds (mirrors metrics).
const procClockTicks = 100.0

// ProcMatch selects which processes a process watch tracks: by name (the exe
// basename or its full resolved path) and optionally the owning user.
type ProcMatch struct {
	Name string
	User string
}

// ProcInfo is one matched process's current resource counters. CPU and IO are
// cumulative; the watch derives rates from successive samples.
type ProcInfo struct {
	PID      int
	CPUTicks uint64 // accumulated CPU jiffies (utime+stime)
	RSS      uint64 // resident memory bytes
	IOBytes  uint64 // cumulative read+write bytes (/proc/<pid>/io)
	HasIO    bool   // false when /proc/<pid>/io was unreadable
}

// ProcSampler lists the processes matching a selector and reads each one's
// current counters. Injected so the process watch can be tested without /proc;
// nil uses the host /proc implementation.
type ProcSampler interface {
	Sample(match ProcMatch) []ProcInfo
}

// procCond is the set of per-process conditions a process watch evaluates. An
// empty set is invalid (rejected at build time). All present conditions must
// hold for a PID to fire (AND).
type procCond struct {
	minAge   time.Duration // process must have been observed alive at least this long
	cpuOp    string        // CPU% threshold ("" = none)
	cpuValue float64
	memOp    string // RSS-bytes threshold ("" = none)
	memValue float64
	ioOp     string // IO bytes/sec threshold ("" = none)
	ioValue  float64
	onGone   bool // fire when a previously-seen matching PID disappears
}

func (c procCond) any() bool {
	return c.hasPresence() || c.onGone
}

// hasPresence reports whether any condition fires while a process is present (as
// opposed to onGone, which fires on disappearance). A watch with only onGone must
// never fire merely because a matching process exists.
func (c procCond) hasPresence() bool {
	return c.minAge > 0 || c.cpuOp != "" || c.memOp != "" || c.ioOp != ""
}

// procState is the remembered per-PID data across cycles: when we first saw it
// (for age), the previous CPU/IO counters (for rates), and the edge state.
type procState struct {
	firstSeen time.Time
	prevCPU   uint64
	prevIO    uint64
	prevAt    time.Time
	hadIO     bool
	fired     bool // previous cycle's predicate, for edge detection
}

// procWatcher monitors the processes matching a name for a minimum age and/or
// CPU/memory/IO thresholds, firing the hook once per matching PID when its
// conditions are newly met (edge-triggered) — one event and one hook per PID.
type procWatcher struct {
	name      string
	match     ProcMatch
	cond      procCond
	hook      HookSpec
	notifiers []notify.Notifier
	dryRun    bool
	runner    HookRunner
	now       func() time.Time
	emit      func(Event)
	sampler   ProcSampler

	state map[int]*procState
}

func (w *procWatcher) runCycle(ctx context.Context) {
	if w.state == nil {
		w.state = map[int]*procState{}
	}
	now := w.now
	if now == nil {
		now = time.Now
	}
	sampler := w.sampler
	if sampler == nil {
		sampler = osProcSampler{}
	}

	samples := sampler.Sample(w.match)
	sort.Slice(samples, func(i, j int) bool { return samples[i].PID < samples[j].PID })

	t := now()
	seen := make(map[int]bool, len(samples))
	for _, s := range samples {
		if ctx.Err() != nil {
			return
		}
		seen[s.PID] = true
		st, known := w.state[s.PID]
		if !known {
			st = &procState{firstSeen: t}
			w.state[s.PID] = st
		}

		fire, env, msg := w.evaluate(st, t, s)
		if fire && !st.fired {
			w.fire(ctx, msg, env)
		}
		st.fired = fire
		// Remember this sample for next cycle's rate computation.
		st.prevCPU, st.prevIO, st.prevAt, st.hadIO = s.CPUTicks, s.IOBytes, t, s.HasIO
	}

	// Processes that vanished: fire `gone` (if configured) once per PID, then drop
	// their state — which also re-arms a reused PID.
	var gone []int
	for pid := range w.state {
		if !seen[pid] {
			gone = append(gone, pid)
		}
	}
	sort.Ints(gone)
	for _, pid := range gone {
		if ctx.Err() != nil {
			return
		}
		if w.cond.onGone {
			st := w.state[pid]
			env := map[string]string{
				"SERMO_PID":         strconv.Itoa(pid),
				"SERMO_PROCESS":     w.match.Name,
				"SERMO_CHANGE":      "gone",
				"SERMO_AGE_SECONDS": strconv.FormatInt(int64(t.Sub(st.firstSeen).Seconds()), 10),
			}
			if w.match.User != "" {
				env["SERMO_USER"] = w.match.User
			}
			w.fire(ctx, fmt.Sprintf("%s pid %d is gone", w.match.Name, pid), env)
		}
		delete(w.state, pid)
	}
}

func procSamplerFromDeps(deps Deps) ProcSampler {
	if deps.ProcSampler != nil {
		return deps.ProcSampler
	}
	return osProcSampler{userLookup: deps.UserLookup}
}

// evaluate computes whether a PID satisfies every configured condition this
// cycle, returning the firing decision, the hook environment and a message.
func (w *procWatcher) evaluate(st *procState, now time.Time, s ProcInfo) (bool, map[string]string, string) {
	c := w.cond
	age := now.Sub(st.firstSeen)

	env := map[string]string{
		"SERMO_PID":         strconv.Itoa(s.PID),
		"SERMO_PROCESS":     w.match.Name,
		"SERMO_CHANGE":      "threshold",
		"SERMO_AGE_SECONDS": strconv.FormatInt(int64(age.Seconds()), 10),
		"SERMO_MEMORY":      strconv.FormatUint(s.RSS, 10),
	}
	if w.match.User != "" {
		env["SERMO_USER"] = w.match.User
	}

	// A watch with only `gone` never fires on presence.
	ok := c.hasPresence()
	if c.minAge > 0 && age < c.minAge {
		ok = false
	}
	if c.memOp != "" && !cfgval.CompareFloat(float64(s.RSS), c.memOp, c.memValue) {
		ok = false
	}

	if cpuPct, ready := cpuPercent(st.prevCPU, s.CPUTicks, st.prevAt, now); ready {
		env["SERMO_CPU"] = strconv.FormatFloat(cpuPct, 'f', 2, 64)
		if c.cpuOp != "" && !cfgval.CompareFloat(cpuPct, c.cpuOp, c.cpuValue) {
			ok = false
		}
	} else if c.cpuOp != "" {
		ok = false // no rate yet (first cycle for this PID): cannot fire on CPU
	}

	if ioRate, ready := ioBytesPerSec(st.prevIO, s.IOBytes, st.hadIO, s.HasIO, st.prevAt, now); ready {
		env["SERMO_IO"] = strconv.FormatFloat(ioRate, 'f', 0, 64)
		if c.ioOp != "" && !cfgval.CompareFloat(ioRate, c.ioOp, c.ioValue) {
			ok = false
		}
	} else if c.ioOp != "" {
		ok = false
	}

	msg := fmt.Sprintf("%s pid %d matches (age %ds, rss %d)", w.match.Name, s.PID, int64(age.Seconds()), s.RSS)
	return ok, env, msg
}

func (w *procWatcher) fire(ctx context.Context, msg string, env map[string]string) {
	env["SERMO_WATCH"] = w.name
	env["SERMO_CHECK_TYPE"] = "process"
	env["SERMO_MESSAGE"] = msg
	if w.dryRun {
		w.emitEvent(Event{Watch: w.name, Kind: "dry-run", Message: watchDryRunMessage(w.hook, w.notifiers, nil) + ": " + msg})
		return
	}

	if len(w.hook.Command) > 0 {
		runner := defaultHookRunner(w.runner)
		if err := w.hook.Run(ctx, runner, env); err != nil {
			w.emitEvent(Event{Watch: w.name, Kind: "hook-failed", Message: msg + ": " + err.Error()})
		} else {
			w.emitEvent(Event{Watch: w.name, Kind: "hook", Message: msg})
		}
	}
	dispatchNotify(ctx, w.notifiers, watchMessage(w.name, msg, env), w.name, w.emitEvent)
}

func (w *procWatcher) emitEvent(e Event) {
	if w.emit != nil {
		w.emit(e)
	}
}

// cpuPercent derives a process's CPU% from two tick samples: Δticks/hz over the
// elapsed wall time across all CPUs. Not ready without a previous sample, and a
// counter that went backwards (exec/PID reuse) is treated as not ready.
func cpuPercent(prevTicks, curTicks uint64, prevAt, now time.Time) (float64, bool) {
	if prevAt.IsZero() || curTicks < prevTicks {
		return 0, false
	}
	wall := now.Sub(prevAt).Seconds()
	n := runtime.NumCPU()
	if wall <= 0 || n <= 0 {
		return 0, false
	}
	secs := float64(curTicks-prevTicks) / procClockTicks
	return secs / (wall * float64(n)) * 100, true
}

// ioBytesPerSec derives a process's IO rate from two cumulative byte samples.
func ioBytesPerSec(prevIO, curIO uint64, hadIO, hasIO bool, prevAt, now time.Time) (float64, bool) {
	if !hadIO || !hasIO || prevAt.IsZero() || curIO < prevIO {
		return 0, false
	}
	wall := now.Sub(prevAt).Seconds()
	if wall <= 0 {
		return 0, false
	}
	return float64(curIO-prevIO) / wall, true
}

// osProcSampler reads matching processes and their counters from the host /proc.
type osProcSampler struct {
	userLookup *process.UserLookup
}

func (s osProcSampler) Sample(m ProcMatch) []ProcInfo {
	lookup := s.userLookup
	if lookup == nil {
		lookup = process.DefaultUserLookup()
	}
	pr := process.OSReader{LookupUserName: lookup.Username}
	pids, err := pr.PIDs()
	if err != nil {
		return nil
	}
	mr := metrics.OSReader{}

	var out []ProcInfo
	for _, pid := range pids {
		id, ok := pr.Identity(pid)
		if !ok || !procMatchesWithLookup(m, id, lookup) {
			continue
		}
		info := ProcInfo{PID: pid}
		if v, ok := mr.ProcessCPU(pid); ok {
			info.CPUTicks = v
		}
		if v, ok := mr.ProcessRSS(pid); ok {
			info.RSS = v
		}
		if v, ok := readProcIO(pid); ok {
			info.IOBytes, info.HasIO = v, true
		}
		out = append(out, info)
	}
	return out
}

// procMatches reports whether a process matches the selector: its name against
// the resolved exe (full path or basename) and, if set, the owning user.
func procMatches(m ProcMatch, id process.Identity) bool {
	return procMatchesWithLookup(m, id, nil)
}

func procMatchesWithLookup(m ProcMatch, id process.Identity, lookup *process.UserLookup) bool {
	if m.Name != "" {
		if !id.ExeOK || (m.Name != id.Exe && m.Name != filepath.Base(id.Exe)) {
			return false
		}
	}
	if m.User != "" && m.User != id.User {
		if lookup == nil {
			return false
		}
		uid, ok := lookup.ResolveUser(m.User)
		if !ok || uid != id.UID {
			return false
		}
	}
	return m.Name != "" || m.User != ""
}

// readProcIO sums read_bytes and write_bytes from /proc/<pid>/io (the bytes that
// actually hit storage). Unreadable (permission or unsupported) yields ok=false.
func readProcIO(pid int) (uint64, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/io")
	if err != nil {
		return 0, false
	}
	return parseProcIO(string(data))
}

func parseProcIO(data string) (uint64, bool) {
	var read, write uint64
	var haveRead, haveWrite bool
	for _, line := range strings.Split(data, "\n") {
		if v, ok := strings.CutPrefix(line, "read_bytes:"); ok {
			parsed, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return 0, false
			}
			read = parsed
			haveRead = true
		} else if v, ok := strings.CutPrefix(line, "write_bytes:"); ok {
			parsed, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return 0, false
			}
			write = parsed
			haveWrite = true
		}
	}
	if !haveRead || !haveWrite {
		return 0, false
	}
	return read + write, true
}
