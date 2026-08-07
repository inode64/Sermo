package checks

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/process"
)

const (
	// TerminalMultiplexerTmux selects the tmux client protocol.
	TerminalMultiplexerTmux = "tmux"
	// TerminalMultiplexerScreen selects the GNU screen client protocol.
	TerminalMultiplexerScreen = "screen"
	// TerminalMultiplexerSummary is the user-facing list of supported values.
	TerminalMultiplexerSummary = TerminalMultiplexerTmux + " or " + TerminalMultiplexerScreen
	// TerminalSessionStateAttached reports a session with at least one attached client.
	TerminalSessionStateAttached = "attached"
	// TerminalSessionStateDetached reports a session without an attached client.
	TerminalSessionStateDetached = "detached"
	// TerminalSessionStateUnknown reports a client state that could not be normalized.
	TerminalSessionStateUnknown = "unknown"

	tmuxSessionFormat               = "#{session_name}\t#{session_attached}\t#{session_windows}\t#{session_activity}\t#{session_id}\t#{session_created}\t#{pane_pid}\t#{pane_tty}"
	screenSessionStateMultiAttached = "multi, " + TerminalSessionStateAttached
	screenSessionStateMultiDetached = "multi, " + TerminalSessionStateDetached
)

const (
	tmuxSessionNameField = iota
	tmuxSessionAttachedField
	tmuxSessionWindowsField
	tmuxSessionActivityField
	tmuxSessionIDField
	tmuxSessionCreatedField
	tmuxSessionPIDField
	tmuxSessionTTYField
	tmuxSessionFieldCount
)

const (
	screenSessionNameMatch  = 1
	screenSessionStateMatch = 2
	screenSessionMatchCount = screenSessionStateMatch + 1
	screenSessionNameParts  = 2
)

var screenSessionLine = regexp.MustCompile(`^\s*(\d+\.\S+)\s+\(([^)]+)\)`)

// TerminalSession is one tmux or screen session reported by its own client.
// Identity is a multiplexer-owned generation marker used to reject a stale
// close request after a session name has been reused.
type TerminalSession struct {
	Multiplexer  string `json:"multiplexer"`
	Name         string `json:"name"`
	User         string `json:"user"`
	State        string `json:"state"`
	Windows      int    `json:"windows,omitempty"`
	ActivityUnix int64  `json:"activity_unix,omitempty"`
	Identity     string `json:"identity,omitempty"`
	PIDs         []int  `json:"pids,omitempty"`
	TTY          string `json:"tty,omitempty"`
}

// TerminalSessionSample is the read-only result of one configured terminal
// multiplexer query. Present distinguishes a live multiplexer namespace with
// no sessions from an absent namespace, so the Web UI does not render stale
// configured sources as empty sessions.
type TerminalSessionSample struct {
	Sessions []TerminalSession
	Present  bool
}

// TerminalSessionConfig identifies the one terminal multiplexer namespace to
// query. The explicit user prevents a root daemon from silently enumerating
// every user's private terminal sessions.
type TerminalSessionConfig struct {
	Multiplexer string
	Binary      string
	User        string
	Socket      string
	// StartTicks is injectable for deterministic screen identity tests. Nil
	// reads the kernel process generation from /proc.
	StartTicks func(int) (uint64, bool)
}

type terminalMultiplexerAdapter struct {
	args           func(TerminalSessionConfig) []string
	closeArgs      func(TerminalSessionConfig, TerminalSession) []string
	sessionsAbsent func(string) bool
	parseSessions  func(TerminalSessionConfig, string) ([]TerminalSession, error)
}

// Validate confirms that a terminal-session query has an explicit, bounded
// target. It is shared by the builder and test-only direct sampler use.
func (c TerminalSessionConfig) Validate() error {
	if !IsTerminalMultiplexer(c.Multiplexer) {
		return fmt.Errorf("multiplexer must be %s", TerminalMultiplexerSummary)
	}
	if c.Binary == "" {
		return errors.New("binary is required")
	}
	if !filepath.IsAbs(c.Binary) {
		return fmt.Errorf("binary path %q must be absolute", c.Binary)
	}
	if c.User == "" {
		return errors.New("user is required")
	}
	if c.Socket == "" {
		return nil
	}
	if c.Multiplexer != TerminalMultiplexerTmux {
		return errors.New("socket is only supported for tmux")
	}
	if !filepath.IsAbs(c.Socket) {
		return fmt.Errorf("socket path %q must be absolute", c.Socket)
	}
	return nil
}

// IsTerminalMultiplexer reports whether name is a terminal multiplexer Sermo
// can enumerate through its read-only client command.
func IsTerminalMultiplexer(name string) bool {
	_, ok := terminalMultiplexerAdapterFor(name)
	return ok
}

func terminalMultiplexerAdapterFor(name string) (terminalMultiplexerAdapter, bool) {
	switch name {
	case TerminalMultiplexerTmux:
		return terminalMultiplexerAdapter{
			args:           tmuxSessionArgs,
			closeArgs:      tmuxSessionCloseArgs,
			sessionsAbsent: tmuxSessionsAbsent,
			parseSessions:  parseTmuxSessions,
		}, true
	case TerminalMultiplexerScreen:
		return terminalMultiplexerAdapter{
			args:           screenSessionArgs,
			closeArgs:      screenSessionCloseArgs,
			sessionsAbsent: screenSessionsAbsent,
			parseSessions:  parseScreenSessionsResult,
		}, true
	default:
		return terminalMultiplexerAdapter{}, false
	}
}

// TerminalSessionsFromData restores terminal sessions from a result data map.
// JSON-backed snapshots rehydrate slices as []any maps, so the conversion keeps
// the service detail working after a daemon restart as well as in live memory.
func TerminalSessionsFromData(data map[string]any) []TerminalSession {
	raw := data[DataKeyTerminalSessions]
	switch values := raw.(type) {
	case []TerminalSession:
		return slices.Clone(values)
	case []any:
		out := make([]TerminalSession, 0, len(values))
		for _, value := range values {
			entry, ok := value.(map[string]any)
			if !ok {
				continue
			}
			session, ok := terminalSessionFromMap(entry)
			if ok {
				out = append(out, session)
			}
		}
		return out
	default:
		return nil
	}
}

func terminalSessionFromMap(entry map[string]any) (TerminalSession, bool) {
	multiplexer := cfgval.String(entry[CheckKeyMultiplexer])
	name := cfgval.String(entry[CheckKeyName])
	user := cfgval.String(entry[CheckKeyUser])
	state := cfgval.String(entry[CheckKeyState])
	if !IsTerminalMultiplexer(multiplexer) || name == "" || user == "" || !isTerminalSessionState(state) {
		return TerminalSession{}, false
	}
	windows, _ := cfgval.Int(entry[DataKeyWindows])
	activityUnix, _ := cfgval.Int(entry[DataKeyActivityUnix])
	pids, _ := cfgval.IntList(entry[DataKeyPIDs])
	return TerminalSession{
		Multiplexer: multiplexer, Name: name, User: user, State: state, Windows: max(windows, 0),
		ActivityUnix: int64(max(activityUnix, 0)), Identity: cfgval.String(entry[DataKeyIdentity]), PIDs: pids,
		TTY: cfgval.String(entry[DataKeyTTY]),
	}, true
}

func isTerminalSessionState(state string) bool {
	return state == TerminalSessionStateAttached || state == TerminalSessionStateDetached || state == TerminalSessionStateUnknown
}

// terminalSessionsCheck compares a configured session count. It does not
// create, attach, detach or signal any terminal-multiplexer process.
type terminalSessionsCheck struct {
	base
	preds  []levelPred
	config TerminalSessionConfig
	runner execx.Runner
}

func (c terminalSessionsCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start

	sample, err := sampleTerminalSessions(ctx, c.runner, c.config)
	if err != nil {
		failure := execx.FormatContextOrError(err, c.timeout)
		res := c.unavailableResult("terminal sessions: "+failure, start)
		res.Data = map[string]any{DataKeySampleError: failure}
		return res
	}
	attached, detached := terminalSessionCounts(sample.Sessions)
	values := map[string]float64{
		DataKeyCount:    float64(len(sample.Sessions)),
		DataKeyAttached: float64(attached),
		DataKeyDetached: float64(detached),
	}
	res := c.result(levelPredsHold(c.preds, values), terminalSessionMessage(c.config, len(sample.Sessions), attached, detached), start)
	res.Data = map[string]any{
		DataKeyCount:            len(sample.Sessions),
		DataKeyAttached:         attached,
		DataKeyDetached:         detached,
		DataKeyPresent:          sample.Present,
		DataKeyTerminalSessions: sample.Sessions,
		DataKeyValue:            float64(len(sample.Sessions)),
		DataKeyUnit:             metrics.MetricUnitSessions,
	}
	return res
}

func sampleTerminalSessions(ctx context.Context, runner execx.Runner, config TerminalSessionConfig) (TerminalSessionSample, error) {
	if err := config.Validate(); err != nil {
		return TerminalSessionSample{}, err
	}
	adapter, ok := terminalMultiplexerAdapterFor(config.Multiplexer)
	if !ok {
		return TerminalSessionSample{}, fmt.Errorf("unsupported terminal multiplexer %q", config.Multiplexer)
	}
	runner = execx.RunnerOrDefault(runner)
	result, err := execx.RunUser(ctx, runner, execx.NoTimeout, config.User, config.Binary, adapter.args(config)...)
	if result.ExitCode == execx.ExitCodeRunFailure {
		if err == nil {
			err = errors.New(execx.CommandDidNotStart)
		}
		return TerminalSessionSample{}, fmt.Errorf("list %s sessions for user %q: %w", config.Multiplexer, config.User, err)
	}
	output := strings.TrimSpace(result.Stdout + "\n" + result.Stderr)
	if adapter.sessionsAbsent(output) {
		return TerminalSessionSample{Present: false}, nil
	}
	if err != nil {
		return TerminalSessionSample{}, fmt.Errorf("list %s sessions for user %q: %w", config.Multiplexer, config.User, err)
	}
	if result.ExitCode != execx.ExitCodeSuccess {
		return TerminalSessionSample{}, fmt.Errorf("list %s sessions for user %q: exit %d", config.Multiplexer, config.User, result.ExitCode)
	}
	sessions, err := adapter.parseSessions(config, result.Stdout)
	if err != nil {
		return TerminalSessionSample{}, err
	}
	return TerminalSessionSample{Sessions: sessions, Present: true}, nil
}

func tmuxSessionArgs(config TerminalSessionConfig) []string {
	if config.Socket == "" {
		return []string{"list-sessions", "-F", tmuxSessionFormat}
	}
	return []string{"-S", config.Socket, "list-sessions", "-F", tmuxSessionFormat}
}

func screenSessionArgs(TerminalSessionConfig) []string {
	return []string{"-ls"}
}

func tmuxSessionCloseArgs(config TerminalSessionConfig, session TerminalSession) []string {
	args := []string{"kill-session", "-t", "=" + session.Name}
	if config.Socket == "" {
		return args
	}
	return append([]string{"-S", config.Socket}, args...)
}

func tmuxServerCloseArgs(config TerminalSessionConfig) []string {
	return []string{"-S", config.Socket, "kill-server"}
}

func screenSessionCloseArgs(_ TerminalSessionConfig, session TerminalSession) []string {
	return []string{"-S", session.Name, "-X", "quit"}
}

func tmuxSessionsAbsent(output string) bool {
	output = strings.ToLower(output)
	return strings.Contains(output, "no server running on") ||
		(strings.Contains(output, "error connecting to") && strings.Contains(output, "no such file or directory"))
}

func screenSessionsAbsent(output string) bool {
	return strings.Contains(strings.ToLower(output), "no sockets found in")
}

func parseTmuxSessions(config TerminalSessionConfig, output string) ([]TerminalSession, error) {
	if strings.TrimSpace(output) == "" {
		return nil, nil
	}
	sessions := make([]TerminalSession, 0)
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) != tmuxSessionFieldCount || strings.TrimSpace(parts[tmuxSessionNameField]) == "" {
			return nil, fmt.Errorf("parse tmux session line %q", line)
		}
		attachedClients, err := strconv.Atoi(strings.TrimSpace(parts[tmuxSessionAttachedField]))
		if err != nil || attachedClients < 0 {
			return nil, fmt.Errorf("parse tmux session attached client count %q", parts[tmuxSessionAttachedField])
		}
		windows, err := strconv.Atoi(strings.TrimSpace(parts[tmuxSessionWindowsField]))
		if err != nil || windows < 0 {
			return nil, fmt.Errorf("parse tmux session window count %q", parts[tmuxSessionWindowsField])
		}
		activity, err := strconv.ParseInt(strings.TrimSpace(parts[tmuxSessionActivityField]), 10, 64)
		if err != nil || activity < 0 {
			return nil, fmt.Errorf("parse tmux session activity %q", parts[tmuxSessionActivityField])
		}
		created, err := strconv.ParseInt(strings.TrimSpace(parts[tmuxSessionCreatedField]), 10, 64)
		if err != nil || created <= 0 {
			return nil, fmt.Errorf("parse tmux session creation time %q", parts[tmuxSessionCreatedField])
		}
		pid, err := strconv.Atoi(strings.TrimSpace(parts[tmuxSessionPIDField]))
		if err != nil || pid <= 0 {
			return nil, fmt.Errorf("parse tmux session pane pid %q", parts[tmuxSessionPIDField])
		}
		sessionID := strings.TrimSpace(parts[tmuxSessionIDField])
		if sessionID == "" {
			return nil, errors.New("parse tmux session id")
		}
		state := TerminalSessionStateDetached
		if attachedClients > 0 {
			state = TerminalSessionStateAttached
		}
		sessions = append(sessions, TerminalSession{
			Multiplexer: TerminalMultiplexerTmux, Name: strings.TrimSpace(parts[tmuxSessionNameField]), User: config.User,
			State: state, Windows: windows, ActivityUnix: activity, Identity: sessionID + ":" + strconv.FormatInt(created, 10),
			PIDs: []int{pid}, TTY: strings.TrimSpace(parts[tmuxSessionTTYField]),
		})
	}
	return sortedTerminalSessions(sessions), nil
}

func parseScreenSessions(config TerminalSessionConfig, output string) []TerminalSession {
	sessions := make([]TerminalSession, 0)
	for line := range strings.SplitSeq(output, "\n") {
		matches := screenSessionLine.FindStringSubmatch(line)
		if len(matches) != screenSessionMatchCount {
			continue
		}
		state := TerminalSessionStateUnknown
		switch strings.ToLower(strings.TrimSpace(matches[screenSessionStateMatch])) {
		case TerminalSessionStateAttached, screenSessionStateMultiAttached:
			state = TerminalSessionStateAttached
		case TerminalSessionStateDetached, screenSessionStateMultiDetached:
			state = TerminalSessionStateDetached
		}
		name := matches[screenSessionNameMatch]
		pid, _ := strconv.Atoi(strings.SplitN(name, ".", screenSessionNameParts)[0])
		identity := ""
		if pid > 0 {
			startTicks := config.StartTicks
			if startTicks == nil {
				startTicks = process.StartTicks
			}
			if ticks, ok := startTicks(pid); ok {
				identity = strconv.Itoa(pid) + ":" + strconv.FormatUint(ticks, 10)
			}
		}
		sessions = append(sessions, TerminalSession{
			Multiplexer: TerminalMultiplexerScreen, Name: name, User: config.User, State: state,
			Identity: identity, PIDs: positivePID(pid),
		})
	}
	return sortedTerminalSessions(sessions)
}

func parseScreenSessionsResult(config TerminalSessionConfig, output string) ([]TerminalSession, error) {
	return parseScreenSessions(config, output), nil
}

func positivePID(pid int) []int {
	if pid <= 0 {
		return nil
	}
	return []int{pid}
}

// CloseTerminalSession re-lists one configured multiplexer namespace and
// closes only an exact, unchanged session through that multiplexer's own
// client. No shell or process signal is involved.
func CloseTerminalSession(ctx context.Context, runner execx.Runner, config TerminalSessionConfig, want TerminalSession) error {
	if want.Multiplexer != config.Multiplexer || want.User != config.User || want.Name == "" || want.Identity == "" {
		return errors.New("invalid terminal session target")
	}
	sample, err := sampleTerminalSessions(ctx, runner, config)
	if err != nil {
		return fmt.Errorf("refresh %s session: %w", config.Multiplexer, err)
	}
	var current *TerminalSession
	for i := range sample.Sessions {
		if sample.Sessions[i].Name == want.Name && sample.Sessions[i].User == want.User {
			current = &sample.Sessions[i]
			break
		}
	}
	if current == nil {
		return errors.New("terminal session is no longer active; refresh and try again")
	}
	if current.Identity == "" || current.Identity != want.Identity {
		return errors.New("terminal session changed; refresh and try again")
	}
	adapter, _ := terminalMultiplexerAdapterFor(config.Multiplexer)
	result, err := execx.RunUser(ctx, execx.RunnerOrDefault(runner), execx.NoTimeout, config.User, config.Binary, adapter.closeArgs(config, *current)...)
	if result.ExitCode == execx.ExitCodeRunFailure {
		if err == nil {
			err = errors.New(execx.CommandDidNotStart)
		}
		return fmt.Errorf("close %s session %q: %w", config.Multiplexer, want.Name, err)
	}
	if err != nil {
		return fmt.Errorf("close %s session %q: %w", config.Multiplexer, want.Name, err)
	}
	if result.ExitCode != execx.ExitCodeSuccess {
		return fmt.Errorf("close %s session %q: exit %d", config.Multiplexer, want.Name, result.ExitCode)
	}
	return nil
}

// CloseEmptyTmuxServer re-lists one explicitly configured tmux namespace and
// closes its server only when it is still present and has no sessions. tmux
// removes its own socket as part of kill-server; Sermo never unlinks a live
// socket directly.
func CloseEmptyTmuxServer(ctx context.Context, runner execx.Runner, config TerminalSessionConfig) error {
	if config.Multiplexer != TerminalMultiplexerTmux || config.Socket == "" {
		return errors.New("empty terminal source close requires a configured tmux socket")
	}
	sample, err := sampleTerminalSessions(ctx, runner, config)
	if err != nil {
		return fmt.Errorf("refresh tmux server: %w", err)
	}
	if !sample.Present {
		return errors.New("tmux server is no longer active; refresh and try again")
	}
	if len(sample.Sessions) != 0 {
		return errors.New("tmux server has active sessions; refresh and try again")
	}
	result, err := execx.RunUser(ctx, execx.RunnerOrDefault(runner), execx.NoTimeout, config.User, config.Binary, tmuxServerCloseArgs(config)...)
	if result.ExitCode == execx.ExitCodeRunFailure {
		if err == nil {
			err = errors.New(execx.CommandDidNotStart)
		}
		return fmt.Errorf("close empty tmux server: %w", err)
	}
	if err != nil {
		return fmt.Errorf("close empty tmux server: %w", err)
	}
	if result.ExitCode != execx.ExitCodeSuccess {
		return fmt.Errorf("close empty tmux server: exit %d", result.ExitCode)
	}
	verified, err := sampleTerminalSessions(ctx, runner, config)
	if err != nil {
		return fmt.Errorf("verify empty tmux server close: %w", err)
	}
	if verified.Present {
		return errors.New("tmux server remains active after close")
	}
	return nil
}

func sortedTerminalSessions(sessions []TerminalSession) []TerminalSession {
	slices.SortFunc(sessions, func(a, b TerminalSession) int {
		if byName := strings.Compare(a.Name, b.Name); byName != 0 {
			return byName
		}
		return strings.Compare(a.User, b.User)
	})
	return sessions
}

func terminalSessionCounts(sessions []TerminalSession) (attached, detached int) {
	for _, session := range sessions {
		switch session.State {
		case TerminalSessionStateAttached:
			attached++
		case TerminalSessionStateDetached:
			detached++
		}
	}
	return attached, detached
}

func terminalSessionMessage(config TerminalSessionConfig, total, attached, detached int) string {
	return fmt.Sprintf("%d %s session(s) for %s (%d attached, %d detached)", total, config.Multiplexer, config.User, attached, detached)
}
