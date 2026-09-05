package web

import (
	"context"
	"net/http"

	"sermo/internal/mountctl"
)

func (s *Server) handleMounts(w http.ResponseWriter, r *http.Request) {
	s.readJSON(w, r, func(ctx context.Context, backend Backend) any {
		mounts := backend.Mounts(ctx)
		if roleFrom(ctx) == roleGuest {
			mounts = redactMountCmdlines(mounts)
		}
		return mounts
	})
}

func (s *Server) handleMountAction(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.mutationBackend(w, r)
	if !ok {
		return
	}
	name := r.PathValue(apiParamName)
	action := r.PathValue(apiParamAction)
	switch action {
	case mountctl.ActionMount, mountctl.ActionUmount:
		s.extendActionWriteDeadline(w)
		s.operate(w, r, backend, func(ctx context.Context, backend Backend) (bool, any) {
			res := backend.MountAction(ctx, name, action, MountActionOptions{
				AllowForce:   queryBool(r, apiQueryForce),
				AllowLazy:    queryBool(r, apiQueryLazy),
				KillBlockers: queryBool(r, apiQueryKill),
			})
			return res.OK, res
		})
	case apiActionAlert:
		s.extendActionWriteDeadline(w)
		s.operate(w, r, backend, func(ctx context.Context, backend Backend) (bool, any) {
			res := backend.AlertMountUsers(ctx, name)
			return res.OK, res
		})
	default:
		writeError(w, http.StatusBadRequest, apiErrorUnknownMountActionPrefix+action)
	}
}

func (s *Server) handleMountBlockers(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue(apiParamName)
	s.readJSON(w, r, func(ctx context.Context, backend Backend) any {
		res := backend.MountBlockers(ctx, name)
		if roleFrom(ctx) == roleGuest {
			res.Blockers = redactBlockerCmdlines(res.Blockers)
		}
		return res
	})
}
