package web

import (
	"net/http"
	"strings"
	"time"
)

const (
	accessActorAnonymous = "anonymous"
	accessSourceWeb      = "web"
	accessFieldAction    = "action"
	accessFieldActor     = "actor"
	accessFieldMethod    = "method"
	accessFieldPath      = "path"
	accessFieldQuery     = "query"
	accessFieldSource    = "source"
	accessFieldStatus    = "status"
	accessFieldTarget    = "target"
	accessFieldTime      = "time"
)

const (
	apiAccessRootMinSegments = 2
	apiAccessRootSegment     = 0
	apiAccessResourceSegment = 1
	apiAccessTargetSegments  = 3
	apiAccessTargetSegment   = 2
	apiAccessActionSegments  = 4
	apiAccessActionSegment   = 3
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
		if s.AccessLog == nil || isReadMethod(r.Method) || !strings.HasPrefix(r.URL.Path, apiPathPrefix) {
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
		actor = s.Auth.role(r)
	}
	if actor == "" {
		actor = accessActorAnonymous
	}
	entry := map[string]any{
		accessFieldTime:   time.Now().UTC().Format(time.RFC3339),
		accessFieldSource: accessSourceWeb,
		accessFieldActor:  actor,
		accessFieldMethod: r.Method,
		accessFieldPath:   r.URL.Path,
		accessFieldStatus: status,
		accessFieldTarget: target,
		accessFieldAction: action,
	}
	// Query parameters change what an action does (umount?kill=1, clear?before=,
	// release?name=), so the audit record must keep them. CSRF travels in a
	// header, never in the query, so logging the raw string is safe.
	if q := r.URL.RawQuery; q != "" {
		entry[accessFieldQuery] = q
	}
	_ = s.AccessLog.Write(entry)
}

func parseAPIAccessTarget(path string) (target, action string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < apiAccessRootMinSegments || parts[apiAccessRootSegment] != apiSegmentRoot {
		return "", ""
	}
	switch parts[apiAccessResourceSegment] {
	case apiSegmentServices, apiSegmentWatches, apiSegmentMounts, apiSegmentNotifiers:
		if len(parts) >= apiAccessTargetSegments {
			target = parts[apiAccessTargetSegment]
		}
		if len(parts) >= apiAccessActionSegments {
			action = parts[apiAccessActionSegment]
		}
	case apiSegmentLocks:
		if len(parts) >= apiAccessTargetSegments {
			target = parts[apiAccessTargetSegment]
			action = apiActionRelease
		}
	case apiSegmentEvents:
		if len(parts) >= apiAccessTargetSegments {
			action = parts[apiAccessTargetSegment]
		} else {
			action = apiActionClear
		}
	case apiSegmentState:
		if len(parts) >= apiAccessTargetSegments {
			action = parts[apiAccessTargetSegment]
		} else {
			action = apiActionCompact
		}
	case apiSegmentPanic:
		if len(parts) >= apiAccessTargetSegments {
			action = parts[apiAccessTargetSegment]
		}
	case apiSegmentReload:
		action = apiActionReload
	default:
		if len(parts) >= apiAccessTargetSegments {
			action = parts[apiAccessTargetSegment]
		}
	}
	return target, action
}
