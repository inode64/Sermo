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

func TestOperateActions(t *testing.T) {
	b := &fakeBackend{}
	h := newServer(b)
	for _, action := range []string{"start", "stop", "restart"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/services/web/"+action, nil))
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
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/services/web/unmonitor", nil))
	if rec.Code != http.StatusOK || b.monitored["web"] != false {
		t.Fatalf("unmonitor: code=%d monitored=%v", rec.Code, b.monitored)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/services/web/monitor", nil))
	if rec.Code != http.StatusOK || b.monitored["web"] != true {
		t.Fatalf("monitor: code=%d monitored=%v", rec.Code, b.monitored)
	}
}

func TestUnknownActionIsBadRequest(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/services/web/destroy", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown action = %d, want 400", rec.Code)
	}
}

func TestFailedOperateIsConflict(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{failOp: true}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/services/web/restart", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("failed operate = %d, want 409", rec.Code)
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
