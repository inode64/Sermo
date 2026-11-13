package web

import (
	"context"
	"net/http"

	"sermo/internal/checks"
)

// SessionKindSMB identifies Samba worker session sources.
const SessionKindSMB = "smb"

// SMBSession accounts for one worker, including every login that closing it affects.
type SMBSession struct {
	checks.SMBSession
	Service  string `json:"service"`
	Check    string `json:"check"`
	CanClose bool   `json:"can_close"`
	Message  string `json:"message,omitempty"`
	SessionUsage
}

type smbSessionCloser interface {
	CloseSMBSession(ctx context.Context, service, check, identity string) ActionResult
}

func (s *Server) handleSMBSessionClose(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.mutationBackend(w, r)
	if !ok {
		return
	}
	closer, ok := backend.(smbSessionCloser)
	if !ok {
		writeError(w, http.StatusNotImplemented, "SMB session close is unavailable")
		return
	}
	check, identity := r.PathValue(apiQueryCheck), r.URL.Query().Get(APIQueryIdentity)
	if check == "" || identity == "" {
		writeError(w, http.StatusBadRequest, "invalid SMB connection identity")
		return
	}
	s.operate(w, backend, func(ctx context.Context, _ Backend) (bool, any) {
		result := closer.CloseSMBSession(ctx, r.PathValue(apiParamName), check, identity)
		return result.OK, result
	})
}
