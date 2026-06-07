// Package web serves a small read-and-act dashboard for the daemon: it lists the
// monitored services with their status and lets an operator monitor/unmonitor and
// start/stop/restart them. It is deliberately minimal and depends on the daemon
// only through the Backend interface, so it stays decoupled and testable.
//
// Access is optional HTTP Basic auth with admin (read+act) and guest (read-only)
// roles; state-changing POST requests also require an X-Sermo-CSRF header. When
// no passwords are configured the UI is open — bind to a trusted interface
// (loopback by default) or set passwords / front it with an authenticating reverse
// proxy. GET /livez and GET /readyz are always public for health probes.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strconv"
	"time"
)

//go:embed index.html
var assets embed.FS

// Service is the web view of one monitored service.
type Service struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Backend     string `json:"backend"`
	Unit        string `json:"unit"`
	Status      string `json:"status"`
	Monitored          bool   `json:"monitored"`
	MonitorSource      string `json:"monitor_source,omitempty"`       // cli | web | config | daemon
	MonitorChangedAt   string `json:"monitor_changed_at,omitempty"`   // RFC3339 when monitoring state last changed
	CheckHealth        string `json:"check_health,omitempty"`         // ok | failing | unknown | paused
	ChecksFailing      int    `json:"checks_failing,omitempty"`       // required checks currently failing
}

// ActionResult is the outcome of an operation (start/stop/restart).
type ActionResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

// Check is one check's latest observed result in a service detail.
type Check struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	OK       bool   `json:"ok"`
	Optional bool   `json:"optional"`
	Skipped  bool   `json:"skipped,omitempty"` // gated off (requires/skip_when_changed)
	Message  string `json:"message,omitempty"`
	Ran      bool   `json:"ran"`           // false if not observed yet
	At       string `json:"at,omitempty"` // RFC3339 when the check last ran (cached checks keep prior time)
}

// SLAWindow is a service's availability over one rolling window. Ratio is nil
// when the window has no data.
type SLAWindow struct {
	Window string   `json:"window"`
	Ratio  *float64 `json:"ratio"`
	Up     int64    `json:"up"`
	Total  int64    `json:"total"`
}

// Process is a discovered process belonging to a service (parity with
// `sermoctl processes`).
type Process struct {
	PID         int      `json:"pid"`
	PPID        int      `json:"ppid"`
	User        string   `json:"user,omitempty"`
	Exe         string   `json:"exe,omitempty"`
	ExeResolved bool     `json:"exe_resolved"`
	Role        string   `json:"role,omitempty"`
	Source      string   `json:"source"`
	Cmdline     []string `json:"cmdline,omitempty"`
}

// Remediation is the automatic remediation policy gating view for one service.
type Remediation struct {
	Allowed           bool   `json:"allowed"`
	Reason            string `json:"reason,omitempty"` // cooldown | rate limit
	Cooldown          string `json:"cooldown,omitempty"`
	EffectiveCooldown string `json:"effective_cooldown,omitempty"`
	CurrentBackoff    string `json:"current_backoff,omitempty"`
	LastActionAt      string `json:"last_action_at,omitempty"`  // RFC3339
	CooldownUntil     string `json:"cooldown_until,omitempty"`  // RFC3339
	MaxActions        int    `json:"max_actions,omitempty"`
	MaxActionsWindow  string `json:"max_actions_window,omitempty"`
	RecentActions     int    `json:"recent_actions,omitempty"`
}

// Lock is a named runtime lock for one service (parity with `sermoctl locks`).
type Lock struct {
	Name        string `json:"name,omitempty"`
	Reason      string `json:"reason,omitempty"`
	State       string `json:"state"` // active | expired | stale
	OwnerPID    int    `json:"owner_pid"`
	StaleReason string `json:"stale_reason,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"` // RFC3339
	ExpiresAt   string `json:"expires_at,omitempty"` // RFC3339
}

// Detail is a single service's view: its summary plus its checks and SLA.
type Detail struct {
	Service
	Checks      []Check      `json:"checks"`
	SLA         []SLAWindow  `json:"sla"`
	Locks       []Lock       `json:"locks,omitempty"`
	Processes   []Process    `json:"processes,omitempty"`
	Remediation *Remediation `json:"remediation,omitempty"`
}

// SeriesPoint is one per-minute availability sample of the SLA history. Ratio is
// nil for a minute with no observed cycle.
type SeriesPoint struct {
	Start string   `json:"start"` // RFC3339, minute-aligned
	Ratio *float64 `json:"ratio"`
	Up    int64    `json:"up"`
	Total int64    `json:"total"`
}

// MetricPoint is one time bucket of a check's latency series (milliseconds).
type MetricPoint struct {
	Start string  `json:"start"` // RFC3339, minute-aligned
	N     int64   `json:"n"`
	Avg   float64 `json:"avg"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
}

// MetricSummary is a check's latency over the window: sample count and
// average/min/max in milliseconds (Count==0 means no data).
type MetricSummary struct {
	Count int64   `json:"count"`
	Avg   float64 `json:"avg"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
}

// MetricSeries is a check's latency history plus its summary for one window.
type MetricSeries struct {
	Check   string        `json:"check"`
	Since   string        `json:"since"`
	Unit    string        `json:"unit"`
	Summary MetricSummary `json:"summary"`
	Points  []MetricPoint `json:"points"`
}

// Finding is one diagnostic result (level: error|warning|info).
type Finding struct {
	Level   string `json:"level"`
	Scope   string `json:"scope"`
	Message string `json:"message"`
}

// OperationSlots is the global start/stop/restart concurrency pool (section 24).
type OperationSlots struct {
	InUse int `json:"in_use"`
	Total int `json:"total"`
}

// ReadyReport is the /readyz readiness probe payload.
type ReadyReport struct {
	Ready    bool   `json:"ready"`
	Status   string `json:"status"` // ok | starting | shutting_down
	Backend  string `json:"backend,omitempty"`
	Services int    `json:"services"`
	Watches  int    `json:"watches"`
	Message  string `json:"message,omitempty"`
}

// ReadinessChecker reports whether the daemon has begun monitoring.
type ReadinessChecker interface {
	Report(ctx context.Context) ReadyReport
}

// Event is one recorded daemon event for the activity log.
type Event struct {
	Time    string `json:"time"` // RFC3339
	Service string `json:"service,omitempty"`
	Watch   string `json:"watch,omitempty"`
	Kind    string `json:"kind"`
	Rule    string `json:"rule,omitempty"`
	Action  string `json:"action,omitempty"`
	Status  string `json:"status,omitempty"`
	Message string `json:"message,omitempty"`
}

// maxSeriesWindow bounds the history a single request may ask for (the retention).
const maxSeriesWindow = 366 * 24 * time.Hour

// defaultEventLimit / maxEventLimit bound how many log events a request returns.
const defaultEventLimit = 100
const maxEventLimit = 1000

// defaultSeriesWindow is used when no (or an invalid) `since` is given.
const defaultSeriesWindow = 24 * time.Hour

// Backend is what the web server needs from the daemon.
type Backend interface {
	// Services returns the current view of every monitored service.
	Services(ctx context.Context) []Service
	// Detail returns one service's checks and SLA; ok is false for unknown names.
	Detail(ctx context.Context, name string) (Detail, bool)
	// Series returns a service's per-minute availability history over since; ok is
	// false for unknown names.
	Series(ctx context.Context, name string, since time.Duration) ([]SeriesPoint, bool)
	// Metrics returns a check's latency summary and per-minute history over since;
	// ok is false for unknown service names.
	Metrics(ctx context.Context, name, check string, since time.Duration) (MetricSeries, bool)
	// Events returns up to limit recent events, newest first (the global feed).
	Events(ctx context.Context, limit int) []Event
	// Diagnostics runs config/host/database consistency checks and returns the
	// findings (ordered by severity).
	Diagnostics(ctx context.Context) []Finding
	// Operations reports how many global operation slots are in use.
	Operations(ctx context.Context) OperationSlots
	// ServiceEvents returns up to limit recent events for one service, newest
	// first; ok is false for unknown names.
	ServiceEvents(ctx context.Context, name string, limit int) ([]Event, bool)
	// Operate runs start|stop|restart on a service through the safe engine.
	Operate(ctx context.Context, name, action string) ActionResult
	// SetMonitored pauses (false) or resumes (true) monitoring of a service.
	SetMonitored(ctx context.Context, name string, monitored bool) error
}

// operateActions and monitorActions are the action verbs the API accepts.
var operateActions = map[string]bool{"start": true, "stop": true, "restart": true}
var monitorActions = map[string]bool{"monitor": true, "unmonitor": true}

// defaultOperationTimeout matches operation.DefaultOperationTimeout when sermod
// does not set OperationTimeout on the server.
const defaultOperationTimeout = 90 * time.Second

// writeTimeoutMargin is added to OperationTimeout so the handler can finish
// writing the JSON response after a long operation completes.
const writeTimeoutMargin = 5 * time.Second

// minWriteTimeout keeps short read-only requests bounded when OperationTimeout
// is unusually small.
const minWriteTimeout = 30 * time.Second

// Server is the HTTP dashboard. Addr is a host:port; Backend is required. Auth is
// optional (zero value = open). OperationTimeout bounds how long start/stop/restart
// may run and sizes the HTTP write deadline; it should be the maximum per-service
// deadline (app.MaxOperationTimeout).
type Server struct {
	Addr    string
	Backend Backend
	Auth    Auth
	Logger  *slog.Logger

	OperationTimeout time.Duration
	// Readiness is optional; nil makes /readyz report ready (tests).
	Readiness ReadinessChecker

	started  time.Time         // when the server began serving; for /livez uptime
	shutdown context.Context // daemon lifetime; set in Run
}

// Handler returns the router behind the auth middleware: the dashboard at /, the
// service list at /api/services, and POST /api/services/{name}/{action} for
// actions.
func (s *Server) Handler() http.Handler {
	if s.started.IsZero() {
		s.started = time.Now()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /livez", s.handleLivez)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /api/whoami", s.handleWhoami)
	mux.HandleFunc("GET /api/services", s.handleServices)
	mux.HandleFunc("GET /api/services/{name}", s.handleDetail)
	mux.HandleFunc("GET /api/services/{name}/sla", s.handleSeries)
	mux.HandleFunc("GET /api/services/{name}/metrics", s.handleMetrics)
	mux.HandleFunc("GET /api/services/{name}/events", s.handleServiceEvents)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/diagnostics", s.handleDiagnostics)
	mux.HandleFunc("GET /api/ops", s.handleOperations)
	mux.HandleFunc("POST /api/services/{name}/{action}", s.handleAction)
	return s.withAuth(mux)
}

// eventLimit reads the `limit` query param, defaulting and capping it.
func eventLimit(r *http.Request) int {
	limit := defaultEventLimit
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxEventLimit {
		limit = maxEventLimit
	}
	return limit
}

// csrfHeader must be present on every state-changing (POST) request. A cross-site
// HTML form cannot set a custom header, and a cross-site fetch that tries to would
// trigger a CORS preflight we never answer — so requiring it blocks CSRF against
// the (root-privileged) action endpoints, in both authenticated and open modes.
const csrfHeader = "X-Sermo-CSRF"

// serverWriteTimeout returns the HTTP write deadline for action handlers that may
// block until a safe operation finishes.
func serverWriteTimeout(maxOp time.Duration) time.Duration {
	if maxOp <= 0 {
		maxOp = defaultOperationTimeout
	}
	wt := maxOp + writeTimeoutMargin
	if wt < minWriteTimeout {
		return minWriteTimeout
	}
	return wt
}

// operateContext returns a context for start/stop/restart that is not tied to the
// HTTP request. Client disconnect and the generic write deadline must not abort
// an in-flight safe operation; the operation engine applies its own timeout.
func (s *Server) operateContext() context.Context {
	if s.shutdown != nil {
		return s.shutdown
	}
	return context.Background()
}

// Run serves until ctx is cancelled, then shuts down gracefully. Timeouts bound
// slow clients (the server runs as root, so it is hardened by default).
func (s *Server) Run(ctx context.Context) error {
	s.shutdown = ctx
	srv := &http.Server{
		Addr:              s.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      serverWriteTimeout(s.OperationTimeout),
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page, err := assets.ReadFile("index.html")
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(page)
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Backend.Services(r.Context()))
}

func (s *Server) handleDetail(w http.ResponseWriter, r *http.Request) {
	detail, ok := s.Backend.Detail(r.Context(), r.PathValue("name"))
	if !ok {
		writeJSON(w, http.StatusNotFound, ActionResult{OK: false, Message: "unknown service"})
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// seriesSince reads the `since` query param, defaulting and capping it.
func seriesSince(r *http.Request) time.Duration {
	since := defaultSeriesWindow
	if q := r.URL.Query().Get("since"); q != "" {
		if d, err := time.ParseDuration(q); err == nil && d > 0 {
			since = d
		}
	}
	if since > maxSeriesWindow {
		since = maxSeriesWindow
	}
	return since
}

func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	since := seriesSince(r)
	points, ok := s.Backend.Series(r.Context(), r.PathValue("name"), since)
	if !ok {
		writeJSON(w, http.StatusNotFound, ActionResult{OK: false, Message: "unknown service"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"since": since.String(), "points": points})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	check := r.URL.Query().Get("check")
	if check == "" {
		writeJSON(w, http.StatusBadRequest, ActionResult{OK: false, Message: "check query parameter is required"})
		return
	}
	res, ok := s.Backend.Metrics(r.Context(), r.PathValue("name"), check, seriesSince(r))
	if !ok {
		writeJSON(w, http.StatusNotFound, ActionResult{OK: false, Message: "unknown service or check"})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Backend.Events(r.Context(), eventLimit(r)))
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Backend.Diagnostics(r.Context()))
}

func (s *Server) handleOperations(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Backend.Operations(r.Context()))
}

// handleLivez is the liveness probe: if the daemon's web server can answer, the
// process is alive, so it always returns 200. Plain requests get "ok"; `?verbose`
// returns JSON with uptime, the number of services and the runtime version. It is
// served without authentication (see withAuth) so probes need no credentials.
func (s *Server) readyReport(ctx context.Context) ReadyReport {
	if s.Readiness != nil {
		return s.Readiness.Report(ctx)
	}
	return ReadyReport{
		Ready: true, Status: "ok",
		Services: len(s.Backend.Services(ctx)),
	}
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	rep := s.readyReport(r.Context())
	status := http.StatusOK
	if !rep.Ready {
		status = http.StatusServiceUnavailable
	}
	if !r.URL.Query().Has("verbose") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(status)
		if rep.Ready {
			_, _ = io.WriteString(w, "ok\n")
		} else {
			_, _ = io.WriteString(w, rep.Status+"\n")
		}
		return
	}
	writeJSON(w, status, rep)
}

func (s *Server) handleLivez(w http.ResponseWriter, r *http.Request) {
	if !r.URL.Query().Has("verbose") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
		return
	}
	now := time.Now()
	uptime := now.Sub(s.started)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"started_at":     s.started.Format(time.RFC3339),
		"now":            now.Format(time.RFC3339),
		"uptime":         uptime.Round(time.Second).String(),
		"uptime_seconds": int64(uptime.Seconds()),
		"services":       len(s.Backend.Services(r.Context())),
		"go":             runtime.Version(),
	})
}

func (s *Server) handleServiceEvents(w http.ResponseWriter, r *http.Request) {
	events, ok := s.Backend.ServiceEvents(r.Context(), r.PathValue("name"), eventLimit(r))
	if !ok {
		writeJSON(w, http.StatusNotFound, ActionResult{OK: false, Message: "unknown service"})
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	action := r.PathValue("action")
	switch {
	case operateActions[action]:
		res := s.Backend.Operate(s.operateContext(), name, action)
		status := http.StatusOK
		if !res.OK {
			status = http.StatusConflict
		}
		writeJSON(w, status, res)
	case monitorActions[action]:
		err := s.Backend.SetMonitored(r.Context(), name, action == "monitor")
		if err != nil {
			writeJSON(w, http.StatusConflict, ActionResult{OK: false, Message: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, ActionResult{OK: true})
	default:
		writeJSON(w, http.StatusBadRequest, ActionResult{OK: false, Message: "unknown action " + action})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
