package web

import (
	"net/http"
	"strings"
	"time"
)

type accessStatusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *accessStatusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.AccessLog == nil || r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		rec := accessStatusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(&rec, r)
		s.recordWebAccess(r, rec.status)
	})
}

func (s *Server) recordWebAccess(r *http.Request, status int) {
	if s == nil || s.AccessLog == nil || r == nil {
		return
	}
	target, action := parseAPIAccessTarget(r.URL.Path)
	actor := roleFrom(r.Context())
	if actor == "" {
		actor = "anonymous"
	}
	_ = s.AccessLog.Write(map[string]any{
		"time":   time.Now().UTC().Format(time.RFC3339),
		"source": "web",
		"actor":  actor,
		"method": r.Method,
		"path":   r.URL.Path,
		"status": status,
		"target": target,
		"action": action,
	})
}

func parseAPIAccessTarget(path string) (target, action string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[0] != "api" {
		return "", ""
	}
	switch parts[1] {
	case "services", "watches":
		if len(parts) >= 3 {
			target = parts[2]
		}
		if len(parts) >= 4 {
			action = parts[3]
		}
	case "locks":
		if len(parts) >= 3 {
			target = parts[2]
			action = apiActionRelease
		}
	case "events":
		if len(parts) >= 3 {
			action = parts[2]
		} else {
			action = apiActionClear
		}
	case "state":
		if len(parts) >= 3 {
			action = parts[2]
		} else {
			action = apiActionCompact
		}
	case "panic":
		if len(parts) >= 3 {
			action = parts[2]
		}
	case "reload":
		action = apiActionReload
	default:
		if len(parts) >= 3 {
			action = parts[2]
		}
	}
	return target, action
}
