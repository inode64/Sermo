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
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
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
	if reader == nil {
		reader = process.NewCachingReader(process.OSReader{ReadTTY: true}, 0)
	}
	if lookup == nil {
		lookup = process.DefaultUserLookup()
	}
	return newSSHIdleSampler(reader, lookup, utmp.Sessions, func(line string) (utmp.Terminal, error) {
		return utmp.TerminalInfo(sshIdleDevRoot, line)
	}, time.Now)
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
		ssh, unknown := terminalSSH(processes, snapshot, sshdFilters)
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

func terminalSSH(processes []process.Identity, snapshot map[int]process.Identity, filters []process.IdentityFilter) (ssh, unknown bool) {
	for _, id := range processes {
		matched, uncertain := sshAncestor(id, snapshot, filters)
		if matched {
			return true, false
		}
		unknown = unknown || uncertain
	}
	return false, unknown
}

func sshAncestor(id process.Identity, snapshot map[int]process.Identity, filters []process.IdentityFilter) (matched, unknown bool) {
	seen := map[int]bool{}
	for {
		if seen[id.PID] {
			return false, true
		}
		seen[id.PID] = true
		for _, filter := range filters {
			outcome, _ := filter.Match(id, nil, nil)
			if outcome == process.IdentityMatched {
				return true, false
			}
			unknown = unknown || outcome == process.IdentityUnknown
		}
		if id.PPID <= 1 {
			return false, unknown
		}
		parent, ok := snapshot[id.PPID]
		if !ok {
			return false, true
		}
		id = parent
	}
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
			if key != CheckKeyExe && key != CheckKeyUser && key != CheckKeyGroup {
				return nil, fmt.Errorf("%s.%s is not supported", name, key)
			}
		}
		for _, key := range []string{CheckKeyExe, CheckKeyUser, CheckKeyGroup} {
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
