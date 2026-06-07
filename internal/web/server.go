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
	"log/slog"
	"net/http"
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

// Backend is what the web server needs from the daemon.
type Backend interface {
	// Services returns the current view of every monitored service.
	Services(ctx context.Context) []Service
	// Detail returns one service's checks and SLA; ok is false for unknown names.
	Detail(ctx context.Context, name string) (Detail, bool)
	// Operate runs start|stop|restart on a service through the safe engine.
	Operate(ctx context.Context, name, action string) ActionResult
	// SetMonitored pauses (false) or resumes (true) monitoring of a service.
	SetMonitored(ctx context.Context, name string, monitored bool) error
}

// operateActions and monitorActions are the action verbs the API accepts.
var operateActions = map[string]bool{"start": true, "stop": true, "restart": true}
var monitorActions = map[string]bool{"monitor": true, "unmonitor": true}

// Server is the HTTP dashboard. Addr is a host:port; Backend is required.
type Server struct {
	Addr    string
	Backend Backend
	Logger  *slog.Logger
}

// Handler returns the router: the dashboard at /, the service list at
// /api/services, and POST /api/services/{name}/{action} for actions.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /api/services", s.handleServices)
	mux.HandleFunc("GET /api/services/{name}", s.handleDetail)
	mux.HandleFunc("POST /api/services/{name}/{action}", s.handleAction)
	return mux
}

// Run serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{Addr: s.Addr, Handler: s.Handler()}
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
