package web

import (
	"context"
	"net/http"
	"strconv"

	"sermo/internal/checks"
)

// SSHSession is one current interactive terminal attributed to the selected
// SSH service. CanClose is true only when a per-session process boundary and
// start-time identity were observed and can therefore be revalidated safely.
type SSHSession struct {
	Service         string `json:"service"`
	User            string `json:"user"`
	Terminal        string `json:"terminal"`
	PID             int    `json:"pid,omitempty"`
	StartTicks      uint64 `json:"start_ticks,omitempty"`
	IdleSeconds     int64  `json:"idle_seconds,omitempty"`
	CanClose        bool   `json:"can_close"`
	ManagedByLogind bool   `json:"managed_by_logind,omitempty"`
	Residual        bool   `json:"residual,omitempty"`
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

// TerminalSession is one tmux or screen session from a configured
// terminal_sessions check. CanClose requires an exact generation identity.
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
	PIDs         []int  `json:"pids,omitempty"`
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
	// SessionSourcePartial means verified sessions were collected while one or
	// more terminals could not be attributed safely.
	SessionSourcePartial = "partial"
)

func isTerminalSessionKind(kind string) bool {
	return kind == SessionKindTmux || kind == SessionKindScreen
}

// SessionSource describes one configured SSH, tmux or screen inventory source,
// including an explicit empty or unavailable state.
type SessionSource struct {
	Kind          string         `json:"kind"`
	Service       string         `json:"service"`
	Check         string         `json:"check,omitempty"`
	User          string         `json:"user,omitempty"`
	State         string         `json:"state"`
	Message       string         `json:"message,omitempty"`
	Issues        []SessionIssue `json:"issues,omitempty"`
	CanCloseEmpty bool           `json:"can_close_empty"`
}

// SessionIssue is one terminal whose SSH process ancestry cannot be attributed.
// A systemd host may still offer a login1-managed close after independently
// verifying the exact remote session identity.
type SessionIssue struct {
	User            string `json:"user"`
	Terminal        string `json:"terminal"`
	Message         string `json:"message"`
	PID             int    `json:"pid,omitempty"`
	StartTicks      uint64 `json:"start_ticks,omitempty"`
	CanClose        bool   `json:"can_close"`
	ManagedByLogind bool   `json:"managed_by_logind,omitempty"`
}

// DBQuerySession is one statement a database server is running, listed by a
// db_queries watch. CanKill requires a service watch (the kill runs through the
// service's operation engine) and an exact statement identity.
type DBQuerySession struct {
	Service string `json:"service,omitempty"`
	Watch   string `json:"watch"`
	Engine  string `json:"engine"`
	ID      int64  `json:"id"`
	QueryID int64  `json:"query_id,omitempty"`
	// OSThreadID is a MySQL/MariaDB OS thread ID or a PostgreSQL backend PID,
	// as reported by the database; it is not the connection ID.
	OSThreadID     int64  `json:"os_tid,omitempty"`
	At             string `json:"at"` // RFC3339 sample time
	User           string `json:"user"`
	Host           string `json:"host,omitempty"`
	Database       string `json:"database,omitempty"`
	Command        string `json:"command,omitempty"`
	State          string `json:"state,omitempty"`
	ElapsedSeconds int64  `json:"elapsed_seconds"`
	Query          string `json:"query"`
	Truncated      bool   `json:"truncated,omitempty"`
	Long           bool   `json:"long"`
	Matched        bool   `json:"matched"`
	Alerted        bool   `json:"alerted"`
	Identity       string `json:"identity,omitempty"`
	// DisplayIdentity preserves row expansion across samples of one statement.
	// Identity, not this presentation key, authorizes a cancellation request.
	DisplayIdentity string `json:"display_identity,omitempty"`
	CanKill         bool   `json:"can_kill"`
	// Stopping marks a statement the server is already stopping (cancelled
	// and rolling back), which is why it cannot be killed again.
	Stopping bool `json:"stopping,omitempty"`
	// CPUThread is the statement thread's CPU against one logical CPU;
	// SessionUsage.CPU is its share of the host, and CPUReady covers both.
	CPUThread float64 `json:"cpu_thread,omitempty"`
	// SessionUsage is the statement's CPU, memory and IO, in the same shape as the
	// SSH and terminal rows.
	SessionUsage
}

// SessionInventory is the dashboard-wide view of interactive sessions and
// running database statements.
type SessionInventory struct {
	Sources  []SessionSource   `json:"sources"`
	SSH      []SSHSession      `json:"ssh"`
	Terminal []TerminalSession `json:"terminal"`
	Database []DBQuerySession  `json:"database"`
}

// SessionKindDatabase identifies a db_queries statement source.
const SessionKindDatabase = "database"

// DBQueryKillRequest names one listed statement to stop: the identity the
// inventory displayed and how to stop it.
type DBQueryKillRequest struct {
	Watch    string
	ID       int64
	Identity string
	Mode     string
}

// dbQueryKiller is the optional backend capability behind the kill route.
type dbQueryKiller interface {
	KillDBQuery(ctx context.Context, service string, req DBQueryKillRequest) ActionResult
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
// preceding detail read. The backend re-discovers either its trusted SSH process
// boundary or its exact login1 session immediately before closing, so these
// request values never authorize a stale or recycled PID on their own.
func (s *Server) handleSSHSessionClose(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.mutationBackend(w, r)
	if !ok {
		return
	}
	pid, startTicks, err := parseProcessIdentity(r, "SSH session")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	terminal := r.URL.Query().Get(apiQueryTerminal)
	if terminal == "" {
		writeError(w, http.StatusBadRequest, "SSH session terminal is required")
		return
	}
	managedByLogind := false
	if value := r.URL.Query().Get(apiQueryManagedByLogind); value != "" {
		managedByLogind, err = strconv.ParseBool(value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid managed_by_logind value")
			return
		}
	}
	s.operate(w, backend, func(ctx context.Context, backend Backend) (bool, any) {
		res := backend.CloseSSHSession(ctx, r.PathValue(apiParamName), SSHSession{
			PID:             pid,
			StartTicks:      startTicks,
			Terminal:        terminal,
			ManagedByLogind: managedByLogind,
		})
		return res.OK, res
	})
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
		Identity: r.URL.Query().Get(APIQueryIdentity),
	}
	if session.Check == "" || session.Name == "" || session.User == "" || session.Identity == "" ||
		!isTerminalSessionKind(session.Multiplexer) {
		writeError(w, http.StatusBadRequest, "invalid terminal session identity")
		return
	}
	s.operate(w, backend, func(ctx context.Context, backend Backend) (bool, any) {
		res := backend.CloseTerminalSession(ctx, r.PathValue(apiParamName), session)
		return res.OK, res
	})
}

// handleEmptyTerminalSessionClose closes one configured tmux server only after
// the backend freshly proves that its namespace is present and has no sessions.
func (s *Server) handleEmptyTerminalSessionClose(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.mutationBackend(w, r)
	if !ok {
		return
	}
	check := r.PathValue(apiQueryCheck)
	if check == "" {
		writeError(w, http.StatusBadRequest, "terminal session check is required")
		return
	}
	s.operate(w, backend, func(ctx context.Context, backend Backend) (bool, any) {
		res := backend.CloseEmptyTerminalSession(ctx, r.PathValue(apiParamName), check)
		return res.OK, res
	})
}

// handleDBQueryKill accepts the statement id and identity a preceding inventory
// read displayed. The backend re-samples the server through the watch's own
// configured connection and requires that exact statement before cancelling it
// (mode query) or closing its connection (mode connection).
func (s *Server) handleDBQueryKill(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.mutationBackend(w, r)
	if !ok {
		return
	}
	killer, ok := backend.(dbQueryKiller)
	if !ok {
		writeError(w, http.StatusNotImplemented, "query kill is not available")
		return
	}
	query := r.URL.Query()
	id, err := strconv.ParseInt(query.Get(APIQueryID), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid statement id")
		return
	}
	req := DBQueryKillRequest{Watch: r.PathValue(apiQueryWatch), ID: id, Identity: query.Get(APIQueryIdentity), Mode: query.Get(APIQueryMode)}
	if req.Watch == "" || req.Identity == "" {
		writeError(w, http.StatusBadRequest, "invalid statement identity")
		return
	}
	if req.Mode == "" {
		req.Mode = checks.DBQueryKillModeQuery
	}
	if !checks.ValidDBQueryKillMode(req.Mode) {
		writeError(w, http.StatusBadRequest, "mode must be "+checks.DBQueryKillModeSummary)
		return
	}
	s.operate(w, backend, func(ctx context.Context, _ Backend) (bool, any) {
		res := killer.KillDBQuery(ctx, r.PathValue(apiParamName), req)
		return res.OK, res
	})
}
