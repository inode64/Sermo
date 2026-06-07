// Package web serves a small read-and-act dashboard for the daemon: it lists the
// monitored services with their status and lets an operator monitor/unmonitor and
// start/stop/restart them. It is deliberately minimal and depends on the daemon
// only through the Backend interface, so it stays decoupled and testable.
//
// It performs no authentication, so it must bind to a trusted interface
// (loopback by default); expose it only behind an authenticating reverse proxy.
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
	Monitored   bool   `json:"monitored"`
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
	Ran      bool   `json:"ran"` // false if not observed yet
}

// SLAWindow is a service's availability over one rolling window. Ratio is nil
// when the window has no data.
type SLAWindow struct {
	Window string   `json:"window"`
	Ratio  *float64 `json:"ratio"`
	Up     int64    `json:"up"`
	Total  int64    `json:"total"`
}

// Detail is a single service's view: its summary plus its checks and SLA.
type Detail struct {
	Service
	Checks []Check     `json:"checks"`
	SLA    []SLAWindow `json:"sla"`
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

// Server is the HTTP dashboard. Addr is a host:port; Backend is required. Auth is
// optional (zero value = open).
type Server struct {
	Addr    string
	Backend Backend
	Auth    Auth
	Logger  *slog.Logger

	started time.Time // when the server began serving; for /livez uptime
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
	mux.HandleFunc("GET /api/whoami", s.handleWhoami)
	mux.HandleFunc("GET /api/services", s.handleServices)
	mux.HandleFunc("GET /api/services/{name}", s.handleDetail)
	mux.HandleFunc("GET /api/services/{name}/sla", s.handleSeries)
	mux.HandleFunc("GET /api/services/{name}/metrics", s.handleMetrics)
	mux.HandleFunc("GET /api/services/{name}/events", s.handleServiceEvents)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/diagnostics", s.handleDiagnostics)
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

// Run serves until ctx is cancelled, then shuts down gracefully. Timeouts bound
// slow clients (the server runs as root, so it is hardened by default).
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
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
		writeJSON(w, http.StatusNotFound, ActionResult{OK: false, Message: "unknown service"})
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

// handleLivez is the liveness probe: if the daemon's web server can answer, the
// process is alive, so it always returns 200. Plain requests get "ok"; `?verbose`
// returns JSON with uptime, the number of services and the runtime version. It is
// served without authentication (see withAuth) so probes need no credentials.
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
		res := s.Backend.Operate(r.Context(), name, action)
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
