package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
)

// watchProcessKiller is the optional backend capability behind the
// unowned_processes kill route.
type watchProcessKiller interface {
	KillWatchProcess(ctx context.Context, watch string, req WatchProcessKillRequest) ActionResult
}

// parseProcessIdentity reads the {pid} path value and the start_ticks query of
// a route that signals one displayed process. Both must be positive: the pair
// is the identity the backend re-verifies, so a missing or zero start ticks
// could only ever name a recycled PID. subject names the row in the error.
func parseProcessIdentity(r *http.Request, subject string) (pid int, startTicks uint64, err error) {
	pid, err = strconv.Atoi(r.PathValue(apiParamPID))
	if err != nil || pid <= 0 {
		return 0, 0, errors.New("invalid " + subject + " pid")
	}
	startTicks, err = strconv.ParseUint(r.URL.Query().Get(APIQueryStartTicks), 10, 64)
	if err != nil || startTicks == 0 {
		return 0, 0, errors.New("invalid " + subject + " " + APIQueryStartTicks)
	}
	return pid, startTicks, nil
}

// handleWatchProcessKill accepts the pid and start ticks a preceding watch read
// displayed. The backend re-reads the process immediately before signalling and
// requires that exact incarnation plus the watch's own kill authorization, so
// these request values never authorize a stale or recycled PID on their own.
func (s *Server) handleWatchProcessKill(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.mutationBackend(w, r)
	if !ok {
		return
	}
	killer, ok := backend.(watchProcessKiller)
	if !ok {
		writeError(w, http.StatusNotImplemented, "process kill is not available")
		return
	}
	pid, startTicks, err := parseProcessIdentity(r, "watch process")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req := WatchProcessKillRequest{PID: pid, StartTicks: startTicks}
	if value := r.URL.Query().Get(APIQueryEscalate); value != "" {
		req.Escalate, err = strconv.ParseBool(value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid "+APIQueryEscalate+" value")
			return
		}
	}
	s.operate(w, backend, func(ctx context.Context, _ Backend) (bool, any) {
		res := killer.KillWatchProcess(ctx, r.PathValue(apiParamName), req)
		return res.OK, res
	})
}
