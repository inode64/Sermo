package app

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/utmp"
	"sermo/internal/web"
)

type terminalSessionSource struct {
	check       string
	multiplexer string
	user        string
	binary      string
	socket      string
}

func (s terminalSessionSource) config() checks.TerminalSessionConfig {
	return checks.TerminalSessionConfig{Multiplexer: s.multiplexer, Binary: s.binary, User: s.user, Socket: s.socket}
}

type sshSessionIdentity struct {
	pid        int
	startTicks uint64
	terminal   string
}

func terminalSessionSources(tree map[string]any) []terminalSessionSource {
	entries, _ := tree[config.SectionChecks].(map[string]any)
	result := make([]terminalSessionSource, 0)
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		entry, _ := entries[name].(map[string]any)
		if cfgval.String(entry[checks.CheckKeyType]) != checks.CheckTypeTerminalSessions {
			continue
		}
		result = append(result, terminalSessionSource{
			check:       name,
			multiplexer: cfgval.String(entry[checks.CheckKeyMultiplexer]),
			user:        cfgval.String(entry[checks.CheckKeyUser]),
			binary:      cfgval.String(entry[checks.CheckKeyBinary]),
			socket:      cfgval.String(entry[checks.CheckKeySocket]),
		})
	}
	return result
}

// Sessions returns the dashboard-wide interactive-session inventory. SSH is
// sampled through the short-lived shared cache; tmux and screen are decoded
// only from daemon-published check snapshots, never by running their clients in
// an HTTP request.
func (b *WebBackend) Sessions(_ context.Context) web.SessionInventory {
	result := web.SessionInventory{}
	seenSSH := make(map[sshSessionIdentity]struct{})
	for _, service := range b.order {
		entry := b.entries[service]
		if entry == nil {
			continue
		}
		b.appendSSHSessions(&result, seenSSH, service, entry)
		b.appendTerminalSessions(&result, service, entry)
	}
	b.attachSessionMetrics(&result)
	slices.SortFunc(result.SSH, compareWebSSHSessions)
	slices.SortFunc(result.Terminal, compareWebTerminalSessions)
	return result
}

type sessionProcessSnapshot struct {
	byPID    map[int]process.Identity
	children map[int][]int
}

func newSessionProcessSnapshot(reader process.Reader) sessionProcessSnapshot {
	result := sessionProcessSnapshot{byPID: map[int]process.Identity{}, children: map[int][]int{}}
	if reader == nil {
		return result
	}
	snapshot, err := process.Snapshot(reader)
	if err != nil {
		return result
	}
	result.byPID = snapshot
	for pid, identity := range snapshot {
		result.children[identity.PPID] = append(result.children[identity.PPID], pid)
	}
	return result
}

func (s sessionProcessSnapshot) treePIDs(roots []int) []int {
	seen := make(map[int]bool)
	queue := slices.Clone(roots)
	result := make([]int, 0, len(roots))
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if pid <= 0 || seen[pid] {
			continue
		}
		if _, ok := s.byPID[pid]; !ok {
			continue
		}
		seen[pid] = true
		result = append(result, pid)
		queue = append(queue, s.children[pid]...)
	}
	slices.Sort(result)
	return result
}

func (b *WebBackend) attachSessionMetrics(inventory *web.SessionInventory) {
	if b == nil || inventory == nil {
		return
	}
	b.sessionMetricsMu.Lock()
	defer b.sessionMetricsMu.Unlock()
	snapshot := newSessionProcessSnapshot(b.terminalProcessReader)
	active := make(map[string]struct{}, len(inventory.SSH)+len(inventory.Terminal))
	for i := range inventory.SSH {
		session := &inventory.SSH[i]
		key := "ssh:" + strconv.Itoa(session.PID) + ":" + strconv.FormatUint(session.StartTicks, 10)
		active[key] = struct{}{}
		if pids := snapshot.treePIDs([]int{session.PID}); b.sessionMetricCollector != nil && len(pids) > 0 {
			session.SessionUsage = sessionUsage(b.sessionMetricCollector.SampleService(key, pids))
		}
	}
	for i := range inventory.Terminal {
		session := &inventory.Terminal[i]
		key := session.Multiplexer + ":" + session.Service + ":" + session.Check + ":" + session.Identity
		active[key] = struct{}{}
		pids := snapshot.treePIDs(session.PIDs)
		if b.sessionMetricCollector != nil && len(pids) > 0 {
			session.SessionUsage = sessionUsage(b.sessionMetricCollector.SampleService(key, pids))
		}
		attachTerminalSessionIdle(snapshot, session, b.webNow())
	}
	if b.sessionMetricCollector != nil {
		for key := range b.sessionMetricKeys {
			if _, ok := active[key]; !ok {
				b.sessionMetricCollector.ForgetService(key)
			}
		}
	}
	b.sessionMetricKeys = active
}

func sessionUsage(sample metrics.Snapshot) web.SessionUsage {
	result := web.SessionUsage{}
	mem := sample[metrics.MetricMemory]
	result.MemoryReady = mem.Ready
	if mem.Ready {
		result.RSS = int64(mem.Absolute)
	}
	cpu := sample[metrics.MetricCPU]
	result.CPUReady, result.CPU = cpu.Ready, cpu.Percent
	read, write := sample[metrics.MetricIORead], sample[metrics.MetricIOWrite]
	result.IOReady = read.Ready && write.Ready
	if result.IOReady {
		result.IORead, result.IOWrite = read.Absolute, write.Absolute
	}
	return result
}

func attachTerminalSessionIdle(snapshot sessionProcessSnapshot, session *web.TerminalSession, now time.Time) {
	if session.ActivityUnix > 0 {
		session.IdleSeconds = max(now.Unix()-session.ActivityUnix, 0)
		session.HasIdle = true
		return
	}
	var latest time.Time
	for _, pid := range snapshot.treePIDs(session.PIDs) {
		identity := snapshot.byPID[pid]
		if !identity.TTYOK || identity.TTY == 0 {
			continue
		}
		if at, ok := utmp.TerminalAccessedAt("/dev", identity.TTY); ok && at.After(latest) {
			latest = at
		}
	}
	if !latest.IsZero() {
		session.IdleSeconds = max(int64(now.Sub(latest).Seconds()), 0)
		session.HasIdle = true
	}
}

func (b *WebBackend) appendSSHSessions(result *web.SessionInventory, seen map[sshSessionIdentity]struct{}, service string, entry *webEntry) {
	if len(entry.sshSessionFilters) == 0 {
		return
	}
	source := web.SessionSource{Kind: web.SessionKindSSH, Service: service, State: web.SessionSourceAvailable}
	sessions, err := b.sshSessions(entry.sshSessionFilters)
	if err != nil {
		source.State = web.SessionSourceUnavailable
		source.Message = err.Error()
		result.Sources = append(result.Sources, source)
		return
	}
	result.Sources = append(result.Sources, source)
	for _, session := range sshSessionsToWeb(sessions) {
		key := sshSessionIdentity{pid: session.PID, startTicks: session.StartTicks, terminal: session.Terminal}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		session.Service = service
		result.SSH = append(result.SSH, session)
	}
}

func (b *WebBackend) appendTerminalSessions(result *web.SessionInventory, service string, entry *webEntry) {
	if len(entry.terminalSessions) == 0 {
		return
	}
	snapshots := b.snapshots.Get(service)
	for _, configured := range entry.terminalSessions {
		source := web.SessionSource{
			Kind: configured.multiplexer, Service: service, Check: configured.check,
			User: configured.user, State: web.SessionSourceCollecting,
		}
		if entry.disabled {
			source.State = web.SessionSourceUnavailable
			source.Message = "service disabled"
			result.Sources = append(result.Sources, source)
			continue
		}
		snapshot, ok := snapshots[configured.check]
		if !ok || !b.serviceCheckSnapshotCurrent(entry, configured.check, snapshot) {
			result.Sources = append(result.Sources, source)
			continue
		}
		if sampleError := cfgval.String(snapshot.Data[checks.DataKeySampleError]); sampleError != "" {
			source.State = web.SessionSourceUnavailable
			source.Message = sampleError
			result.Sources = append(result.Sources, source)
			continue
		}
		source.State = web.SessionSourceAvailable
		result.Sources = append(result.Sources, source)
		for _, session := range checks.TerminalSessionsFromData(snapshot.Data) {
			result.Terminal = append(result.Terminal, web.TerminalSession{
				Service: service, Check: configured.check, Multiplexer: session.Multiplexer,
				Name: session.Name, User: session.User, State: session.State, Windows: session.Windows,
				Identity: session.Identity, CanClose: session.Identity != "", PIDs: slices.Clone(session.PIDs),
				ActivityUnix: session.ActivityUnix, TTY: session.TTY,
			})
		}
	}
}

func freshTerminalSessionCloser(runner execx.Runner, sources []terminalSessionSource) func(context.Context, operation.TerminalSessionTarget) error {
	byCheck := make(map[string]terminalSessionSource, len(sources))
	for _, source := range sources {
		byCheck[source.check] = source
	}
	return func(ctx context.Context, target operation.TerminalSessionTarget) error {
		source, ok := byCheck[target.Check]
		if !ok || source.multiplexer != target.Multiplexer || source.user != target.User {
			return errors.New("terminal session source changed; refresh and try again")
		}
		return checks.CloseTerminalSession(ctx, runner, source.config(), checks.TerminalSession{
			Multiplexer: target.Multiplexer, Name: target.Name, User: target.User, Identity: target.Identity,
		})
	}
}

// CloseTerminalSession closes one freshly revalidated tmux or screen session
// through the service operation engine and the configured multiplexer client.
func (b *WebBackend) CloseTerminalSession(ctx context.Context, name string, session web.TerminalSession) web.ActionResult {
	e := b.entries[name]
	if e == nil {
		return b.operateError(name, "close terminal session", unknownServiceMessage+name)
	}
	if e.disabled {
		return b.operateError(name, "close terminal session", serviceSubjectPrefix+name+" is disabled in configuration")
	}
	r := e.engine.CloseTerminalSession(ctx, operation.TerminalSessionTarget{
		Check: session.Check, Multiplexer: session.Multiplexer, Name: session.Name, User: session.User, Identity: session.Identity,
	})
	return webActionResultFrom(r, name, "close terminal session")
}

func compareWebSSHSessions(a, b web.SSHSession) int {
	if byService := strings.Compare(a.Service, b.Service); byService != 0 {
		return byService
	}
	if byUser := strings.Compare(a.User, b.User); byUser != 0 {
		return byUser
	}
	if byTerminal := strings.Compare(a.Terminal, b.Terminal); byTerminal != 0 {
		return byTerminal
	}
	return a.PID - b.PID
}

func compareWebTerminalSessions(a, b web.TerminalSession) int {
	if byMultiplexer := strings.Compare(a.Multiplexer, b.Multiplexer); byMultiplexer != 0 {
		return byMultiplexer
	}
	if byService := strings.Compare(a.Service, b.Service); byService != 0 {
		return byService
	}
	if byUser := strings.Compare(a.User, b.User); byUser != 0 {
		return byUser
	}
	return strings.Compare(a.Name, b.Name)
}
