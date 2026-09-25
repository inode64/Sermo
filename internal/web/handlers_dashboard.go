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
	backend, generation, ok := s.backendRead(w)
	if !ok {
		return
	}
	snapshot := s.dashboardSnapshot(r.Context(), backend, s.seriesSince(r))
	if roleFrom(r.Context()) == roleGuest {
		snapshot.Mounts = redactMountCmdlines(snapshot.Mounts)
	}
	if generation > 0 {
		snapshot.Generation = generation
	}
	s.writeBackendJSON(w, http.StatusOK, snapshot, generation)
}

type serviceLockSource interface {
	ServicesAndLocks(ctx context.Context) ([]Service, []Lock)
}

// CollectDashboardSnapshot collects the reload-sensitive dashboard sections in
// parallel from one backend instance. It intentionally omits server-owned
// readiness and liveness fields, which Server adds around the aggregate.
func CollectDashboardSnapshot(ctx context.Context, backend Backend, since time.Duration) DashboardSnapshot {
	var snapshot DashboardSnapshot
	var wg sync.WaitGroup
	if source, ok := backend.(serviceLockSource); ok {
		wg.Go(func() { snapshot.Services, snapshot.Locks = source.ServicesAndLocks(ctx) })
	} else {
		wg.Go(func() { snapshot.Services = backend.Services(ctx) })
		wg.Go(func() { snapshot.Locks = backend.Locks(ctx) })
	}
	wg.Go(func() { snapshot.Mounts = backend.Mounts(ctx) })
	wg.Go(func() { snapshot.Notifiers = backend.Notifiers(ctx) })
	wg.Go(func() { snapshot.Daemon = backend.DaemonInfo(ctx) })
	wg.Go(func() { snapshot.DaemonMetrics = backend.DaemonMetrics(ctx, since) })
	wg.Go(func() { snapshot.Activity = backend.ActivitySummary(ctx) })
	wg.Go(func() { snapshot.HostMetrics = backend.HostMetrics(ctx) })
	wg.Wait()
	// Derive totals from the same service rows: a concurrent monitor transition
	// must not make the dashboard counters disagree with its own list.
	for i := range snapshot.Services {
		service := &snapshot.Services[i]
		if !service.Enabled {
			continue
		}
		snapshot.Monitoring.Total++
		if service.Monitored {
			snapshot.Monitoring.Monitored++
		}
	}
	snapshot.Monitoring.Paused = snapshot.Monitoring.Total - snapshot.Monitoring.Monitored
	// DaemonInfo warms the shared SSH sampler cache above. Read sessions after
	// the parallel batch so this aggregate does not race a duplicate host scan.
	if source, ok := backend.(sessionInventorySource); ok {
		snapshot.Sessions = source.Sessions(ctx)
	}
	return snapshot
}

func (s *Server) dashboardSnapshot(ctx context.Context, backend Backend, since time.Duration) DashboardSnapshot {
	var snapshot DashboardSnapshot
	if s.Readiness == nil {
		snapshot = CollectDashboardSnapshot(ctx, backend, since)
		snapshot.Ready = readyFallback(len(snapshot.Services))
	} else {
		var ready ReadyReport
		var wg sync.WaitGroup
		wg.Go(func() { snapshot = CollectDashboardSnapshot(ctx, backend, since) })
		wg.Go(func() { ready = s.Readiness.Report(ctx) })
		wg.Wait()
		snapshot.Ready = ready
	}
	snapshot.Live = s.liveReport(time.Now(), len(snapshot.Services))
	return snapshot
}

func (s *Server) liveReport(now time.Time, services int) LiveReport {
	uptime := now.Sub(s.started)
	return LiveReport{
		Status:        apiStatusOK,
		StartedAt:     s.started.Format(time.RFC3339),
		Now:           now.Format(time.RFC3339),
		UptimeSeconds: int64(uptime.Seconds()),
		Services:      services,
		Go:            runtime.Version(),
	}
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
	return readyFallback(len(backend.Services(ctx)))
}

func readyFallback(services int) ReadyReport {
	return ReadyReport{Ready: true, Status: apiStatusOK, Services: services}
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	backend, generation, ok := s.backendRead(w)
	if !ok {
		return
	}
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
	backend, generation, ok := s.backendRead(w)
	if !ok {
		return
	}
	s.writeBackendJSON(w, http.StatusOK, s.liveReport(now, len(backend.Services(r.Context()))), generation)
}
