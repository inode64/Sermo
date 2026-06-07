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
	services    []Service
	operated    []string // "name/action"
	monitored   map[string]bool
	failOp      bool
	seriesSince time.Duration
	eventLimit  int
	metricCheck string
	metricSince time.Duration
	opsSlots    OperationSlots
}

func (f *fakeBackend) Services(context.Context) []Service { return f.services }
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
func (f *fakeBackend) Metrics(_ context.Context, name, check string, since time.Duration) (MetricSeries, bool) {
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
func (f *fakeBackend) Diagnostics(context.Context) []Finding {
	return []Finding{{Level: "warning", Scope: "database", Message: `stored data for service "ghost"`}}
}
func (f *fakeBackend) Operations(context.Context) OperationSlots { return f.opsSlots }
func (f *fakeBackend) Operate(_ context.Context, name, action string) ActionResult {
	f.operated = append(f.operated, name+"/"+action)
	if f.failOp {
		return ActionResult{OK: false, Message: "blocked"}
	}
	return ActionResult{OK: true, Message: "ok"}
}
func (f *fakeBackend) SetMonitored(_ context.Context, name string, monitored bool) error {
	if f.monitored == nil {
		f.monitored = map[string]bool{}
	}
	f.monitored[name] = monitored
	return nil
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
}

func TestListServices(t *testing.T) {
	b := &fakeBackend{services: []Service{{Name: "web", Status: "active", Monitored: true}}}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/services", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got []Service
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Name != "web" || !got[0].Monitored {
		t.Fatalf("unexpected services: %+v", got)
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

func TestDiagnostics(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/diagnostics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("diagnostics status %d", rec.Code)
	}
	var got []Finding
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Level != "warning" || got[0].Scope != "database" {
		t.Fatalf("unexpected findings: %+v", got)
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

func TestUnknownActionIsBadRequest(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{}).ServeHTTP(rec, postReq("/api/services/web/destroy"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown action = %d, want 400", rec.Code)
	}
}

func TestFailedOperateIsConflict(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{failOp: true}).ServeHTTP(rec, postReq("/api/services/web/restart"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("failed operate = %d, want 409", rec.Code)
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

func (b *ctxCapturingBackend) Operate(ctx context.Context, name, action string) ActionResult {
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
