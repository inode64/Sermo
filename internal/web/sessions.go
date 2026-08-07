package web

import (
	"context"
	"net/http"
	"strconv"
)

// SSHSession is one current interactive terminal attributed to the selected
// SSH service. CanClose is true only when a per-session process boundary and
// start-time identity were observed and can therefore be revalidated safely.
type SSHSession struct {
	Service     string `json:"service"`
	User        string `json:"user"`
	Terminal    string `json:"terminal"`
	PID         int    `json:"pid,omitempty"`
	StartTicks  uint64 `json:"start_ticks,omitempty"`
	IdleSeconds int64  `json:"idle_seconds,omitempty"`
	CanClose    bool   `json:"can_close"`
	SessionUsage
}

// SessionUsage is the process-tree resource sample shared by SSH, tmux and
// screen session rows. Ready fields distinguish measured zeroes from missing
// samples.
type SessionUsage struct {
	RSS         int64   `json:"rss,omitempty"`
	MemoryReady bool    `json:"memory_ready"`
	CPU         float64 `json:"cpu,omitempty"`
	CPUReady    bool    `json:"cpu_ready"`
	IORead      float64 `json:"io_read,omitempty"`
	IOWrite     float64 `json:"io_write,omitempty"`
	IOReady     bool    `json:"io_ready"`
}

// TerminalSession is one read-only tmux or screen session from a configured
// terminal_sessions check.
type TerminalSession struct {
	Service     string `json:"service"`
	Check       string `json:"check"`
	Multiplexer string `json:"multiplexer"`
	Name        string `json:"name"`
	User        string `json:"user"`
	State       string `json:"state"`
	Windows     int    `json:"windows,omitempty"`
	IdleSeconds int64  `json:"idle_seconds,omitempty"`
	HasIdle     bool   `json:"has_idle"`
	SessionUsage
	Identity     string `json:"identity,omitempty"`
	CanClose     bool   `json:"can_close"`
	PIDs         []int  `json:"-"`
	ActivityUnix int64  `json:"-"`
	TTY          string `json:"-"`
}

const (
	// SessionKindSSH identifies an interactive SSH terminal source.
	SessionKindSSH = "ssh"
	// SessionKindTmux identifies a tmux terminal-session source.
	SessionKindTmux = "tmux"
	// SessionKindScreen identifies a GNU screen terminal-session source.
	SessionKindScreen = "screen"
	// SessionSourceAvailable means the source completed its latest sample.
	SessionSourceAvailable = "available"
	// SessionSourceCollecting means no current sample has been published yet.
	SessionSourceCollecting = "collecting"
	// SessionSourceUnavailable means the latest sample could not be collected.
	SessionSourceUnavailable = "unavailable"
)

func isTerminalSessionKind(kind string) bool {
	return kind == SessionKindTmux || kind == SessionKindScreen
}

// SessionSource describes one configured SSH, tmux or screen inventory source,
// including an explicit empty or unavailable state.
type SessionSource struct {
	Kind    string `json:"kind"`
	Service string `json:"service"`
	Check   string `json:"check,omitempty"`
	User    string `json:"user,omitempty"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

// SessionInventory is the dashboard-wide view of interactive sessions.
type SessionInventory struct {
	Sources  []SessionSource   `json:"sources"`
	SSH      []SSHSession      `json:"ssh"`
	Terminal []TerminalSession `json:"terminal"`
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	s.readJSON(w, r, func(ctx context.Context, backend Backend) any {
		if source, ok := backend.(sessionInventorySource); ok {
			return source.Sessions(ctx)
		}
		return SessionInventory{}
	})
}

// handleSSHSessionClose accepts only a fully identified session displayed by a
// preceding detail read. The backend re-discovers it immediately before SIGTERM
// so these request values never authorize a stale or recycled PID on their own.
func (s *Server) handleSSHSessionClose(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.mutationBackend(w, r)
	if !ok {
		return
	}
	pid, err := strconv.Atoi(r.PathValue(apiParamPID))
	if err != nil || pid <= 0 {
		writeError(w, http.StatusBadRequest, "invalid SSH session pid")
		return
	}
	startTicks, err := strconv.ParseUint(r.URL.Query().Get(apiQueryStartTicks), 10, 64)
	if err != nil || startTicks == 0 {
		writeError(w, http.StatusBadRequest, "invalid SSH session start_ticks")
		return
	}
	terminal := r.URL.Query().Get(apiQueryTerminal)
	if terminal == "" {
		writeError(w, http.StatusBadRequest, "SSH session terminal is required")
		return
	}
	s.extendActionWriteDeadline(w)
	//nolint:contextcheck // see operateContext
	res := backend.CloseSSHSession(s.operateContext(r), r.PathValue(apiParamName), SSHSession{
		PID:        pid,
		StartTicks: startTicks,
		Terminal:   terminal,
	})
	writeActionResult(w, res.OK, res)
}

// handleTerminalSessionClose accepts the opaque generation marker displayed
// by the inventory. The backend re-lists the configured namespace and requires
// that exact identity before invoking the multiplexer client.
func (s *Server) handleTerminalSessionClose(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.mutationBackend(w, r)
	if !ok {
		return
	}
	session := TerminalSession{
		Check: r.PathValue(apiQueryCheck), Multiplexer: r.URL.Query().Get(apiQueryMultiplexer),
		Name: r.URL.Query().Get(apiQuerySession), User: r.URL.Query().Get(apiQueryUser),
		Identity: r.URL.Query().Get(apiQueryIdentity),
	}
	if session.Check == "" || session.Name == "" || session.User == "" || session.Identity == "" ||
		!isTerminalSessionKind(session.Multiplexer) {
		writeError(w, http.StatusBadRequest, "invalid terminal session identity")
		return
	}
	s.extendActionWriteDeadline(w)
	//nolint:contextcheck // see operateContext
	res := backend.CloseTerminalSession(s.operateContext(r), r.PathValue(apiParamName), session)
	writeActionResult(w, res.OK, res)
}
