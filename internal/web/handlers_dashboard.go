package web

import (
	"context"
	htmlpkg "html"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"sermo/internal/buildinfo"
)

func (*Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page, err := assets.ReadFile(assetIndexHTML)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	html := strings.ReplaceAll(string(page), templateNoncePlaceholder, cspNonceFrom(r.Context()))
	page = []byte(strings.ReplaceAll(html, templateVersionPlaceholder, htmlpkg.EscapeString(buildinfo.Short())))
	w.Header().Set(headerContentType, contentTypeHTMLUTF8)
	// The dashboard markup/JS is embedded in the binary and changes across
	// versions (new sections like host watches are added over time). Without a
	// cache directive a browser may keep serving a stale copy after an upgrade,
	// so newly added sections never appear even though the API returns their
	// data. no-cache forces a revalidation on every load.
	w.Header().Set(headerCacheControl, headerValueNoCache)
	_, _ = w.Write(page)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	backend, generation := s.backendRead()
	snapshot := s.dashboardSnapshot(r.Context(), backend, s.seriesSince(r))
	if roleFrom(r.Context()) == roleGuest {
		snapshot.Mounts = redactMountCmdlines(snapshot.Mounts)
	}
	if generation > 0 {
		snapshot.Generation = generation
	}
	s.writeBackendJSON(w, http.StatusOK, snapshot, generation)
}

// CollectDashboardSnapshot collects the reload-sensitive dashboard sections in
// parallel from one backend instance. It intentionally omits server-owned
// readiness and liveness fields, which Server adds around the aggregate.
func CollectDashboardSnapshot(ctx context.Context, backend Backend, since time.Duration) DashboardSnapshot {
	var snapshot DashboardSnapshot
	var wg sync.WaitGroup
	run := func(fn func()) {
		wg.Go(fn)
	}
	run(func() { snapshot.Services = backend.Services(ctx) })
	run(func() { snapshot.Mounts = backend.Mounts(ctx) })
	run(func() { snapshot.Notifiers = backend.Notifiers(ctx) })
	run(func() { snapshot.Daemon = backend.DaemonInfo(ctx) })
	run(func() { snapshot.DaemonMetrics = backend.DaemonMetrics(ctx, since) })
	run(func() { snapshot.Locks = backend.Locks(ctx) })
	run(func() { snapshot.Activity = backend.ActivitySummary(ctx) })
	run(func() { snapshot.Monitoring = backend.MonitoringStatus(ctx) })
	run(func() { snapshot.HostMetrics = backend.HostMetrics(ctx) })
	wg.Wait()
	// DaemonInfo warms the shared SSH sampler cache above. Read sessions after
	// the parallel batch so this aggregate does not race a duplicate host scan.
	if source, ok := backend.(sessionInventorySource); ok {
		snapshot.Sessions = source.Sessions(ctx)
	}
	return snapshot
}

func (s *Server) dashboardSnapshot(ctx context.Context, backend Backend, since time.Duration) DashboardSnapshot {
	if source, ok := backend.(dashboardSnapshotSource); ok {
		return s.dashboardSnapshotWithReadiness(ctx, func() DashboardSnapshot {
			return source.DashboardSnapshot(ctx, since)
		})
	}
	return s.dashboardSnapshotWithReadiness(ctx, func() DashboardSnapshot {
		return CollectDashboardSnapshot(ctx, backend, since)
	})
}

func (s *Server) dashboardSnapshotWithReadiness(ctx context.Context, collect func() DashboardSnapshot) DashboardSnapshot {
	if s.Readiness == nil {
		snapshot := collect()
		snapshot.Ready = ReadyReport{Ready: true, Status: apiStatusOK, Services: len(snapshot.Services)}
		return s.finishDashboardSnapshot(snapshot)
	}
	var snapshot DashboardSnapshot
	var ready ReadyReport
	var wg sync.WaitGroup
	wg.Go(func() { snapshot = collect() })
	wg.Go(func() { ready = s.Readiness.Report(ctx) })
	wg.Wait()
	snapshot.Ready = ready
	return s.finishDashboardSnapshot(snapshot)
}

func (s *Server) finishDashboardSnapshot(snapshot DashboardSnapshot) DashboardSnapshot {
	now := time.Now()
	uptime := now.Sub(s.started)
	snapshot.Live = LiveReport{
		Status:        apiStatusOK,
		StartedAt:     s.started.Format(time.RFC3339),
		Now:           now.Format(time.RFC3339),
		Uptime:        uptime.Round(time.Second).String(),
		UptimeSeconds: int64(uptime.Seconds()),
		Services:      len(snapshot.Services),
		Go:            runtime.Version(),
	}
	return snapshot
}

func (s *Server) handleDaemon(w http.ResponseWriter, r *http.Request) {
	s.readJSON(w, r, func(ctx context.Context, backend Backend) any { return backend.DaemonInfo(ctx) })
}

func (s *Server) handleDaemonMetrics(w http.ResponseWriter, r *http.Request) {
	since := s.seriesSince(r)
	s.readJSON(w, r, func(ctx context.Context, backend Backend) any { return backend.DaemonMetrics(ctx, since) })
}

func (s *Server) handleHost(w http.ResponseWriter, r *http.Request) {
	s.readJSON(w, r, func(ctx context.Context, backend Backend) any { return backend.HostMetrics(ctx) })
}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	s.readJSON(w, r, func(ctx context.Context, backend Backend) any { return backend.ActivitySummary(ctx) })
}

func (s *Server) handleMonitoring(w http.ResponseWriter, r *http.Request) {
	s.readJSON(w, r, func(ctx context.Context, backend Backend) any { return backend.MonitoringStatus(ctx) })
}

// readyReportFromBackend builds the readiness report: it delegates to the
// configured Readiness probe when present, otherwise reports ready with the
// service count.
func (s *Server) readyReportFromBackend(ctx context.Context, backend Backend) ReadyReport {
	if s.Readiness != nil {
		return s.Readiness.Report(ctx)
	}
	return ReadyReport{
		Ready: true, Status: apiStatusOK,
		Services: len(backend.Services(ctx)),
	}
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	backend, generation := s.backendRead()
	rep := s.readyReportFromBackend(r.Context(), backend)
	status := http.StatusOK
	if !rep.Ready {
		status = http.StatusServiceUnavailable
	}
	if !r.URL.Query().Has(apiQueryVerbose) {
		w.Header().Set(headerContentType, contentTypeTextUTF8)
		w.WriteHeader(status)
		if rep.Ready {
			_, _ = io.WriteString(w, apiStatusOKLine)
		} else {
			_, _ = io.WriteString(w, rep.Status+"\n")
		}
		return
	}
	s.writeBackendJSON(w, status, rep, generation)
}

// handleLivez is the liveness probe: if the daemon's web server can answer, the
// process is alive, so it always returns 200. Plain requests get "ok"; `?verbose`
// returns JSON with uptime, the number of services and the runtime version. It is
// served without authentication (see withAuth) so probes need no credentials.
func (s *Server) handleLivez(w http.ResponseWriter, r *http.Request) {
	if !r.URL.Query().Has(apiQueryVerbose) {
		w.Header().Set(headerContentType, contentTypeTextUTF8)
		_, _ = io.WriteString(w, apiStatusOKLine)
		return
	}
	now := time.Now()
	uptime := now.Sub(s.started)
	backend, generation := s.backendRead()
	s.writeBackendJSON(w, http.StatusOK, map[string]any{
		apiJSONKeyStatus:        apiStatusOK,
		apiJSONKeyStartedAt:     s.started.Format(time.RFC3339),
		apiJSONKeyNow:           now.Format(time.RFC3339),
		apiJSONKeyUptime:        uptime.Round(time.Second).String(),
		apiJSONKeyUptimeSeconds: int64(uptime.Seconds()),
		apiJSONKeyServices:      len(backend.Services(r.Context())),
		apiJSONKeyGo:            runtime.Version(),
	}, generation)
}
