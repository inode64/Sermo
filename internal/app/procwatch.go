package app

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/metrics"
	"sermo/internal/notify"
	"sermo/internal/process"
	"sermo/internal/severity"
	"sermo/internal/units"
)

// ProcMatch selects which processes a process watch tracks: by name (the exe
// basename or its full resolved path) and optionally the owning user. All
// selects every process on the host; a watch that evaluates the whole process
// table sets it explicitly, so an empty selector keeps matching nothing.
type ProcMatch struct {
	Name string
	User string
	All  bool
	PID  int // > 0: only this process, whatever its name or user; for re-verifying one PID
}

// ProcInfo is one matched process's current resource counters. CPU and IO are
// cumulative; the watch derives rates from successive samples.
type ProcInfo struct {
	process.Identity
	CPUTicks uint64 // accumulated CPU jiffies (utime+stime)
	RSS      uint64 // resident memory bytes
	IOBytes  uint64 // cumulative read+write bytes (/proc/<pid>/io)
	HasIO    bool   // false when /proc/<pid>/io was unreadable
	// StartTime is when the process itself started (/proc/<pid>/stat field 22
	// against the boot time), so `for` measures the process's own age rather than
	// how long this daemon has watched it. Zero when it was unreadable.
	StartTime time.Time
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
	minAge   time.Duration // process must have been alive at least this long (see procState.age)
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

// procState is the remembered per-PID data across cycles: the process's own
// start time and when we first saw it (both for age), the previous CPU/IO
// counters (for rates), and the edge state.
type procState struct {
	startTime  time.Time // the process's own start time; zero when unreadable
	startTicks uint64    // the same instant in boot-clock ticks: the PID's identity
	firstSeen  time.Time
	prevCPU    uint64
	prevIO     uint64
	prevAt     time.Time
	hadIO      bool
	fired      bool // previous cycle's predicate, for edge detection
}

// age is how long the process has been alive: measured from its own start time
// (/proc/<pid>/stat), falling back to the daemon's first observation only when
// that is unreadable. Wall-clock steps backwards (NTP, a manual clock set) can
// put "now" before the start time; report zero rather than a negative age.
func (st *procState) age(now time.Time) time.Duration {
	base := st.startTime
	if base.IsZero() {
		base = st.firstSeen
	}
	if d := now.Sub(base); d > 0 {
		return d
	}
	return 0
}

// supersededBy reports whether this remembered state cannot be attributed to the
// process currently holding the PID. The kernel recycles PIDs, and a PID replaced
// between two cycles is never seen as gone, so without this check the successor
// would inherit its predecessor's age, rate baseline and edge state.
//
// A sample that could not read the start time identifies nothing, so it never
// supersedes anything — a transient /proc failure must not reset a healthy PID.
// Every other mismatch does, including a state that has no identity yet: that
// state was built from a sample which failed the same read, so there is no
// evidence it belongs to this process either.
//
// It compares ticks rather than StartTime on purpose: StartTime is derived from
// the boot time, which the kernel re-anchors on a clock step, so comparing it
// would declare every tracked PID superseded the moment chronyd (or a `makestep`
// watch) moves the clock — dropping the edge state of every one of them.
func (st *procState) supersededBy(s ProcInfo) bool {
	if s.StartTicks == 0 {
		return false
	}
	return st.startTicks != s.StartTicks
}

// sameProcessAs reports whether two samples of one PID describe the same process.
// A start time neither side could read proves nothing, so the answer is yes: the
// caller then has only the name/user match, exactly as before start times existed.
func (s ProcInfo) sameProcessAs(other ProcInfo) bool {
	return s.StartTicks == 0 || other.StartTicks == 0 || s.StartTicks == other.StartTicks
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
	name string
	// severity grades every fire this watch reports.
	severity  severity.Level
	match     ProcMatch
	cond      procCond
	summary   string
	check     map[string]any
	hook      HookSpec
	kill      *killSpec
	killer    pidKiller // the shared signal path then.kill runs through
	notifiers []notify.Notifier
	dryRun    bool
	inPanic   func() bool
	runner    HookRunner
	now       func() time.Time
	emit      func(Event)
	sampler   ProcSampler
	publish   func(string, string, checks.Result)

	state map[int]*procState
}

func (w *procWatcher) runCycle(ctx context.Context) {
	if w.state == nil {
		w.state = map[int]*procState{}
	}
	now := clockOrNow(w.now)

	samples, ok := w.sampler.Sample(w.match)
	if !ok {
		// Could not read the process list this cycle (transient /proc failure).
		// Treating the empty result as "all matching PIDs vanished" would fire a
		// spurious `gone` for every tracked PID and discard their state; instead
		// keep the previous state untouched and retry next cycle.
		w.publishSnapshot(nil, false)
		return
	}
	slices.SortFunc(samples, func(a, b ProcInfo) int { return cmp.Compare(a.PID, b.PID) })
	defer w.publishSnapshot(samples, true)

	t := now()
	seen := make(map[int]bool, len(samples))
	for i := range samples {
		if ctx.Err() != nil {
			return
		}
		seen[samples[i].PID] = true
		st := w.state[samples[i].PID]
		if st != nil && st.supersededBy(samples[i]) {
			// Another process now holds this PID. The one we tracked is gone even
			// though the number is still in use, and the sweep below only sees PIDs
			// absent from the sample — so report it here, before dropping its state.
			w.fireGone(ctx, samples[i].PID, st, t)
			st = nil
		}
		if st == nil {
			st = &procState{firstSeen: t}
			w.state[samples[i].PID] = st
		}
		// Adopt each start reading on the first sample that carries it: a transient
		// /proc read failure must not pin this PID to the fallback age, nor leave it
		// without the identity that detects PID reuse.
		if st.startTime.IsZero() {
			st.startTime = samples[i].StartTime
		}
		if st.startTicks == 0 {
			st.startTicks = samples[i].StartTicks
		}

		fire, env, msg := w.evaluate(st, t, samples[i])
		if fire && !st.fired && !observeOnlyCycle(ctx) {
			w.fire(ctx, samples[i], msg, env)
		}
		if !observeOnlyCycle(ctx) {
			st.fired = fire
		}
		// Remember this sample for next cycle's rate computation.
		st.prevCPU, st.prevIO, st.prevAt, st.hadIO = samples[i].CPUTicks, samples[i].IOBytes, t, samples[i].HasIO
	}

	// Processes that vanished: fire `gone` (if configured) once per PID, then drop
	// their state — which also re-arms a reused PID.
	var gone []int
	for _, pid := range slices.Sorted(maps.Keys(w.state)) {
		if !seen[pid] {
			gone = append(gone, pid)
		}
	}
	for _, pid := range gone {
		if ctx.Err() != nil {
			return
		}
		if st := w.state[pid]; st != nil {
			w.fireGone(ctx, pid, st, t)
		}
		delete(w.state, pid)
	}
}

// fireGone reports a tracked PID's disappearance, whether it vanished from the
// sample or was replaced by another process holding the same number.
func (w *procWatcher) fireGone(ctx context.Context, pid int, st *procState, t time.Time) {
	if !w.cond.onGone || observeOnlyCycle(ctx) {
		return
	}
	env := w.procEnv(pid, procChangeGone, st.age(t))
	w.fire(ctx, ProcInfo{PID: pid}, fmt.Sprintf("%s pid %d is gone", w.match.Name, pid), env)
}

func (w *procWatcher) publishSnapshot(samples []ProcInfo, ok bool) {
	if w.publish == nil {
		return
	}
	if !ok {
		w.publish(w.name, checks.CheckTypeProcess, checks.Result{
			Check:   w.name,
			OK:      false,
			Message: "process " + w.match.Name + ": sample unavailable",
			Data:    map[string]any{watchReadingFieldProcess: w.match.Name},
		})
		return
	}
	data := processWatchData(w.match.Name, w.match.User, samples)
	target := "process " + w.match.Name
	if w.match.User != "" {
		target += " user " + w.match.User
	}
	summary := fmt.Sprintf("%s: %d matching process%s", target, len(samples), pluralSuffix(len(samples), "process"))
	if len(samples) > 0 {
		if rss, ok := data[watchReadingFieldRSS].(uint64); ok {
			summary += ", rss " + checks.HumanizeSignedBytes(uintToInt64(rss))
		}
	}
	result := checks.Result{
		Check:   w.name,
		OK:      true,
		Message: summary,
		Data:    data,
	}
	w.publish(w.name, checks.CheckTypeProcess, checks.ApplySummary(w.summary, w.check, result))
}

// processWatchData is the persisted reading data for one process-watch sample,
// shared by the daemon cycle snapshot and the live watch view.
func processWatchData(name, user string, samples []ProcInfo) map[string]any {
	var rssTotal, cpuTicksTotal, ioTotal uint64
	ioKnown := false
	for i := range samples {
		rssTotal += samples[i].RSS
		cpuTicksTotal += samples[i].CPUTicks
		if samples[i].HasIO {
			ioKnown = true
			ioTotal += samples[i].IOBytes
		}
	}
	data := map[string]any{
		watchReadingFieldProcess:  name,
		watchReadingFieldMatches:  len(samples),
		checks.DataKeyPIDs:        processPIDList(samples),
		watchReadingFieldRSS:      rssTotal,
		watchReadingFieldCPUTicks: cpuTicksTotal,
	}
	if user != "" {
		data[watchReadingFieldUser] = user
	}
	if ioKnown {
		data[metrics.MetricIO] = ioTotal
	}
	return data
}

// procSamplerFromDeps resolves optional dependencies once at construction.
func procSamplerFromDeps(deps Deps) ProcSampler {
	if deps.ProcSampler != nil {
		return deps.ProcSampler
	}
	lookup := deps.UserLookup
	if lookup == nil {
		lookup = process.DefaultUserLookup()
	}
	return osProcSampler{userLookup: lookup}
}

// procEnv is the hook environment both firing paths share — a presence threshold
// and a `gone` disappearance — so the identity and age they report cannot drift
// apart. The presence path adds its own reading-derived variables on top.
func (w *procWatcher) procEnv(pid int, change string, age time.Duration) map[string]any {
	env := map[string]any{
		sermoEnvPID:        strconv.Itoa(pid),
		sermoEnvProcess:    w.match.Name,
		sermoEnvChange:     change,
		sermoEnvAgeSeconds: age,
	}
	if w.match.User != "" {
		env[sermoEnvUser] = w.match.User
	}
	return env
}

// evaluate computes whether a PID satisfies every configured condition this
// cycle, returning the firing decision, the hook environment and a message.
func (w *procWatcher) evaluate(st *procState, now time.Time, s ProcInfo) (bool, map[string]any, string) {
	c := w.cond
	age := st.age(now)

	env := w.procEnv(s.PID, procChangeThreshold, age)
	env[sermoEnvMemory] = s.RSS

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

	msg := fmt.Sprintf("%s pid %d matches (age %s, rss %s)", w.match.Name, s.PID, units.HumanizeDuration(age), checks.HumanizeSignedBytes(uintToInt64(s.RSS)))
	return ok, env, msg
}

func (w *procWatcher) fire(ctx context.Context, info ProcInfo, msg string, values map[string]any) {
	msg = w.summaryMessage(info, msg, values)
	env := map[string]string{}
	watchValuesEnv(env, values)
	env[sermoEnvWatch] = w.name
	env[sermoEnvCheckType] = checks.CheckTypeProcess
	env[sermoEnvMessage] = msg
	// The kill action only applies to a presence fire (a matched, still-present
	// PID); a `gone` fire has nothing to signal.
	killable := w.kill != nil && env[sermoEnvChange] == procChangeThreshold
	spec := watchFireSpec{
		name:        w.name,
		hook:        w.hook,
		runner:      w.runner,
		notifiers:   w.notifiers,
		inPanic:     w.inPanic,
		dryRun:      w.dryRun,
		emit:        w.emitEvent,
		dryRunLabel: w.dryRunActions(killable),
		panicLabel:  "panic mode: hook/notify/kill suppressed",
		severity:    w.severity,
	}
	if killable {
		spec.action = func() { w.doKill(ctx, info, msg) }
	}
	dispatchWatchFire(ctx, spec, msg, env)
}

func (w *procWatcher) summaryMessage(info ProcInfo, message string, env map[string]any) string {
	if w.summary == "" {
		return message
	}
	data := map[string]any{
		checks.DataKeyPID:        info.PID,
		checks.DataKeyTrigger:    env[sermoEnvChange],
		watchReadingFieldProcess: w.match.Name,
	}
	addSummaryAge(data, env)
	for _, field := range []struct {
		envKey  string
		dataKey string
	}{
		{sermoEnvCPU, "cpu"},
		{sermoEnvMemory, "memory"},
		{sermoEnvIO, "io"},
	} {
		if raw, ok := env[field.envKey]; ok {
			value, ok := cfgval.Float(raw)
			// CPU and IO retain the decimal precision already exposed to hooks.
			if text, formatted := raw.(string); formatted {
				var err error
				value, err = strconv.ParseFloat(text, envFloatBits)
				ok = err == nil
			}
			if ok {
				data[field.dataKey] = value
				data[checks.DataKeyValue] = value
			}
		}
	}
	return checks.ApplySummary(w.summary, w.check, checks.Result{Check: w.name, Message: message, Data: data}).Message
}

// dryRunActions describes the actions the watch would take, including the native
// kill when it applies to this fire — the process-watch analogue of
// watchDryRunMessage (which does not know about kill).
func (w *procWatcher) dryRunActions(killable bool) string {
	if !killable {
		return watchDryRunMessage(w.hook, w.notifiers)
	}
	return watchDryRunMessage(w.hook, w.notifiers, config.WatchThenKeyKill)
}

// doKill signals a matched PID through the shared pidKiller: the first signal,
// then — with escalate — a re-verified SIGKILL after the grace period.
func (w *procWatcher) doKill(ctx context.Context, info ProcInfo, msg string) {
	w.killer.kill(ctx, killTarget{info: info, spec: *w.kill, resample: w.matchingProcess, msg: msg})
}

// matchingProcess re-samples the watch's selector and returns pid's current
// sample if it is still among the matches — the identity re-check that defends
// the escalated SIGKILL against PID reuse. It answers on name and user alone, so
// callers acting on the result must also compare the start time (see
// sameProcessAs). A transient sampling failure fails safe (no kill).
func (w *procWatcher) matchingProcess(pid int) (ProcInfo, bool) {
	samples, ok := w.sampler.Sample(w.match)
	if !ok {
		return ProcInfo{}, false
	}
	for i := range samples {
		if samples[i].PID == pid {
			return samples[i], true
		}
	}
	return ProcInfo{}, false
}

func (s ProcInfo) asProcess() process.Process {
	// Host process watches do not establish service ownership for a deleted
	// executable, so they intentionally carry no ExeFile cleanup authority.
	return process.Process{
		PID:        s.PID,
		StartTicks: s.StartTicks,
		User:       s.User,
		UID:        s.UID,
		Exe:        s.Exe,
		ExeOK:      s.ExeOK,
		ExePrev:    s.ExePrev,
		Cmdline:    s.Cmdline,
		Cgroup:     s.Cgroup,
	}
}

func (w *procWatcher) emitEvent(e Event) { emitSafe(w.emit, e) }

// cpuPercent derives a process's CPU% from two tick samples: Δticks/hz over the
// elapsed wall time across all CPUs. Not ready without a previous sample, and a
// counter that went backwards (exec/PID reuse) is treated as not ready.
func cpuPercent(prevTicks, curTicks uint64, prevAt, now time.Time) (float64, bool) {
	if prevAt.IsZero() {
		return 0, false
	}
	// OSReader counts host CPUs from /proc/stat rather than this process's
	// affinity mask, which keeps a pinned sermod from inflating CPU percentages.
	n := (metrics.OSReader{}).NumCPU()
	return metrics.CPUPercent(prevTicks, curTicks, prevAt, now, metrics.LinuxClockTicks, n)
}

// ioBytesPerSec derives a process's IO rate from two cumulative byte samples.
func ioBytesPerSec(prevIO, curIO uint64, hadIO, hasIO bool, prevAt, now time.Time) (float64, bool) {
	if !hadIO || !hasIO || prevAt.IsZero() {
		return 0, false
	}
	return metrics.BytesPerSecond(prevIO, curIO, prevAt, now)
}

// osProcSampler reads matching processes and their counters from the host /proc.
type osProcSampler struct {
	userLookup *process.UserLookup
}

func (s osProcSampler) Sample(m ProcMatch) ([]ProcInfo, bool) {
	lookup := s.userLookup
	pr := process.OSReader{LookupUserName: lookup.Username}
	var pids []int
	if m.PID > 0 {
		pids = []int{m.PID}
	} else {
		var err error
		pids, err = pr.PIDs()
		if err != nil {
			return nil, false
		}
	}
	mr := metrics.OSReader{}

	var out []ProcInfo
	for _, pid := range pids {
		id, ok := pr.Identity(pid)
		if !ok || !procMatchesWithLookup(m, id, lookup) {
			continue
		}
		info := ProcInfo{Identity: id}
		if ticks, at, ok := mr.ProcessStart(pid); ok {
			info.StartTicks, info.StartTime = ticks, at
		}
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
// All matches every process, PID exactly one; without either an empty selector
// matches none.
func procMatchesWithLookup(m ProcMatch, id process.Identity, lookup *process.UserLookup) bool {
	if m.PID > 0 {
		return id.PID == m.PID
	}
	if m.All {
		return true
	}
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
