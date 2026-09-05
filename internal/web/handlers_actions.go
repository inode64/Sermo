package web

import (
	"context"
	"net/http"

	"sermo/internal/operation"
)

// monitorActions and watchOperateActions are the non-operation action verbs the API accepts.
var monitorActions = map[string]bool{apiActionMonitor: true, apiActionUnmonitor: true}

var watchOperateActions = map[string]bool{apiActionExpand: true, apiActionProbe: true, apiActionPause: true, apiActionResume: true, apiActionReplicationStart: true}

// handlePanic enables (action "on") or disables (action "off") the daemon-wide
// panic mode. It is admin-only (POST gated by withAuth) and CSRF-protected.
func (s *Server) handlePanic(w http.ResponseWriter, r *http.Request) {
	var on bool
	switch r.PathValue(apiParamAction) {
	case apiActionPanicOn:
		on = true
	case apiActionPanicOff:
		on = false
	default:
		writeJSON(w, http.StatusBadRequest, ActionResult{OK: false, Message: apiErrorPanicAction})
		return
	}
	backend, _, ok := s.backendRead(w)
	if !ok {
		return
	}
	s.operate(w, r, backend, func(ctx context.Context, backend Backend) (bool, any) {
		res := backend.SetPanic(ctx, on)
		return res.OK, res
	})
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.mutationBackend(w, r)
	if !ok {
		return
	}
	name := r.PathValue(apiParamName)
	action := r.PathValue(apiParamAction)
	switch {
	case operation.IsServiceAction(action):
		s.extendActionWriteDeadline(w)
		opts := OperateOpts{NoCascade: queryBool(r, apiQueryNoCascade)}
		s.operate(w, r, backend, func(ctx context.Context, backend Backend) (bool, any) {
			res := backend.Operate(ctx, name, action, opts)
			return res.OK, res
		})
	case action == apiActionReap:
		s.extendActionWriteDeadline(w)
		s.operate(w, r, backend, func(ctx context.Context, backend Backend) (bool, any) {
			res := backend.ReapStrays(ctx, name)
			return res.OK, res
		})
	case monitorActions[action]:
		err := backend.SetMonitored(r.Context(), name, action == apiActionMonitor)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ActionResult{OK: true})
	default:
		writeError(w, http.StatusBadRequest, apiErrorUnknownActionPrefix+action)
	}
}

// handleServiceButton runs one configured operator button. Buttons are
// explicit admin commands, so the route shares the mutation gates (admin,
// CSRF, generation) with every other action.
func (s *Server) handleServiceButton(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.mutationBackend(w, r)
	if !ok {
		return
	}
	s.extendActionWriteDeadline(w)
	s.operate(w, r, backend, func(ctx context.Context, backend Backend) (bool, any) {
		res := backend.ServiceButton(ctx, r.PathValue(apiParamName), r.PathValue(apiParamButton))
		return res.OK, res
	})
}

func (s *Server) handleWatchAction(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.mutationBackend(w, r)
	if !ok {
		return
	}
	name := r.PathValue(apiParamName)
	action := r.PathValue(apiParamAction)
	if watchOperateActions[action] {
		s.extendActionWriteDeadline(w)
		s.operate(w, r, backend, func(ctx context.Context, backend Backend) (bool, any) {
			var res ActionResult
			switch action {
			case apiActionExpand:
				res = backend.ExpandWatch(ctx, name)
			case apiActionProbe:
				res = backend.ProbeWatch(ctx, name)
			case apiActionReplicationStart:
				res = backend.ControlReplication(ctx, name)
			default:
				res = backend.ControlRAID(ctx, name, action, r.Header.Get(headerSermoConfirm))
			}
			return res.OK, res
		})
		return
	}
	if !monitorActions[action] {
		writeError(w, http.StatusBadRequest, apiErrorUnknownActionPrefix+action)
		return
	}
	if err := backend.SetWatchMonitored(r.Context(), name, action == apiActionMonitor); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ActionResult{OK: true})
}

func (s *Server) handleReload(w http.ResponseWriter, _ *http.Request) {
	if s.Reload == nil {
		writeError(w, http.StatusServiceUnavailable, apiErrorReloadUnavailable)
		return
	}
	if err := s.Reload(); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ActionResult{OK: true, Message: apiMessageReloadRequested})
}
