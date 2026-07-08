package app

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"syscall"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/metrics"
	"sermo/internal/notify"
	"sermo/internal/process"
)

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
	User     string
	UID      uint32
	Exe      string
	ExeOK    bool
	CPUTicks uint64 // accumulated CPU jiffies (utime+stime)
	RSS      uint64 // resident memory bytes
	IOBytes  uint64 // cumulative read+write bytes (/proc/<pid>/io)
	HasIO    bool   // false when /proc/<pid>/io was unreadable
}

// ProcSampler lists the processes matching a selector and reads each one's
// current counters. Injected so the process watch can be tested without /proc;
// nil uses the host /proc implementation. The bool reports whether the process
// list could be read at all: false signals a transient failure (e.g. /proc
// unreadable), which must be distinguished from "no process matched" so the
// watch does not mistake a read error for every tracked PID disappearing.
type ProcSampler interface {
	Sample(match ProcMatch) ([]ProcInfo, bool)
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

const (
	procChangeGone      = "gone"
	procChangeThreshold = "threshold"
)

// killSpec is a process watch's `then.kill` action: signal the matched PID with
// the native process signaller (process.OSSignaler). escalate follows the first
// signal with SIGKILL for a survivor after termTimeout — the same TERM→KILL model
// the stop policy uses, reusing process.Wait for the (cancellable) grace period.
type killSpec struct {
	signal      syscall.Signal // first signal to send (default SIGTERM)
	escalate    bool           // follow up with SIGKILL if the PID survives
	termTimeout time.Duration  // grace before the escalated SIGKILL
	killTimeout time.Duration  // grace after the escalated SIGKILL (verification)
	selector    process.KillSelector
}

// procWatcher monitors the processes matching a name for a minimum age and/or
// CPU/memory/IO thresholds, firing the hook once per matching PID when its
// conditions are newly met (edge-triggered) — one event and one hook per PID.
type procWatcher struct {
	name      string
	match     ProcMatch
	cond      procCond
	hook      HookSpec
	kill      *killSpec
	notifiers []notify.Notifier
	dryRun    bool
	inPanic   func() bool
	runner    HookRunner
	signaler  process.Signaler     // nil -> process.OSSignaler{} (real kill(2))
	resolve   process.UserResolver // nil -> process.DefaultUserLookup().ResolveUser
	sleep     func(time.Duration)  // nil -> process.Wait's cancellable timer
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

	samples, ok := sampler.Sample(w.match)
	if !ok {
		// Could not read the process list this cycle (transient /proc failure).
		// Treating the empty result as "all matching PIDs vanished" would fire a
		// spurious `gone` for every tracked PID and discard their state; instead
		// keep the previous state untouched and retry next cycle.
		return
	}
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
		if fire && !st.fired && !observeOnlyCycle(ctx) {
			w.fire(ctx, s, msg, env)
		}
		if !observeOnlyCycle(ctx) {
			st.fired = fire
		}
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
		if w.cond.onGone && !observeOnlyCycle(ctx) {
			st := w.state[pid]
			env := map[string]string{
				sermoEnvPID:        strconv.Itoa(pid),
				sermoEnvProcess:    w.match.Name,
				sermoEnvChange:     procChangeGone,
				sermoEnvAgeSeconds: strconv.FormatInt(int64(t.Sub(st.firstSeen).Seconds()), envFormatBase),
			}
			if w.match.User != "" {
				env[sermoEnvUser] = w.match.User
			}
			w.fire(ctx, ProcInfo{PID: pid}, fmt.Sprintf("%s pid %d is gone", w.match.Name, pid), env)
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
		sermoEnvPID:        strconv.Itoa(s.PID),
		sermoEnvProcess:    w.match.Name,
		sermoEnvChange:     procChangeThreshold,
		sermoEnvAgeSeconds: strconv.FormatInt(int64(age.Seconds()), envFormatBase),
		sermoEnvMemory:     strconv.FormatUint(s.RSS, envFormatBase),
	}
	if w.match.User != "" {
		env[sermoEnvUser] = w.match.User
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
		env[sermoEnvCPU] = strconv.FormatFloat(cpuPct, envFloatFormat, procWatchCPUPrecision, envFloatBits)
		if c.cpuOp != "" && !cfgval.CompareFloat(cpuPct, c.cpuOp, c.cpuValue) {
			ok = false
		}
	} else if c.cpuOp != "" {
		ok = false // no rate yet (first cycle for this PID): cannot fire on CPU
	}

	if ioRate, ready := ioBytesPerSec(st.prevIO, s.IOBytes, st.hadIO, s.HasIO, st.prevAt, now); ready {
		env[sermoEnvIO] = strconv.FormatFloat(ioRate, envFloatFormat, procWatchIOPrecision, envFloatBits)
		if c.ioOp != "" && !cfgval.CompareFloat(ioRate, c.ioOp, c.ioValue) {
			ok = false
		}
	} else if c.ioOp != "" {
		ok = false
	}

	msg := fmt.Sprintf("%s pid %d matches (age %ds, rss %d)", w.match.Name, s.PID, int64(age.Seconds()), s.RSS)
	return ok, env, msg
}

func (w *procWatcher) fire(ctx context.Context, info ProcInfo, msg string, env map[string]string) {
	env[sermoEnvWatch] = w.name
	env[sermoEnvCheckType] = checks.CheckTypeProcess
	env[sermoEnvMessage] = msg
	// The kill action only applies to a presence fire (a matched, still-present
	// PID); a `gone` fire has nothing to signal.
	killable := w.kill != nil && env[sermoEnvChange] == procChangeThreshold
	if w.dryRun {
		w.emitEvent(Event{Watch: w.name, Kind: eventKindDryRun, Message: w.dryRunActions(killable) + ": " + msg})
		dispatchDryRunNotify(ctx, w.notifiers, watchMessage(w.name, msg, env), w.name, w.emitEvent)
		return
	}
	if w.inPanic != nil && w.inPanic() {
		w.emitEvent(Event{Watch: w.name, Kind: eventKindPanicSuppressed, Message: "panic mode: hook/notify/kill suppressed: " + msg})
		return
	}

	if len(w.hook.Command) > 0 {
		runner := defaultHookRunner(w.runner)
		if err := w.hook.Run(ctx, runner, env); err != nil {
			w.emitEvent(Event{Watch: w.name, Kind: eventKindHookFail, Message: msg + ": " + err.Error()})
		} else {
			w.emitEvent(Event{Watch: w.name, Kind: eventKindHook, Message: msg})
		}
	}
	if killable {
		w.doKill(ctx, info, msg)
	}
	dispatchNotify(ctx, w.notifiers, watchMessage(w.name, msg, env), w.name, w.emitEvent)
}

// dryRunActions describes the actions the watch would take, including the native
// kill when it applies to this fire — the process-watch analogue of
// watchDryRunMessage (which does not know about kill).
func (w *procWatcher) dryRunActions(killable bool) string {
	base := watchDryRunMessage(w.hook, w.notifiers, nil)
	if !killable {
		return base
	}
	if base == "dry-run: no configured watch actions" {
		return "dry-run: would run kill"
	}
	return base + ", kill"
}

// doKill signals a matched PID through process.Reaper and process.KillSelector.
// With escalate it follows the first signal with SIGKILL after termTimeout,
// re-verifying the PID's identity first so a recycled PID is never killed.
func (w *procWatcher) doKill(ctx context.Context, info ProcInfo, msg string) {
	first := w.reaper().Signal(ctx, []process.Process{info.asProcess()}, w.kill.selector, w.kill.signal)
	if !w.emitSignalResult(msg, w.kill.signal, first) {
		return
	}

	if !w.kill.escalate || w.kill.signal == syscall.SIGKILL {
		return
	}
	// Wait out the grace period (cancellable), then re-verify the PID still
	// matches this watch before escalating — over the wait it may have exited and
	// the number been reused by an unrelated process.
	if err := process.Wait(ctx, w.sleep, w.kill.termTimeout); err != nil {
		return
	}
	current, ok := w.matchingProcess(info.PID)
	if !ok {
		return
	}
	kill := w.reaper().Signal(ctx, []process.Process{current}, w.kill.selector, syscall.SIGKILL)
	if !w.emitSignalResult(msg, syscall.SIGKILL, kill) {
		return
	}
	// After the kill grace, a PID that still matches is unkillable from here (an
	// uninterruptible sleep, or a zombie whose parent has not reaped it) — surface
	// it rather than claim success, mirroring the reaper's final rediscover.
	if err := process.Wait(ctx, w.sleep, w.kill.killTimeout); err != nil {
		return
	}
	if _, ok := w.matchingProcess(info.PID); ok {
		w.emitEvent(Event{Watch: w.name, Kind: eventKindKillFailed, Message: fmt.Sprintf("%s: pid %d survived SIGKILL", msg, info.PID)})
	}
}

func (w *procWatcher) reaper() process.Reaper {
	return process.Reaper{
		Signaler:    w.signaler,
		ResolveUser: w.resolve,
		Sleep:       w.sleep,
	}
}

func (w *procWatcher) emitSignalResult(msg string, sig syscall.Signal, result process.ReapResult) bool {
	if len(result.Signalled) > 0 {
		pid := result.Signalled[0]
		if sig == syscall.SIGKILL && w.kill.signal != syscall.SIGKILL {
			w.emitEvent(Event{Watch: w.name, Kind: eventKindKill, Message: fmt.Sprintf("%s: escalated to SIGKILL for pid %d", msg, pid)})
		} else {
			w.emitEvent(Event{Watch: w.name, Kind: eventKindKill, Message: fmt.Sprintf("%s: sent %s to pid %d", msg, sigName(sig), pid)})
		}
		return true
	}
	if len(result.Failed) > 0 {
		failure := result.Failed[0]
		w.emitEvent(Event{Watch: w.name, Kind: eventKindKillFailed, Message: fmt.Sprintf("%s: %s pid %d: %v", msg, sigName(sig), failure.PID, failure.Err)})
		return false
	}
	w.emitEvent(Event{Watch: w.name, Kind: eventKindKillFailed, Message: fmt.Sprintf("%s: pid did not match kill selector", msg)})
	return false
}

// matchingProcess re-samples the watch's selector and reports whether pid is
// still among the matches — the identity re-check that defends the escalated
// SIGKILL against PID reuse. A transient sampling failure fails safe (no kill).
func (w *procWatcher) matchingProcess(pid int) (process.Process, bool) {
	sampler := w.sampler
	if sampler == nil {
		sampler = osProcSampler{}
	}
	samples, ok := sampler.Sample(w.match)
	if !ok {
		return process.Process{}, false
	}
	for _, s := range samples {
		if s.PID == pid {
			return s.asProcess(), true
		}
	}
	return process.Process{}, false
}

func (s ProcInfo) asProcess() process.Process {
	return process.Process{
		PID:   s.PID,
		User:  s.User,
		UID:   s.UID,
		Exe:   s.Exe,
		ExeOK: s.ExeOK,
	}
}

// sigName renders the termination signals a kill action can send with their
// conventional names (Signal.String() would say "terminated"/"killed").
func sigName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGKILL:
		return "SIGKILL"
	default:
		return sig.String()
	}
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
	secs := float64(curTicks-prevTicks) / metrics.LinuxClockTicks
	return secs / (wall * float64(n)) * metrics.PercentScale, true
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

func (s osProcSampler) Sample(m ProcMatch) ([]ProcInfo, bool) {
	lookup := s.userLookup
	if lookup == nil {
		lookup = process.DefaultUserLookup()
	}
	pr := process.OSReader{LookupUserName: lookup.Username}
	pids, err := pr.PIDs()
	if err != nil {
		return nil, false
	}
	mr := metrics.OSReader{}

	var out []ProcInfo
	for _, pid := range pids {
		id, ok := pr.Identity(pid)
		if !ok || !procMatchesWithLookup(m, id, lookup) {
			continue
		}
		info := ProcInfo{PID: pid, User: id.User, UID: id.UID, Exe: id.Exe, ExeOK: id.ExeOK}
		if v, ok := mr.ProcessCPU(pid); ok {
			info.CPUTicks = v
		}
		if v, ok := mr.ProcessRSS(pid); ok {
			info.RSS = v
		}
		if read, write, ok := mr.ProcessIO(pid); ok {
			info.IOBytes, info.HasIO = read+write, true
		}
		out = append(out, info)
	}
	return out, true
}

// procMatchesWithLookup reports whether a process matches the selector: its name
// against the resolved exe (full path or basename) and, if set, the owning user.
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
