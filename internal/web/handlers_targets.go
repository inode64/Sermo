package web

import (
	"context"
	"net/http"
	"slices"
)

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	s.readJSON(w, r, func(ctx context.Context, backend Backend) any { return backend.Services(ctx) })
}

func (s *Server) handleWatches(w http.ResponseWriter, r *http.Request) {
	s.readJSON(w, r, func(ctx context.Context, backend Backend) any { return backend.Watches(ctx) })
}

func (s *Server) handleNotifiers(w http.ResponseWriter, r *http.Request) {
	s.readJSON(w, r, func(ctx context.Context, backend Backend) any { return backend.Notifiers(ctx) })
}

func (s *Server) handleNotifierTest(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.mutationBackend(w, r)
	if !ok {
		return
	}
	s.extendActionWriteDeadline(w)
	s.operate(w, r, backend, func(ctx context.Context, backend Backend) (bool, any) {
		res := backend.TestNotifier(ctx, r.PathValue(apiParamName))
		return res.OK, res
	})
}

func (s *Server) handleApplications(w http.ResponseWriter, r *http.Request) {
	s.readJSON(w, r, func(ctx context.Context, backend Backend) any { return backend.Applications(ctx) })
}

func (s *Server) handleLibraries(w http.ResponseWriter, r *http.Request) {
	s.readJSON(w, r, func(ctx context.Context, backend Backend) any { return backend.Libraries(ctx) })
}

func (s *Server) handleLocks(w http.ResponseWriter, r *http.Request) {
	s.readJSON(w, r, func(ctx context.Context, backend Backend) any { return backend.Locks(ctx) })
}

func (s *Server) handleLockRelease(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.mutationBackend(w, r)
	if !ok {
		return
	}
	res := backend.ReleaseLock(r.Context(), r.PathValue(apiParamService), r.URL.Query().Get(apiParamName))
	writeActionResult(w, res.OK, res)
}

// handleNamed answers a lookup keyed by the request's name path parameter:
// 404 with notFoundMsg when fn reports no match, the JSON result otherwise.
func handleNamed[T any](s *Server, w http.ResponseWriter, r *http.Request, notFoundMsg string, fn func(Backend, context.Context, string) (T, bool)) {
	backend, generation, ok := s.backendRead(w)
	if !ok {
		return
	}
	res, ok := fn(backend, r.Context(), r.PathValue(apiParamName))
	if !ok {
		writeError(w, http.StatusNotFound, notFoundMsg)
		return
	}
	s.writeBackendJSON(w, http.StatusOK, res, generation)
}

func (s *Server) handleDetail(w http.ResponseWriter, r *http.Request) {
	handleNamed(s, w, r, apiErrorUnknownService, func(backend Backend, ctx context.Context, name string) (Detail, bool) {
		d, ok := backend.Detail(ctx, name)
		if ok && roleFrom(ctx) == roleGuest {
			d.Processes = redactProcessCmdlines(d.Processes)
		}
		return d, ok
	})
}

// Command-line arguments can carry secrets (tokens, passwords); read-only
// viewers get only the executable. The redactors clone before trimming so the
// backend's own slices are never mutated.

func redactProcessCmdlines(procs []Process) []Process {
	return redactCloned(procs, func(p *Process) { p.Cmdline = executableOnly(p.Cmdline) })
}

func redactMountCmdlines(mounts []Mount) []Mount {
	return redactCloned(mounts, func(m *Mount) { m.Blockers = redactBlockerCmdlines(m.Blockers) })
}

func redactBlockerCmdlines(blockers []MountBlocker) []MountBlocker {
	return redactCloned(blockers, func(b *MountBlocker) { b.Cmdline = executableOnly(b.Cmdline) })
}

// redactCloned applies redact to every element of a clone of items, leaving
// the backend's own slice untouched.
func redactCloned[T any](items []T, redact func(*T)) []T {
	out := slices.Clone(items)
	for i := range out {
		redact(&out[i])
	}
	return out
}

// executableOnly trims a command line to its argv[0].
func executableOnly(cmdline []string) []string {
	if len(cmdline) > 1 {
		return cmdline[:1]
	}
	return cmdline
}

func (s *Server) handleServiceRuntime(w http.ResponseWriter, r *http.Request) {
	handleNamed(s, w, r, apiErrorUnknownService, func(backend Backend, ctx context.Context, name string) (ServiceRuntimeMetrics, bool) {
		return backend.ServiceRuntime(ctx, name, s.seriesSince(r))
	})
}

func (s *Server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.mutationBackend(w, r)
	if !ok {
		return
	}
	res, found := backend.Preflight(r.Context(), r.PathValue(apiParamName))
	if !found {
		writeError(w, http.StatusNotFound, apiErrorUnknownService)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
