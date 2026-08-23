package checks

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/metrics"
	"sermo/internal/process"
	"sermo/internal/utmp"
)

const sshIdleDevRoot = "/dev"

// SSHProtectedProcessFieldSummary is the user-facing list of fields accepted
// by one protected_processes entry.
const SSHProtectedProcessFieldSummary = "exe, user and group"

// SSHProtectedProcessFields returns the fields accepted by one
// protected_processes entry. It returns an array copy so callers cannot alter
// the canonical check schema.
func SSHProtectedProcessFields() [3]string {
	return [3]string{CheckKeyExe, CheckKeyUser, CheckKeyGroup}
}

// IsSSHProtectedProcessField reports whether field is valid in one
// protected_processes entry.
func IsSSHProtectedProcessField(field string) bool {
	for _, candidate := range SSHProtectedProcessFields() {
		if field == candidate {
			return true
		}
	}
	return false
}

// SSHIdleSample is one observation of interactive SSH terminal sessions.
// Count excludes protected sessions; ProtectedCount stays available to guards
// that must retain a named account or long-running terminal job.
type SSHIdleSample struct {
	Count          int
	ProtectedCount int
	OldestIdle     time.Duration
}

// SSHIdleConfig configures one SSH terminal-idle observation.
type SSHIdleConfig struct {
	IdleFor            time.Duration
	SSHDExes           []string
	ProtectedProcesses []SSHProtectedProcess
}

// SSHSession is one interactive terminal proven to descend from a configured
// sshd executable. PID and StartTicks identify the per-session server process
// that may be closed; they stay zero when that boundary cannot be identified
// safely.
type SSHSession struct {
	User       string
	Terminal   string
	PID        int
	StartTicks uint64
	Idle       time.Duration
}

// SSHSessionIssue is one login terminal that the inventory could not attribute
// to a configured sshd identity safely. It is display-only and never carries a
// PID or start time that could authorize a close action.
type SSHSessionIssue struct {
	User     string
	Terminal string
	Message  string
}

// SSHSessionSample separates local console terminals from interactive SSH
// terminals. Issues retain terminals whose live ancestry cannot be attributed
// safely without hiding the verified sessions collected in the same sample.
type SSHSessionSample struct {
	Console int
	SSH     []SSHSession
	Issues  []SSHSessionIssue
}

// SSHSessionConfig identifies the trusted sshd processes for one service.
// Exact executable and real-user identity, rather than a process name,
// prevents a local terminal or an unrelated sshd instance from being mislabeled
// or made closable as an SSH session.
type SSHSessionConfig struct {
	SSHDFilters []process.IdentityFilter
}

// SSHProtectedProcess is a named terminal-scoped process filter. Its Filter
// never authorizes signalling; it only protects the SSH session owning the TTY.
type SSHProtectedProcess struct {
	Name   string
	Filter process.IdentityFilter
}

// SSHIdleSamplerFunc observes SSH terminals using the supplied configuration.
// It is injected by the daemon so all ssh_idle checks share one procfs snapshot
// during a cycle.
type SSHIdleSamplerFunc func(SSHIdleConfig) (SSHIdleSample, error)

// SSHSessionSamplerFunc reads current interactive console and SSH sessions.
// It is injectable so the dashboard and the SSH-idle check can share the same
// terminal-aware procfs cache during a daemon generation.
type SSHSessionSamplerFunc func(SSHSessionConfig) (SSHSessionSample, error)

// sshIdleCheck reports SSH sessions whose terminal has received no input for
// idle_for. It is condition-style: a satisfied predicate means the configured
// session state is present, not that SSH is unhealthy.
type sshIdleCheck struct {
	base
	preds   []levelPred
	config  SSHIdleConfig
	sampler SSHIdleSamplerFunc
}

func (c sshIdleCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start
	if err := ctx.Err(); err != nil {
		return c.unavailableResult(err, start)
	}
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultSSHIdleSampler()
	}
	sample, err := sampler(c.config)
	if err != nil {
		return c.unavailableResult(err, start)
	}
	if err := ctx.Err(); err != nil {
		return c.unavailableResult(err, start)
	}
	values := map[string]float64{
		DataKeyCount:             float64(sample.Count),
		DataKeyProtectedCount:    float64(sample.ProtectedCount),
		DataKeyOldestIdleSeconds: sample.OldestIdle.Seconds(),
	}
	res := c.result(levelPredsHold(c.preds, values), sshIdleMessage(sample), start)
	res.Data = map[string]any{
		DataKeyCount:             sample.Count,
		DataKeyProtectedCount:    sample.ProtectedCount,
		DataKeyOldestIdleSeconds: sample.OldestIdle.Seconds(),
		DataKeyValue:             float64(sample.Count),
		DataKeyUnit:              metrics.MetricUnitSessions,
	}
	return res
}

func (c sshIdleCheck) unavailableResult(err error, start time.Time) Result {
	res := c.base.unavailableResult("ssh idle: "+err.Error(), start)
	res.Data = map[string]any{DataKeySampleError: err.Error()}
	return res
}

func sshIdleMessage(sample SSHIdleSample) string {
	if sample.Count == 0 {
		return fmt.Sprintf("no idle SSH sessions (%d protected)", sample.ProtectedCount)
	}
	return fmt.Sprintf("%d idle SSH session(s), oldest idle %s (%d protected)",
		sample.Count, sample.OldestIdle.Round(time.Second), sample.ProtectedCount)
}

// NewSSHIdleSampler builds a native SSH-idle sampler. reader should normally be
// a process.CachingReader over an OSReader with ReadTTY enabled so configured
// checks share one complete terminal-aware process snapshot per daemon cycle.
func NewSSHIdleSampler(reader process.Reader, lookup *process.UserLookup) SSHIdleSamplerFunc {
	reader, lookup = terminalSessionDeps(reader, lookup)
	return newSSHIdleSampler(reader, lookup, utmp.Sessions, sshSessionTerminal, time.Now)
}

// NewSSHSessionSampler builds a native interactive-session sampler. reader
// should normally be the same terminal-aware caching reader used by
// NewSSHIdleSampler, keeping dashboard refreshes from rescanning procfs.
func NewSSHSessionSampler(reader process.Reader, lookup *process.UserLookup) SSHSessionSamplerFunc {
	reader, lookup = terminalSessionDeps(reader, lookup)
	return newSSHSessionSampler(reader, lookup, utmp.Sessions, sshSessionTerminal, time.Now)
}

func terminalSessionDeps(reader process.Reader, lookup *process.UserLookup) (process.Reader, *process.UserLookup) {
	if reader == nil {
		reader = process.NewCachingReader(process.OSReader{ReadTTY: true}, 0)
	}
	if lookup == nil {
		lookup = process.DefaultUserLookup()
	}
	return reader, lookup
}

func sshSessionTerminal(line string) (utmp.Terminal, error) {
	terminal, err := utmp.TerminalInfo(sshIdleDevRoot, line)
	if err != nil {
		return utmp.Terminal{}, fmt.Errorf("read terminal %s: %w", line, err)
	}
	return terminal, nil
}

func defaultSSHIdleSampler() SSHIdleSamplerFunc {
	return NewSSHIdleSampler(nil, nil)
}

func newSSHIdleSampler(reader process.Reader, lookup *process.UserLookup, sessions func() ([]utmp.Session, error), terminal func(string) (utmp.Terminal, error), now func() time.Time) SSHIdleSamplerFunc {
	return func(config SSHIdleConfig) (SSHIdleSample, error) {
		if config.IdleFor <= 0 {
			return SSHIdleSample{}, errors.New("idle_for must be positive")
		}
		sshdFilters, err := sshdFilters(config.SSHDExes)
		if err != nil {
			return SSHIdleSample{}, err
		}
		sessions, err := sessions()
		if err != nil {
			return SSHIdleSample{}, fmt.Errorf("load terminal sessions: %w", err)
		}
		snapshot, err := process.Snapshot(reader)
		if err != nil {
			return SSHIdleSample{}, fmt.Errorf("read terminal processes: %w", err)
		}
		return sampleSSHIdle(sessions, snapshot, lookup, terminal, now(), config, sshdFilters)
	}
}

func newSSHSessionSampler(reader process.Reader, lookup *process.UserLookup, sessions func() ([]utmp.Session, error), terminal func(string) (utmp.Terminal, error), now func() time.Time) SSHSessionSamplerFunc {
	return func(config SSHSessionConfig) (SSHSessionSample, error) {
		sshdFilters, err := sshSessionFilters(config.SSHDFilters)
		if err != nil {
			return SSHSessionSample{}, err
		}
		loggedIn, err := sessions()
		if err != nil {
			return SSHSessionSample{}, fmt.Errorf("load terminal sessions: %w", err)
		}
		snapshot, err := process.Snapshot(reader)
		if err != nil {
			return SSHSessionSample{}, fmt.Errorf("read terminal processes: %w", err)
		}
		return sampleSSHSessions(loggedIn, snapshot, terminal, now(), sshdFilters, lookup.ResolveUser)
	}
}

func sshSessionFilters(filters []process.IdentityFilter) ([]process.IdentityFilter, error) {
	if len(filters) == 0 {
		return nil, errors.New("sshd process selector is required")
	}
	out := make([]process.IdentityFilter, 0, len(filters))
	for _, raw := range filters {
		if raw.Exe == "" || raw.User == "" {
			return nil, errors.New("sshd process selector requires exact exe and user")
		}
		filter, err := process.NewIdentityFilter(raw.Exe, raw.User, "")
		if err != nil {
			return nil, fmt.Errorf("sshd process selector: %w", err)
		}
		out = append(out, filter)
	}
	return out, nil
}

func sshdFilters(exes []string) ([]process.IdentityFilter, error) {
	if len(exes) == 0 {
		return nil, errors.New("sshd_exe is required")
	}
	filters := make([]process.IdentityFilter, 0, len(exes))
	for _, exe := range exes {
		filter, err := process.NewIdentityFilter(exe, "", "")
		if err != nil {
			return nil, fmt.Errorf("sshd_exe %q: %w", exe, err)
		}
		filters = append(filters, filter)
	}
	return filters, nil
}

func sampleSSHIdle(sessions []utmp.Session, snapshot map[int]process.Identity, lookup *process.UserLookup, terminal func(string) (utmp.Terminal, error), now time.Time, config SSHIdleConfig, sshdFilters []process.IdentityFilter) (SSHIdleSample, error) {
	if lookup == nil {
		lookup = process.DefaultUserLookup()
	}
	seen := map[string]bool{}
	var sample SSHIdleSample
	for _, session := range sessions {
		if seen[session.Line] {
			continue
		}
		seen[session.Line] = true
		info, err := terminal(session.Line)
		if err != nil {
			return SSHIdleSample{}, fmt.Errorf("terminal %s: %w", session.Line, err)
		}
		if info.Device == 0 {
			return SSHIdleSample{}, fmt.Errorf("terminal %s has no device", session.Line)
		}
		processes := terminalProcesses(snapshot, info.Device)
		if len(processes) == 0 {
			return SSHIdleSample{}, fmt.Errorf("terminal %s has no visible processes", session.Line)
		}
		ssh, _, unknown, err := terminalSSH(processes, snapshot, sshdFilters, nil)
		if err != nil {
			return SSHIdleSample{}, fmt.Errorf("attribute terminal %s to sshd: %w", session.Line, err)
		}
		if unknown {
			return SSHIdleSample{}, fmt.Errorf("cannot attribute terminal %s to sshd", session.Line)
		}
		if !ssh {
			continue
		}
		protected, err := terminalProtected(processes, config.ProtectedProcesses, lookup)
		if err != nil {
			return SSHIdleSample{}, fmt.Errorf("protected terminal %s: %w", session.Line, err)
		}
		if protected {
			sample.ProtectedCount++
			continue
		}
		idle := max(now.Sub(info.AccessedAt), 0)
		if idle > sample.OldestIdle {
			sample.OldestIdle = idle
		}
		if idle >= config.IdleFor {
			sample.Count++
		}
	}
	return sample, nil
}

func sampleSSHSessions(sessions []utmp.Session, snapshot map[int]process.Identity, terminal func(string) (utmp.Terminal, error), now time.Time, sshdFilters []process.IdentityFilter, resolveUser process.UserResolver) (SSHSessionSample, error) {
	seen := map[string]bool{}
	var sample SSHSessionSample
	for _, session := range sessions {
		if seen[session.Line] {
			continue
		}
		seen[session.Line] = true
		info, err := terminal(session.Line)
		if err != nil {
			addSSHSessionIssue(&sample, session, fmt.Sprintf("terminal metadata unavailable: %v", err))
			continue
		}
		if info.Device == 0 {
			addSSHSessionIssue(&sample, session, "terminal has no device identity")
			continue
		}
		processes := terminalProcesses(snapshot, info.Device)
		if len(processes) == 0 {
			addSSHSessionIssue(&sample, session, "terminal has no visible processes")
			continue
		}
		ssh, target, unknown, err := terminalSSH(processes, snapshot, sshdFilters, resolveUser)
		if err != nil {
			addSSHSessionIssue(&sample, session, fmt.Sprintf("sshd identity verification failed: %v", err))
			continue
		}
		if unknown {
			addSSHSessionIssue(&sample, session, sshSessionIssueMessage(processes, snapshot))
			continue
		}
		if !ssh {
			if session.Host != "" {
				addSSHSessionIssue(&sample, session, "no configured sshd identity in the live process ancestry")
				continue
			}
			sample.Console++
			continue
		}
		sample.SSH = append(sample.SSH, SSHSession{
			User:       session.User,
			Terminal:   session.Line,
			PID:        target.PID,
			StartTicks: target.StartTicks,
			Idle:       max(now.Sub(info.AccessedAt), 0),
		})
	}
	return sample, nil
}

func addSSHSessionIssue(s *SSHSessionSample, session utmp.Session, message string) {
	s.Issues = append(s.Issues, SSHSessionIssue{User: session.User, Terminal: session.Line, Message: message})
}

func sshSessionIssueMessage(processes []process.Identity, snapshot map[int]process.Identity) string {
	seen := make(map[int]bool)
	queue := slices.Clone(processes)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if id.PID <= 0 || seen[id.PID] {
			continue
		}
		seen[id.PID] = true
		if id.ExePrev != "" {
			return "executable " + id.ExePrev + " was replaced"
		}
		if !id.ExeOK {
			return "a process executable in the live ancestry is unreadable"
		}
		if id.PPID > 1 {
			if parent, ok := snapshot[id.PPID]; ok {
				queue = append(queue, parent)
			}
		}
	}
	return "no configured sshd identity in the live process ancestry"
}

func terminalProcesses(snapshot map[int]process.Identity, device uint64) []process.Identity {
	processes := make([]process.Identity, 0)
	for _, id := range snapshot {
		if id.TTYOK && id.TTY == device {
			processes = append(processes, id)
		}
	}
	slices.SortFunc(processes, func(a, b process.Identity) int { return a.PID - b.PID })
	return processes
}

func terminalSSH(processes []process.Identity, snapshot map[int]process.Identity, filters []process.IdentityFilter, resolveUser process.UserResolver) (ssh bool, target process.Identity, unknown bool, err error) {
	for _, id := range processes {
		matched, candidate, uncertain, matchErr := sshAncestor(id, snapshot, filters, resolveUser)
		if matchErr != nil {
			return false, process.Identity{}, true, matchErr
		}
		if matched {
			return true, candidate, false, nil
		}
		unknown = unknown || uncertain
	}
	return false, process.Identity{}, unknown, nil
}

func sshAncestor(id process.Identity, snapshot map[int]process.Identity, filters []process.IdentityFilter, resolveUser process.UserResolver) (matched bool, target process.Identity, unknown bool, err error) {
	seen := map[int]bool{}
	terminal := id.TTY
	for {
		if seen[id.PID] {
			return false, process.Identity{}, true, nil
		}
		seen[id.PID] = true
		if target.PID == 0 && (!id.TTYOK || id.TTY != terminal) {
			target = id
		}
		for _, filter := range filters {
			outcome, matchErr := filter.Match(id, resolveUser, nil)
			if matchErr != nil {
				return false, process.Identity{}, true, fmt.Errorf("match sshd identity: %w", matchErr)
			}
			if outcome == process.IdentityNoMatch && sshdIdentityConflict(id, filter) {
				// The listener has the configured executable but another real UID.
				// Treat it as untrusted rather than misreporting its terminal as a
				// local console or offering it for a close action.
				return false, process.Identity{}, true, nil
			}
			if outcome == process.IdentityMatched {
				// A direct shell child of sshd has no separate session boundary;
				// never let the trusted listener itself become a close target.
				if target.PID == id.PID {
					target = process.Identity{}
				}
				return true, target, false, nil
			}
			unknown = unknown || outcome == process.IdentityUnknown
		}
		if id.PPID <= 1 {
			return false, process.Identity{}, unknown, nil
		}
		parent, ok := snapshot[id.PPID]
		if !ok {
			return false, process.Identity{}, true, nil
		}
		id = parent
	}
}

func sshdIdentityConflict(id process.Identity, filter process.IdentityFilter) bool {
	if filter.Exe == "" || filter.User == "" || !id.ExeOK {
		return false
	}
	exeOnly, err := process.NewIdentityFilter(filter.Exe, "", "")
	if err != nil {
		return true
	}
	outcome, err := exeOnly.Match(id, nil, nil)
	return err != nil || outcome == process.IdentityMatched
}

// VerifySSHSession confirms that a client-requested session is still live and
// has the same terminal boundary and process generation observed by the
// dashboard. It is deliberately exact: a vanished terminal or recycled PID
// must be rediscovered rather than signalled.
func (s SSHSessionSample) VerifySSHSession(want SSHSession) error {
	if want.PID <= 0 || want.StartTicks == 0 || want.Terminal == "" {
		return errors.New("invalid SSH session target")
	}
	for _, got := range s.SSH {
		if got.PID != want.PID || got.Terminal != want.Terminal {
			continue
		}
		if got.StartTicks == 0 || got.StartTicks != want.StartTicks {
			return errors.New("SSH session changed; refresh and try again")
		}
		return nil
	}
	return errors.New("SSH session is no longer active; refresh and try again")
}

func terminalProtected(processes []process.Identity, filters []SSHProtectedProcess, lookup *process.UserLookup) (bool, error) {
	for _, id := range processes {
		for _, protected := range filters {
			outcome, err := protected.Filter.Match(id, lookup.ResolveUser, lookup.ResolveGroup)
			if err != nil {
				return false, fmt.Errorf("%s: %w", protected.Name, err)
			}
			switch outcome {
			case process.IdentityNoMatch:
				continue
			case process.IdentityMatched:
				return true, nil
			case process.IdentityUnknown:
				return false, fmt.Errorf("%s executable for pid %d is unreadable", protected.Name, id.PID)
			}
		}
	}
	return false, nil
}

func parseProtectedProcesses(raw any) ([]SSHProtectedProcess, error) {
	if raw == nil {
		return nil, nil
	}
	entries, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("must be a mapping")
	}
	protected := make([]SSHProtectedProcess, 0, len(entries))
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		entry, ok := entries[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s must be a mapping", name)
		}
		for _, key := range slices.Sorted(maps.Keys(entry)) {
			if !IsSSHProtectedProcessField(key) {
				return nil, fmt.Errorf("%s.%s is not supported", name, key)
			}
		}
		for _, key := range SSHProtectedProcessFields() {
			if value, present := entry[key]; present && cfgval.AsString(value) == "" {
				return nil, fmt.Errorf("%s.%s must be a non-empty string", name, key)
			}
		}
		filter, err := process.NewIdentityFilter(
			cfgval.AsString(entry[CheckKeyExe]),
			cfgval.AsString(entry[CheckKeyUser]),
			cfgval.AsString(entry[CheckKeyGroup]),
		)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		protected = append(protected, SSHProtectedProcess{Name: name, Filter: filter})
	}
	return protected, nil
}
