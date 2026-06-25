package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeBackend struct {
	services        []Service
	applications    []Application
	mounts          []Mount
	operated        []string // "name/action"
	monitored       map[string]bool
	watchMonitored  map[string]bool
	panic           bool
	watchExpanded   []string
	failOp          bool
	seriesSince     time.Duration
	eventLimit      int
	metricCheck     string
	metricSince     time.Duration
	opsSlots        OperationSlots
	preflightCalled string
	events          []Event
	releasedLocks   []string
	releaseOK       bool
}

func (f *fakeBackend) Services(context.Context) []Service   { return f.services }
func (f *fakeBackend) Watches(context.Context) []Watch      { return nil }
func (f *fakeBackend) Notifiers(context.Context) []Notifier { return nil }
func (f *fakeBackend) Applications(context.Context) []Application {
	return f.applications
}
func (f *fakeBackend) Mounts(context.Context) []Mount           { return f.mounts }
func (f *fakeBackend) DaemonInfo(context.Context) DaemonInfo    { return DaemonInfo{} }
func (f *fakeBackend) HostMetrics(context.Context) []HostMetric { return nil }
func (f *fakeBackend) DaemonMetrics(context.Context, time.Duration) DaemonMetrics {
	return DaemonMetrics{
		Since:   "24h0m0s",
		Current: DaemonRuntime{At: "2026-06-07T10:00:00Z", PID: 123, RSS: 1024, FDs: 9, Threads: 4},
		CPU:     MetricSeries{Check: "sermod", Metric: "cpu", Unit: "%", Points: []MetricPoint{{Start: "2026-06-07T10:00:00Z", N: 1, Avg: 1.5, Min: 1.5, Max: 1.5}}},
	}
}
func (f *fakeBackend) Locks(context.Context) []Lock { return nil }
func (f *fakeBackend) ReleaseLock(_ context.Context, service, name string) ActionResult {
	f.releasedLocks = append(f.releasedLocks, service+"."+name)
	if !f.releaseOK {
		return ActionResult{OK: false, Message: "release blocked"}
	}
	return ActionResult{OK: true, Message: "released"}
}
func (f *fakeBackend) ActivitySummary(context.Context) ActivitySummary   { return ActivitySummary{} }
func (f *fakeBackend) MonitoringStatus(context.Context) MonitoringStatus { return MonitoringStatus{} }
func (f *fakeBackend) Detail(_ context.Context, name string) (Detail, bool) {
	for _, s := range f.services {
		if s.Name == name {
			ratio := 0.99
			return Detail{
				Service: s,
				Checks:  []Check{{Name: "http", Type: "http", OK: true, Ran: true, Message: "status 200"}},
				SLA:     []SLAWindow{{Window: "day", Ratio: &ratio, Up: 99, Total: 100}},
			}, true
		}
	}
	return Detail{}, false
}
func (f *fakeBackend) Series(_ context.Context, name string, since time.Duration) ([]SeriesPoint, bool) {
	for _, s := range f.services {
		if s.Name == name {
			f.seriesSince = since
			r := 1.0
			return []SeriesPoint{{Start: "2026-06-07T10:00:00Z", Ratio: &r, Up: 2, Total: 2}}, true
		}
	}
	return nil, false
}
func (f *fakeBackend) Events(_ context.Context, limit int) []Event {
	f.eventLimit = limit
	if f.events != nil {
		if len(f.events) > limit {
			return f.events[:limit]
		}
		return f.events
	}
	return []Event{{Time: "2026-06-07T10:00:00Z", Service: "web", Kind: "action", Action: "restart", Message: "restarted"}}
}
func (f *fakeBackend) ServiceEvents(_ context.Context, name string, limit int) ([]Event, bool) {
	for _, s := range f.services {
		if s.Name == name {
			return []Event{{Time: "2026-06-07T10:00:00Z", Service: name, Kind: "alert", Message: "down"}}, true
		}
	}
	return nil, false
}

func (f *fakeBackend) ApplicationEvents(_ context.Context, name string, limit int) ([]Event, bool) {
	for _, a := range f.applications {
		if a.Name == name {
			return []Event{{Time: "2026-06-07T10:00:00Z", App: name, Kind: "firing", Message: "error: exit 1"}}, true
		}
	}
	return nil, false
}
func (f *fakeBackend) PruneEvents(_ context.Context, before time.Time) int {
	if before.IsZero() {
		n := len(f.events)
		f.events = nil
		return n
	}
	// simple impl for tests: drop if their (string) Time parses before
	kept := f.events[:0]
	for _, e := range f.events {
		if t, err := time.Parse(time.RFC3339, e.Time); err == nil && !t.Before(before) {
			kept = append(kept, e)
		}
	}
	cleared := len(f.events) - len(kept)
	f.events = kept
	return cleared
}
func (f *fakeBackend) Metrics(_ context.Context, name, check, _ string, since time.Duration) (MetricSeries, bool) {
	for _, s := range f.services {
		if s.Name == name {
			f.metricCheck, f.metricSince = check, since
			return MetricSeries{
				Check: check, Since: since.String(), Unit: "ms",
				Summary: MetricSummary{Count: 10, Avg: 12.5, Min: 3, Max: 40},
				Points:  []MetricPoint{{Start: "2026-06-07T10:00:00Z", N: 2, Avg: 12.5, Min: 3, Max: 40}},
			}, true
		}
	}
	return MetricSeries{}, false
}
func (f *fakeBackend) ServiceRuntime(_ context.Context, name string, since time.Duration) (ServiceRuntimeMetrics, bool) {
	for _, s := range f.services {
		if s.Name == name {
			f.metricSince = since
			return ServiceRuntimeMetrics{
				Since: since.String(),
				Current: ServiceRuntime{
					At:            "2026-06-07T10:00:00Z",
					ProcessTotals: ProcessTotals{Count: 2, RSS: 2048, IORead: 100, IOWrite: 200, CPU: 3.5, HasCPU: true},
					Uptime:        "1h",
					UptimeSeconds: 3600,
				},
				CPU:    MetricSeries{Check: "runtime", Metric: "cpu", Unit: "%", Points: []MetricPoint{{Start: "2026-06-07T10:00:00Z", N: 1, Avg: 3.5, Min: 3.5, Max: 3.5}}},
				Memory: MetricSeries{Check: "runtime", Metric: "memory", Unit: "bytes", Points: []MetricPoint{{Start: "2026-06-07T10:00:00Z", N: 1, Avg: 2048, Min: 2048, Max: 2048}}},
				IO:     MetricSeries{Check: "runtime", Metric: "io", Unit: "B/s", Points: []MetricPoint{{Start: "2026-06-07T10:00:00Z", N: 1, Avg: 25, Min: 25, Max: 25}}},
			}, true
		}
	}
	return ServiceRuntimeMetrics{}, false
}
func (f *fakeBackend) Operations(context.Context) OperationSlots { return f.opsSlots }
func (f *fakeBackend) Operate(_ context.Context, name, action string, opts OperateOpts) ActionResult {
	suffix := ""
	if opts.NoCascade {
		suffix = "?no_cascade"
	}
	f.operated = append(f.operated, name+"/"+action+suffix)
	if f.failOp {
		return ActionResult{OK: false, Message: "blocked"}
	}
	return ActionResult{OK: true, Message: "ok"}
}
func (f *fakeBackend) CompactState(_ context.Context, before time.Time) StateCompactResult {
	return StateCompactResult{
		OK:     true,
		Pruned: 3,
		Before: before.UTC().Format(time.RFC3339),
		Vacuum: true,
	}
}
func (f *fakeBackend) SetMonitored(_ context.Context, name string, monitored bool) error {
	if f.monitored == nil {
		f.monitored = map[string]bool{}
	}
	f.monitored[name] = monitored
	return nil
}
func (f *fakeBackend) SetWatchMonitored(_ context.Context, name string, monitored bool) error {
	if f.watchMonitored == nil {
		f.watchMonitored = map[string]bool{}
	}
	f.watchMonitored[name] = monitored
	return nil
}
func (f *fakeBackend) SetPanic(_ context.Context, on bool) ActionResult {
	f.panic = on
	return ActionResult{OK: true}
}
func (f *fakeBackend) ExpandWatch(_ context.Context, name string) ActionResult {
	f.watchExpanded = append(f.watchExpanded, name)
	return ActionResult{OK: true, Message: "expanded"}
}
func (f *fakeBackend) Preflight(_ context.Context, name string) (PreflightResult, bool) {
	for _, s := range f.services {
		if s.Name == name {
			f.preflightCalled = name
			return PreflightResult{
				OK:     true,
				Checks: []Check{{Name: "storage", OK: true, Ran: true, Message: "ok"}},
			}, true
		}
	}
	return PreflightResult{}, false
}

func newServer(b Backend) http.Handler {
	return (&Server{Backend: b}).Handler()
}

// postReq is a POST request carrying the CSRF header (as the dashboard sends).
func postReq(path string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, nil)
	r.Header.Set(csrfHeader, "1")
	return r
}

func TestHandlePanicToggles(t *testing.T) {
	b := &fakeBackend{}
	h := newServer(b)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postReq("/api/panic/on"))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/panic/on = %d", rec.Code)
	}
	if !b.panic {
		t.Fatal("backend panic flag should be enabled")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, postReq("/api/panic/off"))
	if rec.Code != http.StatusOK || b.panic {
		t.Fatalf("POST /api/panic/off = %d, panic=%v", rec.Code, b.panic)
	}
}

func TestHandlePanicRejectsBadAction(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{}).ServeHTTP(rec, postReq("/api/panic/maybe"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/panic/maybe = %d, want 400", rec.Code)
	}
}

func TestServesDashboard(t *testing.T) {
	h := newServer(&fakeBackend{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("dashboard is not HTML: %s", rec.Body.String()[:64])
	}
	if strings.Contains(rec.Body.String(), "{{CSP_NONCE}}") {
		t.Fatal("dashboard still contains the CSP nonce placeholder")
	}
	if !strings.Contains(rec.Body.String(), `<script nonce="`) || !strings.Contains(rec.Body.String(), `<style nonce="`) {
		t.Fatalf("dashboard did not receive CSP nonce attributes")
	}
	// The served page is built from internal/web/src by esbuild, so JS function
	// names are minified away. Assert on markers that survive minification: CSS
	// class selectors and the verbatim attribute strings in template literals.
	for _, want := range []string{"usagebar-fill", "usagebar-label", "usage-crit", `data-watch-action="expand"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("dashboard missing storage usage UI marker %q", want)
		}
	}
	if strings.Contains(rec.Body.String(), "transform:scaleX") {
		t.Fatal("dashboard storage usage bar should use width growth, not transform growth")
	}
	for _, inlineHandler := range []string{"onclick=", "onchange=", "oninput=", "onkeydown="} {
		if strings.Contains(rec.Body.String(), inlineHandler) {
			t.Fatalf("dashboard contains inline handler %q", inlineHandler)
		}
	}
	// The dashboard must not be cached, or an upgraded binary's new sections
	// (e.g. host watches) stay invisible behind a stale browser copy.
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("dashboard Cache-Control = %q, want no-cache", cc)
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := newServer(&fakeBackend{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("Content-Security-Policy = %q, want it to contain default-src 'self'", csp)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self' 'nonce-") {
		t.Errorf("Content-Security-Policy = %q, want script-src nonce", csp)
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Errorf("Content-Security-Policy = %q, script-src must not allow unsafe-inline", csp)
	}
}

func TestListServices(t *testing.T) {
	b := &fakeBackend{services: []Service{{Name: "web", Category: "frontend", Status: "active", Monitored: true}}}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/services", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got []Service
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Name != "web" || got[0].Category != "frontend" || !got[0].Monitored {
		t.Fatalf("unexpected services: %+v", got)
	}
}

func TestListApplications(t *testing.T) {
	b := &fakeBackend{applications: []Application{{
		Name: "nginx", DisplayName: "Nginx", Category: "web", Binary: "/usr/bin/nginx",
		Permissions: "-rwxr-xr-x (0755)", User: "root", Group: "root",
		Version:      "nginx version: nginx/1.30.2",
		VersionShort: "1.30.2", VersionSource: "nginx-bin", Status: "ok",
	}}}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/applications", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got []Application
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Name != "nginx" || got[0].VersionShort != "1.30.2" ||
		got[0].Binary != "/usr/bin/nginx" || got[0].Permissions != "-rwxr-xr-x (0755)" ||
		got[0].User != "root" || got[0].Group != "root" || got[0].Category != "web" ||
		got[0].VersionSource != "nginx-bin" {
		t.Fatalf("unexpected applications: %+v", got)
	}
}

func TestListMounts(t *testing.T) {
	b := &fakeBackend{mounts: []Mount{{
		Name: "mount-backup", Path: "/mnt/backup", Mounted: true, Refcount: 2, Source: "fstab", State: "active", Refcounted: true,
	}}}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/mounts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got []Mount
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Name != "mount-backup" || !got[0].Mounted || got[0].Refcount != 2 {
		t.Fatalf("unexpected mounts: %+v", got)
	}
}

func TestServiceDetail(t *testing.T) {
	b := &fakeBackend{services: []Service{{Name: "web", Status: "active", Monitored: true}}}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/services/web", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status %d", rec.Code)
	}
	var d Detail
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Name != "web" || len(d.Checks) != 1 || d.Checks[0].Name != "http" || len(d.SLA) != 1 {
		t.Fatalf("unexpected detail: %+v", d)
	}
}

func TestServiceDetailUnknown(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/services/ghost", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown detail = %d, want 404", rec.Code)
	}
}

func TestSLASeries(t *testing.T) {
	b := &fakeBackend{services: []Service{{Name: "web"}}}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/services/web/sla?since=168h", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("series status %d", rec.Code)
	}
	if b.seriesSince != 168*time.Hour {
		t.Fatalf("since not parsed: %v", b.seriesSince)
	}
	var body struct {
		Since  string        `json:"since"`
		Points []SeriesPoint `json:"points"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Points) != 1 || body.Points[0].Total != 2 {
		t.Fatalf("unexpected points: %+v", body.Points)
	}
}

func TestSLASeriesDefaultsAndCaps(t *testing.T) {
	b := &fakeBackend{services: []Service{{Name: "web"}}}
	h := newServer(b)
	// no since -> default 24h
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/services/web/sla", nil))
	if b.seriesSince != 24*time.Hour {
		t.Fatalf("default since = %v, want 24h", b.seriesSince)
	}
	// absurd since -> capped at the retention window
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/services/web/sla?since=99999h", nil))
	if b.seriesSince != maxSeriesWindow {
		t.Fatalf("since not capped: %v", b.seriesSince)
	}
}

func TestSLASeriesUnknown(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/services/ghost/sla", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown series = %d, want 404", rec.Code)
	}
}

func TestGlobalEvents(t *testing.T) {
	b := &fakeBackend{}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events?limit=50", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("events status %d", rec.Code)
	}
	if b.eventLimit != 50 {
		t.Fatalf("limit not parsed: %d", b.eventLimit)
	}
	var got []Event
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Kind != "action" {
		t.Fatalf("unexpected events: %+v", got)
	}
}

func TestEventLimitCapAndDefault(t *testing.T) {
	b := &fakeBackend{}
	h := newServer(b)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/events", nil))
	if b.eventLimit != defaultEventLimit {
		t.Fatalf("default limit = %d, want %d", b.eventLimit, defaultEventLimit)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/events?limit=99999", nil))
	if b.eventLimit != maxEventLimit {
		t.Fatalf("limit not capped: %d", b.eventLimit)
	}
}

func TestGlobalEventsFilters(t *testing.T) {
	events := []Event{
		{Time: "2026-06-07T10:00:04Z", Service: "web", Kind: "action", Action: "restart", Status: "ok", Message: "done"},
		{Time: "2026-06-07T10:00:03Z", Service: "db", Kind: "error", Action: "restart", Status: "failed", Message: "blocked"},
		{Time: "2026-06-07T10:00:02Z", Watch: "storage-root", Kind: "hook-failed", Status: "failed", Message: "hook failed"},
		{Time: "2026-06-07T10:00:01Z", Watch: "storage-root", Kind: "hook", Status: "ok", Message: "hook ok"},
	}
	tests := []struct {
		name       string
		query      string
		wantLimit  int
		wantCount  int
		wantFirst  string
		wantStatus string
	}{
		{name: "service", query: "?service=db", wantLimit: maxEventLimit, wantCount: 1, wantFirst: "db"},
		{name: "watch kind", query: "?watch=storage-root&kind=hook-failed", wantLimit: maxEventLimit, wantCount: 1, wantFirst: "storage-root"},
		{name: "status", query: "?status=failed", wantLimit: maxEventLimit, wantCount: 2, wantStatus: "failed"},
		{name: "only errors", query: "?only_errors=1", wantLimit: maxEventLimit, wantCount: 2},
		{name: "filtered limit", query: "?only_errors=true&limit=1", wantLimit: maxEventLimit, wantCount: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &fakeBackend{events: events}
			rec := httptest.NewRecorder()
			newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events"+tt.query, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d", rec.Code)
			}
			if b.eventLimit != tt.wantLimit {
				t.Fatalf("backend limit = %d, want %d", b.eventLimit, tt.wantLimit)
			}
			var got []Event
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(got) != tt.wantCount {
				t.Fatalf("events = %+v, want %d", got, tt.wantCount)
			}
			if tt.wantFirst != "" {
				who := got[0].Service
				if who == "" {
					who = got[0].Watch
				}
				if who != tt.wantFirst {
					t.Fatalf("first subject = %q, want %q", who, tt.wantFirst)
				}
			}
			if tt.wantStatus != "" && got[0].Status != tt.wantStatus {
				t.Fatalf("first status = %q, want %q", got[0].Status, tt.wantStatus)
			}
		})
	}
}

func TestServiceEvents(t *testing.T) {
	b := &fakeBackend{services: []Service{{Name: "web"}}}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/services/web/events", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("service events status %d", rec.Code)
	}
	var got []Event
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Service != "web" {
		t.Fatalf("unexpected service events: %+v", got)
	}

	rec = httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/services/ghost/events", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown service events = %d, want 404", rec.Code)
	}
}

func TestMetrics(t *testing.T) {
	b := &fakeBackend{services: []Service{{Name: "web"}}}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/services/web/metrics?check=http&since=168h", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status %d", rec.Code)
	}
	if b.metricCheck != "http" || b.metricSince != 168*time.Hour {
		t.Fatalf("params not parsed: check=%q since=%v", b.metricCheck, b.metricSince)
	}
	var got MetricSeries
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Summary.Avg != 12.5 || got.Summary.Max != 40 || got.Unit != "ms" || len(got.Points) != 1 {
		t.Fatalf("unexpected metrics: %+v", got)
	}

	// missing check -> 400
	rec = httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/services/web/metrics", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing check = %d, want 400", rec.Code)
	}
	// unknown service -> 404
	rec = httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/services/ghost/metrics?check=http", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown service = %d, want 404", rec.Code)
	}
}

func TestServiceRuntimeMetrics(t *testing.T) {
	b := &fakeBackend{services: []Service{{Name: "web"}}}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/services/web/runtime?since=168h", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("runtime status %d", rec.Code)
	}
	if b.metricSince != 168*time.Hour {
		t.Fatalf("runtime since not parsed: %v", b.metricSince)
	}
	var got ServiceRuntimeMetrics
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Current.Count != 2 || got.Current.RSS != 2048 || got.CPU.Metric != "cpu" || len(got.CPU.Points) != 1 {
		t.Fatalf("runtime metrics = %+v", got)
	}

	rec = httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/services/ghost/runtime", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown runtime service = %d, want 404", rec.Code)
	}
}

func TestDaemonMetrics(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/daemon/metrics?since=1h", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("daemon metrics status %d", rec.Code)
	}
	var got DaemonMetrics
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Current.PID != 123 || got.Current.FDs != 9 || got.CPU.Metric != "cpu" || len(got.CPU.Points) != 1 {
		t.Fatalf("daemon metrics = %+v", got)
	}
}

func TestOperationsAPI(t *testing.T) {
	b := &fakeBackend{opsSlots: OperationSlots{InUse: 2, Total: 2}}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ops", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("ops status %d", rec.Code)
	}
	var got OperationSlots
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.InUse != 2 || got.Total != 2 {
		t.Fatalf("unexpected ops: %+v", got)
	}
}

func TestReleaseLockEndpoint(t *testing.T) {
	b := &fakeBackend{releaseOK: true}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, postReq("/api/locks/mysql/release?name=backup"))
	if rec.Code != http.StatusOK {
		t.Fatalf("release status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(b.releasedLocks) != 1 || b.releasedLocks[0] != "mysql.backup" {
		t.Fatalf("releasedLocks = %v", b.releasedLocks)
	}
}

func TestReleaseLockEndpointConflict(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{}).ServeHTTP(rec, postReq("/api/locks/mysql/release"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("blocked release status = %d, want 409", rec.Code)
	}
}

func TestOperateActions(t *testing.T) {
	b := &fakeBackend{}
	h := newServer(b)
	for _, action := range []string{"start", "stop", "restart"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postReq("/api/services/web/"+action))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", action, rec.Code)
		}
	}
	want := []string{"web/start", "web/stop", "web/restart"}
	if strings.Join(b.operated, ",") != strings.Join(want, ",") {
		t.Fatalf("operated = %v, want %v", b.operated, want)
	}
}

func TestOperateNoCascadeQuery(t *testing.T) {
	b := &fakeBackend{}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, postReq("/api/services/web/restart?no_cascade=1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("restart no_cascade = %d", rec.Code)
	}
	if len(b.operated) != 1 || b.operated[0] != "web/restart?no_cascade" {
		t.Fatalf("operated = %v, want web/restart?no_cascade", b.operated)
	}
}

func TestStateCompact(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{}).ServeHTTP(rec, postReq("/api/state/compact?before=720h"))
	if rec.Code != http.StatusOK {
		t.Fatalf("state compact = %d body=%s", rec.Code, rec.Body.String())
	}
	var out StateCompactResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK || out.Pruned != 3 || !out.Vacuum {
		t.Fatalf("compact result = %+v", out)
	}
}

func TestMonitorActions(t *testing.T) {
	b := &fakeBackend{}
	h := newServer(b)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postReq("/api/services/web/unmonitor"))
	if rec.Code != http.StatusOK || b.monitored["web"] != false {
		t.Fatalf("unmonitor: code=%d monitored=%v", rec.Code, b.monitored)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, postReq("/api/services/web/monitor"))
	if rec.Code != http.StatusOK || b.monitored["web"] != true {
		t.Fatalf("monitor: code=%d monitored=%v", rec.Code, b.monitored)
	}
}

func TestWatchMonitorActions(t *testing.T) {
	b := &fakeBackend{}
	h := newServer(b)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postReq("/api/watches/storage-root/unmonitor"))
	if rec.Code != http.StatusOK || b.watchMonitored["storage-root"] != false {
		t.Fatalf("watch unmonitor: code=%d monitored=%v", rec.Code, b.watchMonitored)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, postReq("/api/watches/storage-root/monitor"))
	if rec.Code != http.StatusOK || b.watchMonitored["storage-root"] != true {
		t.Fatalf("watch monitor: code=%d monitored=%v", rec.Code, b.watchMonitored)
	}
}

func TestWatchExpandAction(t *testing.T) {
	b := &fakeBackend{}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, postReq("/api/watches/storage-root/expand"))
	if rec.Code != http.StatusOK {
		t.Fatalf("watch expand: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(b.watchExpanded) != 1 || b.watchExpanded[0] != "storage-root" {
		t.Fatalf("watchExpanded = %v, want storage-root", b.watchExpanded)
	}
}

func TestUnknownActionIsBadRequest(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{}).ServeHTTP(rec, postReq("/api/services/web/destroy"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown action = %d, want 400", rec.Code)
	}
}

func TestEventsClear(t *testing.T) {
	b := &fakeBackend{
		events: []Event{
			{Time: "2026-06-01T00:00:00Z", Kind: "action"},
			{Time: "2026-06-10T00:00:00Z", Kind: "alert"},
			{Time: "2026-06-12T00:00:00Z", Kind: "action"},
		},
	}
	h := newServer(b)

	// clear all
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postReq("/api/events/clear"))
	if rec.Code != http.StatusOK {
		t.Fatalf("clear all status %d: %s", rec.Code, rec.Body.String())
	}
	if len(b.events) != 0 {
		t.Fatalf("after clear all, events left: %d", len(b.events))
	}

	// repopulate and clear before a date
	b.events = []Event{
		{Time: "2026-06-01T00:00:00Z", Kind: "old"},
		{Time: "2026-06-12T00:00:00Z", Kind: "keep"},
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, postReq("/api/events/clear?before=2026-06-05T00:00:00Z"))
	if rec.Code != http.StatusOK {
		t.Fatalf("clear before status %d", rec.Code)
	}
	if len(b.events) != 1 || b.events[0].Kind != "keep" {
		t.Fatalf("after prune before, left=%v", b.events)
	}
}

func TestFailedOperateIsConflict(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{failOp: true}).ServeHTTP(rec, postReq("/api/services/web/restart"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("failed operate = %d, want 409", rec.Code)
	}
}

func TestPreflightEndpoint(t *testing.T) {
	b := &fakeBackend{services: []Service{{Name: "web"}}}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, postReq("/api/services/web/preflight"))
	if rec.Code != http.StatusOK {
		t.Fatalf("preflight status = %d, want 200", rec.Code)
	}
	if b.preflightCalled != "web" {
		t.Fatalf("preflightCalled = %q, want web", b.preflightCalled)
	}
	var body PreflightResult
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.OK || len(body.Checks) != 1 || body.Checks[0].Name != "storage" {
		t.Fatalf("body = %+v", body)
	}
}

func TestPreflightUnknownService(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{}).ServeHTTP(rec, postReq("/api/services/ghost/preflight"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown preflight = %d, want 404", rec.Code)
	}
}

func TestServerWriteTimeoutCoversOperationTimeout(t *testing.T) {
	got := serverWriteTimeout(90 * time.Second)
	if got < 90*time.Second {
		t.Fatalf("write timeout %v shorter than operation timeout", got)
	}
	if got := serverWriteTimeout(0); got < defaultOperationTimeout {
		t.Fatalf("zero operation timeout should default write timeout, got %v", got)
	}
}

type ctxCapturingBackend struct {
	fakeBackend
	delay   time.Duration
	operCtx context.Context
}

func (b *ctxCapturingBackend) Operate(ctx context.Context, name, action string, _ OperateOpts) ActionResult {
	b.operCtx = ctx
	timer := time.NewTimer(b.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return ActionResult{OK: true, Message: "ok"}
	case <-ctx.Done():
		return ActionResult{OK: false, Message: ctx.Err().Error()}
	}
}

func TestOperateContextIgnoresRequestCancel(t *testing.T) {
	b := &ctxCapturingBackend{delay: 40 * time.Millisecond}
	srv := &Server{
		Backend:          b,
		OperationTimeout: 200 * time.Millisecond,
	}
	srv.shutdown = context.Background()
	h := srv.Handler()

	req := postReq("/api/services/web/restart")
	reqCtx, cancel := context.WithCancel(req.Context())
	cancel() // simulate client disconnect / HTTP deadline
	req = req.WithContext(reqCtx)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("operate status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if b.operCtx == reqCtx {
		t.Fatal("operate must not use the request context")
	}
	if b.operCtx.Err() != nil {
		t.Fatalf("operate context cancelled early: %v", b.operCtx.Err())
	}
}

func TestGetOnActionRouteNotAllowed(t *testing.T) {
	// Only POST is registered for the action route; GET must not operate.
	b := &fakeBackend{}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/services/web/start", nil))
	if rec.Code == http.StatusOK || len(b.operated) != 0 {
		t.Fatalf("GET should not trigger an action: code=%d operated=%v", rec.Code, b.operated)
	}
}

func TestParseBeforeQueryDurationIsPast(t *testing.T) {
	got, err := parseBeforeQuery("1h")
	if err != nil {
		t.Fatalf("parseBeforeQuery: %v", err)
	}
	// A bare duration means "1h ago": the result is in the past.
	if !got.Before(time.Now()) {
		t.Fatalf("parseBeforeQuery(1h) = %v, want a past time", got)
	}
	if d := time.Since(got); d < 50*time.Minute || d > 70*time.Minute {
		t.Fatalf("parseBeforeQuery(1h) is %v ago, want ~1h", d)
	}
}

func TestEventLimitParsing(t *testing.T) {
	mk := func(q string) *http.Request { return httptest.NewRequest("GET", "/api/events?limit="+q, nil) }
	if got := eventLimit(mk("5")); got != 5 {
		t.Errorf("limit=5 -> %d, want 5", got)
	}
	// A non-positive limit is ignored (n > 0 guard), keeping the default.
	if got := eventLimit(mk("0")); got != defaultEventLimit {
		t.Errorf("limit=0 -> %d, want default %d", got, defaultEventLimit)
	}
	// Over the cap is clamped.
	if got := eventLimit(mk("100000")); got != maxEventLimit {
		t.Errorf("limit=100000 -> %d, want cap %d", got, maxEventLimit)
	}
}

func TestSeriesSinceParsing(t *testing.T) {
	mk := func(q string) *http.Request { return httptest.NewRequest("GET", "/api/series?since="+q, nil) }
	if got := seriesSince(mk("2h")); got != 2*time.Hour {
		t.Errorf("since=2h -> %v, want 2h", got)
	}
	// A non-positive duration is ignored (d > 0 guard), keeping the default.
	if got := seriesSince(mk("0s")); got != defaultSeriesWindow {
		t.Errorf("since=0s -> %v, want default %v", got, defaultSeriesWindow)
	}
	if got := seriesSince(mk("100000h")); got != maxSeriesWindow {
		t.Errorf("since=100000h -> %v, want cap %v", got, maxSeriesWindow)
	}
}
