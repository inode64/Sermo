package web

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sermo/internal/operation"
	"sermo/internal/state"
)

const (
	eventKindError          = "error"
	eventKindFailedFragment = string(operation.ResultFailed)
	eventStatusError        = "error"
)

// defaultEventLimit / maxEventLimit bound how many log events a request returns.
const defaultEventLimit = 100

const maxEventLimit = 1000

// queryCapped reads query param name via parse (which reports whether the
// value is usable), defaulting to def and capping at maxV; the shared clamp
// behind the limit/since query readers.
//
//nolint:ireturn // The type parameter preserves the concrete query value type for callers.
func queryCapped[T cmp.Ordered](r *http.Request, name string, def, maxV T, parse func(string) (T, bool)) T {
	v := def
	if q := r.URL.Query().Get(name); q != "" {
		if n, ok := parse(q); ok {
			v = n
		}
	}
	return min(v, maxV)
}

// eventLimit reads the `limit` query param, defaulting and capping it.
func eventLimit(r *http.Request) int {
	return queryCapped(r, apiQueryLimit, defaultEventLimit, maxEventLimit, func(q string) (int, bool) {
		n, err := strconv.Atoi(q)
		return n, err == nil && n > 0
	})
}

func parseEventQuery(r *http.Request) (EventQuery, error) {
	beforeID, err := eventBeforeID(r)
	if err != nil {
		return EventQuery{}, err
	}
	since, err := eventSince(r)
	if err != nil {
		return EventQuery{}, err
	}
	q := r.URL.Query()
	return EventQuery{
		BeforeID:   beforeID,
		Limit:      eventLimit(r),
		Since:      since,
		Service:    q.Get(apiParamService),
		Watch:      q.Get(apiQueryWatch),
		Kind:       q.Get(apiQueryKind),
		Status:     q.Get(apiQueryStatus),
		OnlyErrors: queryBool(r, apiQueryOnlyErrors),
	}, nil
}

// IsErrorEvent reports whether an event counts as an error for the
// errors-only feed filter. It is the single classification shared by the
// in-memory filter here and the store-backed pagination in the daemon backend.
func IsErrorEvent(e Event) bool {
	if e.Kind == eventKindError || strings.Contains(e.Kind, eventKindFailedFragment) {
		return true
	}
	switch e.Status {
	case string(operation.ResultFailed),
		eventStatusError,
		string(operation.ResultBlocked),
		string(operation.ResultOrphanProcesses),
		string(operation.ResultPreflightFailed),
		string(operation.ResultPostflightFailed):
		return true
	default:
		return false
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	query, err := parseEventQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	backend, generation, ok := s.backendRead(w)
	if !ok {
		return
	}
	s.writeBackendJSON(w, http.StatusOK, backend.EventPage(r.Context(), query), generation)
}

func eventSince(r *http.Request) (time.Duration, error) {
	raw := r.URL.Query().Get(apiQuerySince)
	if raw == "" {
		return 0, nil
	}
	since, err := time.ParseDuration(raw)
	if err != nil || since <= 0 {
		return 0, fmt.Errorf("bad %s: must be a positive duration", apiQuerySince)
	}
	return since, nil
}

func eventBeforeID(r *http.Request) (int64, error) {
	raw := r.URL.Query().Get(apiQueryBeforeID)
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("bad %s: must be a positive integer", apiQueryBeforeID)
	}
	return id, nil
}

// queryBool reports whether the query parameter key is set to the shared truthy
// vocabulary ("1", "true", "yes" or "on", case-insensitive).
func queryBool(r *http.Request, key string) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key))) {
	case queryBoolOne, queryBoolTrue, queryBoolYes, queryBoolOn:
		return true
	default:
		return false
	}
}

// handleEventsClear supports `sermoctl events clear [--before TIME]`.
// TIME may be a non-future RFC3339 timestamp or a positive duration (e.g. "2h"
// means "before now-2h").
func (s *Server) handleEventsClear(w http.ResponseWriter, r *http.Request) {
	before, err := state.ParseCutoff(apiQueryBefore, r.URL.Query().Get(apiQueryBefore), time.Now())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ActionResult{OK: false, Message: err.Error()})
		return
	}
	backend, _, ok := s.backendRead(w)
	if !ok {
		return
	}
	n := backend.PruneEvents(r.Context(), before)
	writeJSON(w, http.StatusOK, map[string]any{
		apiJSONKeyOK:     true,
		apiJSONKeyPruned: n,
	})
}

func (s *Server) handleStateCompact(w http.ResponseWriter, r *http.Request) {
	before, err := state.ParseCutoff(apiQueryBefore, r.URL.Query().Get(apiQueryBefore), time.Now())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, StateCompactResult{OK: false, Message: err.Error()})
		return
	}
	s.extendActionWriteDeadline(w)
	backend, _, ok := s.backendRead(w)
	if !ok {
		return
	}
	s.operate(w, r, backend, func(ctx context.Context, backend Backend) (bool, any) {
		res := backend.CompactState(ctx, before)
		return res.OK, res
	})
}

// handleNamedEvents serves one named subject's recent events. Service and
// application feeds differ only in the backend reader and the not-found message.
func (s *Server) handleNamedEvents(w http.ResponseWriter, r *http.Request, notFoundMsg string,
	events func(Backend, context.Context, string, int) ([]Event, bool),
) {
	handleNamed(s, w, r, notFoundMsg, func(backend Backend, ctx context.Context, name string) ([]Event, bool) {
		return events(backend, ctx, name, eventLimit(r))
	})
}

func (s *Server) handleServiceEvents(w http.ResponseWriter, r *http.Request) {
	s.handleNamedEvents(w, r, apiErrorUnknownService, Backend.ServiceEvents)
}

func (s *Server) handleApplicationEvents(w http.ResponseWriter, r *http.Request) {
	s.handleNamedEvents(w, r, apiErrorUnknownApplication, Backend.ApplicationEvents)
}
