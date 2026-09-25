package web

import (
	"context"
	"net/http"
	"time"

	"sermo/internal/state"
)

// defaultMaxSeriesWindow bounds the history a single request may ask for when the
// daemon did not supply the configured coarsest retention.
var defaultMaxSeriesWindow = state.DefaultRetention().MaxWindow()

// defaultSeriesWindow is used when no (or an invalid) `since` is given.
const defaultSeriesWindow = state.DefaultSeriesWindow

// seriesSince reads the `since` query param, defaulting and capping it at the
// coarsest archive's retention: asking for more history than is stored cannot
// return anything, and an unbounded window would let one request scan the lot.
func (s *Server) seriesSince(r *http.Request) time.Duration {
	limit := s.MaxSeriesWindow
	if limit <= 0 {
		limit = defaultMaxSeriesWindow
	}
	return queryCapped(r, apiQuerySince, defaultSeriesWindow, limit, positiveDuration)
}

func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	check := r.URL.Query().Get(apiQueryCheck)
	metric := r.URL.Query().Get(apiQueryMetric)
	notFound := apiErrorUnknownService
	switch {
	case metric != "":
		notFound = apiErrorUnknownCheckBand
	case check != "":
		notFound = apiErrorUnknownServiceOrCheck
	}
	s.serveSeries(w, r, notFound, func(ctx context.Context, backend Backend, since time.Duration) ([]SeriesPoint, bool) {
		return backend.Series(ctx, r.PathValue(apiParamName), check, metric, since)
	})
}

// handleWatchSeries serves a host watch's availability series or metric state
// bands through the same envelope and bounded window as service series.
func (s *Server) handleWatchSeries(w http.ResponseWriter, r *http.Request) {
	metric := r.URL.Query().Get(apiQueryMetric)
	notFound := apiErrorUnknownAvailWatch
	if metric != "" {
		notFound = apiErrorUnknownCheckBand
	}
	s.serveSeries(w, r, notFound, func(ctx context.Context, backend Backend, since time.Duration) ([]SeriesPoint, bool) {
		return backend.WatchSeries(ctx, r.PathValue(apiParamName), metric, since)
	})
}

func (s *Server) serveSeries(w http.ResponseWriter, r *http.Request, notFound string, fetch func(context.Context, Backend, time.Duration) ([]SeriesPoint, bool)) {
	since := s.seriesSince(r)
	backend, generation, ok := s.backendRead(w)
	if !ok {
		return
	}
	points, ok := fetch(r.Context(), backend, since)
	if !ok {
		writeError(w, http.StatusNotFound, notFound)
		return
	}
	s.writeBackendJSON(w, http.StatusOK, map[string]any{apiJSONKeySince: since.String(), apiJSONKeyPoints: points}, generation)
}

// handleWatchMetrics serves one numeric series a host watch's check publishes.
// A watch has exactly one check, so there is no ?check= to name: ?metric= alone
// selects the series, and a metric the check does not publish is a 404 rather
// than an empty series that would read as a measured flat line.
func (s *Server) handleWatchMetrics(w http.ResponseWriter, r *http.Request) {
	s.serveMetrics(w, r, apiQueryMetric, apiErrorMetricQueryRequired, apiErrorUnknownWatchMetric,
		func(ctx context.Context, backend Backend, metric string, since time.Duration) (MetricSeries, bool) {
			return backend.WatchMetrics(ctx, r.PathValue(apiParamName), metric, since)
		})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	s.serveMetrics(w, r, apiQueryCheck, apiErrorCheckQueryRequired, apiErrorUnknownServiceOrCheck,
		func(ctx context.Context, backend Backend, check string, since time.Duration) (MetricSeries, bool) {
			return backend.Metrics(ctx, r.PathValue(apiParamName), check, r.URL.Query().Get(apiQueryMetric), since)
		})
}

func (s *Server) serveMetrics(w http.ResponseWriter, r *http.Request, required, missing, notFound string, fetch func(context.Context, Backend, string, time.Duration) (MetricSeries, bool)) {
	value := r.URL.Query().Get(required)
	if value == "" {
		writeError(w, http.StatusBadRequest, missing)
		return
	}
	backend, generation, ok := s.backendRead(w)
	if !ok {
		return
	}
	res, ok := fetch(r.Context(), backend, value, s.seriesSince(r))
	if !ok {
		writeError(w, http.StatusNotFound, notFound)
		return
	}
	s.writeBackendJSON(w, http.StatusOK, res, generation)
}
