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

	tmuxSessionFormat = "#{session_name}\t#{session_attached}\t#{session_windows}"
)

const (
	tmuxSessionNameField = iota
	tmuxSessionAttachedField
	tmuxSessionWindowsField
	tmuxSessionFieldCount
)

const (
	screenSessionNameMatch  = 1
	screenSessionStateMatch = 2
	screenSessionMatchCount = screenSessionStateMatch + 1
)

var screenSessionLine = regexp.MustCompile(`^\s*(\d+\.\S+)\s+\(([^)]+)\)`)

// TerminalSession is one tmux or screen session reported by its own client.
// It is observational data only: Sermo does not use it to control a process.
type TerminalSession struct {
	Multiplexer string `json:"multiplexer"`
	Name        string `json:"name"`
	User        string `json:"user"`
	State       string `json:"state"`
	Windows     int    `json:"windows,omitempty"`
}

// TerminalSessionSample is the read-only result of one configured terminal
// multiplexer query.
type TerminalSessionSample struct {
	Sessions []TerminalSession
}

// TerminalSessionConfig identifies the one terminal multiplexer namespace to
// query. The explicit user prevents a root daemon from silently enumerating
// every user's private terminal sessions.
type TerminalSessionConfig struct {
	Multiplexer string
	Binary      string
	User        string
	Socket      string
}

type terminalMultiplexerAdapter struct {
	args           func(TerminalSessionConfig) []string
	sessionsAbsent func(string) bool
	parseSessions  func(string, string) ([]TerminalSession, error)
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
			sessionsAbsent: tmuxSessionsAbsent,
			parseSessions:  parseTmuxSessions,
		}, true
	case TerminalMultiplexerScreen:
		return terminalMultiplexerAdapter{
			args:           screenSessionArgs,
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
	return TerminalSession{Multiplexer: multiplexer, Name: name, User: user, State: state, Windows: max(windows, 0)}, true
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
		res := c.unavailableResult("terminal sessions: "+err.Error(), start)
		res.Data = map[string]any{DataKeySampleError: err.Error()}
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
	output := strings.TrimSpace(result.Stdout + "\n" + result.Stderr)
	if adapter.sessionsAbsent(output) {
		return TerminalSessionSample{}, nil
	}
	if err != nil {
		return TerminalSessionSample{}, fmt.Errorf("list %s sessions for user %q: %w", config.Multiplexer, config.User, err)
	}
	if result.ExitCode != execx.ExitCodeSuccess {
		return TerminalSessionSample{}, fmt.Errorf("list %s sessions for user %q: exit %d", config.Multiplexer, config.User, result.ExitCode)
	}
	sessions, err := adapter.parseSessions(config.User, result.Stdout)
	if err != nil {
		return TerminalSessionSample{}, err
	}
	return TerminalSessionSample{Sessions: sessions}, nil
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

func tmuxSessionsAbsent(output string) bool {
	output = strings.ToLower(output)
	return strings.Contains(output, "no server running on") ||
		(strings.Contains(output, "error connecting to") && strings.Contains(output, "no such file or directory"))
}

func screenSessionsAbsent(output string) bool {
	return strings.Contains(strings.ToLower(output), "no sockets found in")
}

func parseTmuxSessions(user, output string) ([]TerminalSession, error) {
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
		state := TerminalSessionStateDetached
		if attachedClients > 0 {
			state = TerminalSessionStateAttached
		}
		sessions = append(sessions, TerminalSession{Multiplexer: TerminalMultiplexerTmux, Name: strings.TrimSpace(parts[tmuxSessionNameField]), User: user, State: state, Windows: windows})
	}
	return sortedTerminalSessions(sessions), nil
}

func parseScreenSessions(user, output string) []TerminalSession {
	sessions := make([]TerminalSession, 0)
	for line := range strings.SplitSeq(output, "\n") {
		matches := screenSessionLine.FindStringSubmatch(line)
		if len(matches) != screenSessionMatchCount {
			continue
		}
		state := TerminalSessionStateUnknown
		switch strings.ToLower(strings.TrimSpace(matches[screenSessionStateMatch])) {
		case TerminalSessionStateAttached:
			state = TerminalSessionStateAttached
		case TerminalSessionStateDetached:
			state = TerminalSessionStateDetached
		}
		sessions = append(sessions, TerminalSession{Multiplexer: TerminalMultiplexerScreen, Name: matches[screenSessionNameMatch], User: user, State: state})
	}
	return sortedTerminalSessions(sessions)
}

func parseScreenSessionsResult(user, output string) ([]TerminalSession, error) {
	return parseScreenSessions(user, output), nil
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
