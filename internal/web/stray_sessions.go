package web

import (
	"context"
	"net/http"
)

// StraySession is an unclaimed service process, not necessarily an abandoned
// session. A deleted executable is shown even when its client is uninstalled.
type StraySession struct {
	Service     string           `json:"service"`
	PID         int              `json:"pid"`
	PPID        int              `json:"ppid"`
	StartTicks  uint64           `json:"start_ticks"`
	User        string           `json:"user"`
	Exe         string           `json:"exe,omitempty"`
	ExePrevious string           `json:"exe_previous,omitempty"`
	Identity    string           `json:"identity"`
	CanClose    bool             `json:"can_close"`
	CloseReason string           `json:"close_reason,omitempty"`
	Terminal    *TerminalSession `json:"terminal,omitempty"`
	IdleSeconds int64            `json:"idle_seconds,omitempty"`
	HasIdle     bool             `json:"has_idle"`
	SessionUsage
}

type selectedReaper interface {
	ReapProcess(ctx context.Context, service string, target StraySession) ActionResult
}

const routeAPIStrayReap = routeMethodPost + APIPathServices + "/" + routeVarName + "/strays/{pid}/reap"

func (s *Server) handleStrayReap(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.mutationBackend(w, r)
	if !ok {
		return
	}
	pid, ticks, err := parseProcessIdentity(r, "stray process")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	identity := r.URL.Query().Get(APIQueryIdentity)
	if identity == "" {
		writeError(w, http.StatusBadRequest, "stray process identity is required")
		return
	}
	s.operate(w, backend, func(ctx context.Context, backend Backend) (bool, any) {
		reaper, supported := backend.(selectedReaper)
		if !supported {
			return false, ActionResult{Message: "selected reap is unavailable"}
		}
		result := reaper.ReapProcess(ctx, r.PathValue(apiParamName), StraySession{PID: pid, StartTicks: ticks, Identity: identity})
		return result.OK, result
	})
}
