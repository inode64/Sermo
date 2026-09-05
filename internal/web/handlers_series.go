package web

import (
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
	return queryCapped(r, apiQuerySince, defaultSeriesWindow, limit, func(q string) (time.Duration, bool) {
		d, err := time.ParseDuration(q)
		return d, err == nil && d > 0
	})
}

func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	since := s.seriesSince(r)
	check := r.URL.Query().Get(apiQueryCheck)
	metric := r.URL.Query().Get(apiQueryMetric)
	backend, generation := s.backendRead()
	points, ok := backend.Series(r.Context(), r.PathValue(apiParamName), check, metric, since)
	if !ok {
		notFound := apiErrorUnknownService
		switch {
		case metric != "":
			notFound = apiErrorUnknownCheckBand
		case check != "":
			notFound = apiErrorUnknownServiceOrCheck
		}
		writeError(w, http.StatusNotFound, notFound)
		return
	}
	s.writeBackendJSON(w, http.StatusOK, map[string]any{apiJSONKeySince: since.String(), apiJSONKeyPoints: points}, generation)
}

// handleWatchSeries serves a host watch's availability series — or, with
// ?metric=, one of its state bands — through the same envelope and window query
// the service series uses, so the dashboard reads both with one loader.
func (s *Server) handleWatchSeries(w http.ResponseWriter, r *http.Request) {
	since := s.seriesSince(r)
	metric := r.URL.Query().Get(apiQueryMetric)
	backend, generation := s.backendRead()
	points, ok := backend.WatchSeries(r.Context(), r.PathValue(apiParamName), metric, since)
	if !ok {
		notFound := apiErrorUnknownAvailWatch
		if metric != "" {
			notFound = apiErrorUnknownCheckBand
		}
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
	metric := r.URL.Query().Get(apiQueryMetric)
	if metric == "" {
		writeError(w, http.StatusBadRequest, apiErrorMetricQueryRequired)
		return
	}
	backend, generation := s.backendRead()
	res, ok := backend.WatchMetrics(r.Context(), r.PathValue(apiParamName), metric, s.seriesSince(r))
	if !ok {
		writeError(w, http.StatusNotFound, apiErrorUnknownWatchMetric)
		return
	}
	s.writeBackendJSON(w, http.StatusOK, res, generation)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	check := r.URL.Query().Get(apiQueryCheck)
	if check == "" {
		writeError(w, http.StatusBadRequest, apiErrorCheckQueryRequired)
		return
	}
	backend, generation := s.backendRead()
	res, ok := backend.Metrics(r.Context(), r.PathValue(apiParamName), check, r.URL.Query().Get(apiQueryMetric), s.seriesSince(r))
	if !ok {
		writeError(w, http.StatusNotFound, apiErrorUnknownServiceOrCheck)
		return
	}
	s.writeBackendJSON(w, http.StatusOK, res, generation)
}
