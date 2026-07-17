package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sermo/internal/mountctl"
)

type fakeBackend struct {
	services        []Service
	applications    []Application
	libraries       []Library
	mounts          []Mount
	mountAction     MountActionResult
	mountBlockers   MountBlockersResult
	mountAlert      MountAlertResult
	mountOperated   []string
	operated        []string // "name/action"
	monitored       map[string]bool
	watchMonitored  map[string]bool
	panic           bool
	watchExpanded   []string
	watchProbed     []string
	raidControlled  []string
	failOp          bool
	seriesSince     time.Duration
	eventLimit      int
	eventQuery      EventQuery
	metricCheck     string
	metricSince     time.Duration
	opsSlots        OperationSlots
	preflightCalled string
	events          []Event
	releasedLocks   []string
	releaseOK       bool
	notifierTested  string
	notifierResult  ActionResult
}

func (f *fakeBackend) Services(context.Context) []Service   { return f.services }
func (f *fakeBackend) Watches(context.Context) []Watch      { return nil }
func (f *fakeBackend) Notifiers(context.Context) []Notifier { return nil }
func (f *fakeBackend) TestNotifier(_ context.Context, name string) ActionResult {
	f.notifierTested = name
	if f.notifierResult.Message != "" {
		return f.notifierResult
	}
	return ActionResult{OK: true, Message: "test sent"}
}
func (f *fakeBackend) Applications(context.Context) []Application {
	return f.applications
}
func (f *fakeBackend) Libraries(context.Context) []Library { return f.libraries }
func (f *fakeBackend) Mounts(context.Context) []Mount      { return f.mounts }
func (f *fakeBackend) MountAction(_ context.Context, name, action string, opts MountActionOptions) MountActionResult {
	var suffix []string
	if opts.AllowForce {
		suffix = append(suffix, "force")
	}
	if opts.AllowLazy {
		suffix = append(suffix, "lazy")
	}
	if opts.KillBlockers {
		suffix = append(suffix, "kill")
	}
	flagText := ""
	if len(suffix) > 0 {
		flagText = "?" + strings.Join(suffix, "&")
	}
	f.mountOperated = append(f.mountOperated, name+"/"+action+flagText)
	if f.mountAction.Message != "" || f.mountAction.Name != "" {
		return f.mountAction
	}
	return MountActionResult{OK: true, Name: name, Action: action, Status: eventStatusOK, Message: "ok"}
}
func (f *fakeBackend) MountBlockers(_ context.Context, name string) MountBlockersResult {
	if f.mountBlockers.Name != "" || f.mountBlockers.Message != "" || len(f.mountBlockers.Blockers) > 0 {
		return f.mountBlockers
	}
	return MountBlockersResult{OK: true, Name: name}
}
func (f *fakeBackend) AlertMountUsers(_ context.Context, name string) MountAlertResult {
	if f.mountAlert.Name != "" || f.mountAlert.Message != "" {
		return f.mountAlert
	}
	return MountAlertResult{OK: true, Name: name, Message: "alert sent"}
}
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
	return []Event{{Time: "2026-06-07T10:00:00Z", Service: "web", Kind: eventKindAction, Action: apiActionRestart, Message: "restarted"}}
}
func (f *fakeBackend) EventPage(ctx context.Context, query EventQuery) EventPage {
	f.eventQuery = query
	events := f.Events(ctx, query.Limit+1)
	hasMore := len(events) > query.Limit
	if hasMore {
		events = events[:query.Limit]
	}
	page := EventPage{Events: events, HasMore: hasMore}
	if hasMore && len(events) > 0 {
		page.NextBeforeID = events[len(events)-1].ID
	}
	return page
}
func (f *fakeBackend) ServiceEvents(_ context.Context, name string, limit int) ([]Event, bool) {
	for _, s := range f.services {
		if s.Name == name {
			return []Event{{Time: "2026-06-07T10:00:00Z", Service: name, Kind: eventKindAlert, Message: "down"}}, true
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
func (f *fakeBackend) ProbeWatch(_ context.Context, name string) ActionResult {
	f.watchProbed = append(f.watchProbed, name)
	return ActionResult{OK: true, Message: "probed"}
}
func (f *fakeBackend) ControlRAID(_ context.Context, name, action, confirmation string) ActionResult {
	f.raidControlled = append(f.raidControlled, name+"/"+action+"/"+confirmation)
	return ActionResult{OK: true, Message: "controlled"}
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

func TestDashboardSnapshotEndpoint(t *testing.T) {
	b := &fakeBackend{
		services: []Service{{Name: "web", State: "monitored"}},
		mounts:   []Mount{{Name: "data", Path: "/data"}},
		opsSlots: OperationSlots{InUse: 1, Total: 4},
	}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, testAPIPath(apiSegmentDashboard), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got DashboardSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode dashboard: %v", err)
	}
	if len(got.Services) != 1 || got.Services[0].Name != "web" || len(got.Mounts) != 1 {
		t.Fatalf("dashboard inventory = %+v", got)
	}
	if !got.Ready.Ready || got.Ready.Services != 1 || got.Live.Services != 1 {
		t.Fatalf("dashboard probes = ready:%+v live:%+v", got.Ready, got.Live)
	}
	if got.Operations.InUse != 1 || got.Operations.Total != 4 || got.GeneratedAt == "" {
		t.Fatalf("dashboard runtime = %+v", got)
	}
}

type dashboardSourceBackend struct {
	fakeBackend
	snapshot DashboardSnapshot
	calls    int
}

type generationBackend struct {
	fakeBackend
	generation uint64
}

func (b *generationBackend) BackendGeneration() uint64 {
	return b.generation
}

type pinnedGenerationBackend struct {
	fakeBackend
	generation uint64
	released   bool
}

func (b *pinnedGenerationBackend) BeginBackendRead() (Backend, uint64, func()) {
	return &b.fakeBackend, b.generation, func() { b.released = true }
}

func (b *dashboardSourceBackend) DashboardSnapshot(context.Context, time.Duration) DashboardSnapshot {
	b.calls++
	return b.snapshot
}

func TestDashboardSnapshotUsesAtomicSource(t *testing.T) {
	b := &dashboardSourceBackend{
		fakeBackend: fakeBackend{services: []Service{{Name: "fallback"}}},
		snapshot: DashboardSnapshot{
			Services:  []Service{{Name: "one-generation"}},
			Notifiers: []Notifier{{Name: "same-generation"}},
		},
	}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, testAPIPath(apiSegmentDashboard), nil))
	if rec.Code != http.StatusOK || b.calls != 1 {
		t.Fatalf("dashboard status/calls = %d/%d, want 200/1", rec.Code, b.calls)
	}
	var got DashboardSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode dashboard: %v", err)
	}
	if len(got.Services) != 1 || got.Services[0].Name != "one-generation" || len(got.Notifiers) != 1 || got.Notifiers[0].Name != "same-generation" {
		t.Fatalf("dashboard source snapshot = %+v, want atomic source values", got)
	}
}

func TestBackendReadResponsesCarryGeneration(t *testing.T) {
	b := &generationBackend{generation: 7}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, testAPIPath(apiSegmentServices), nil))
	if got := rec.Header().Get(headerSermoGeneration); got != "7" {
		t.Fatalf("response generation = %q, want 7", got)
	}
}

func TestBackendReadResponseUsesPinnedGeneration(t *testing.T) {
	b := &pinnedGenerationBackend{generation: 9}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, testAPIPath(apiSegmentDashboard), nil))
	if got := rec.Header().Get(headerSermoGeneration); got != "9" {
		t.Fatalf("pinned response generation = %q, want 9", got)
	}
	var snapshot DashboardSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode dashboard: %v", err)
	}
	if snapshot.Generation != 9 {
		t.Fatalf("dashboard generation = %d, want 9", snapshot.Generation)
	}
	if !b.released {
		t.Fatal("pinned backend read was not released")
	}
}

// postReq is a POST request carrying the CSRF header (as the dashboard sends).
func postReq(path string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, nil)
	r.Header.Set(headerSermoCSRF, "1")
	return r
}

func TestHandlePanicToggles(t *testing.T) {
	b := &fakeBackend{}
	h := newServer(b)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postReq(testAPIPath(apiSegmentPanic, apiActionPanicOn)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/panic/on = %d", rec.Code)
	}
	if !b.panic {
		t.Fatal("backend panic flag should be enabled")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, postReq(testAPIPath(apiSegmentPanic, apiActionPanicOff)))
	if rec.Code != http.StatusOK || b.panic {
		t.Fatalf("POST /api/panic/off = %d, panic=%v", rec.Code, b.panic)
	}
}

func TestHandlePanicRejectsBadAction(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{}).ServeHTTP(rec, postReq(testAPIPath(apiSegmentPanic, "maybe")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/panic/maybe = %d, want 400", rec.Code)
	}
}

func TestHandleNotifierTest(t *testing.T) {
	b := &fakeBackend{}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, postReq(testTargetPath(apiSegmentNotifiers, "ops", apiActionTest)))
	if rec.Code != http.StatusOK || b.notifierTested != "ops" {
		t.Fatalf("POST notifier test = %d, tested=%q", rec.Code, b.notifierTested)
	}

	b.notifierResult = ActionResult{OK: false, Message: "disabled"}
	rec = httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, postReq(testTargetPath(apiSegmentNotifiers, "ops", apiActionTest)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("failed notifier test = %d, want %d", rec.Code, http.StatusConflict)
	}
}

func TestServesDashboard(t *testing.T) {
	h := newServer(&fakeBackend{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, routePathRoot, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("dashboard is not HTML: %s", rec.Body.String()[:64])
	}
	if strings.Contains(rec.Body.String(), templateNoncePlaceholder) {
		t.Fatal("dashboard still contains the CSP nonce placeholder")
	}
	if !strings.Contains(rec.Body.String(), `<script nonce="`) || !strings.Contains(rec.Body.String(), `<style nonce="`) {
		t.Fatalf("dashboard did not receive CSP nonce attributes")
	}
	// The served page is built from internal/web/src by esbuild, so JS function
	// names and dynamic template values are minified away. Assert on markers that
	// survive minification: CSS class selectors and delegated action attributes.
	for _, want := range []string{"usagebar-fill", "usagebar-label", "usage-crit", "data-watch-action"} {
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
	if cc := rec.Header().Get(headerCacheControl); cc != headerValueNoCache {
		t.Fatalf("dashboard %s = %q, want %s", headerCacheControl, cc, headerValueNoCache)
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := newServer(&fakeBackend{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, routePathRoot, nil))
	want := map[string]string{
		headerXContentTypeOptions: headerValueNoSniff,
		headerXFrameOptions:       headerValueDeny,
		headerReferrerPolicy:      headerValueNoReferrer,
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if csp := rec.Header().Get(headerContentSecurityPolicy); !strings.Contains(csp, cspDirectiveDefaultSrc) {
		t.Errorf("%s = %q, want it to contain %s", headerContentSecurityPolicy, csp, cspDirectiveDefaultSrc)
	}
	csp := rec.Header().Get(headerContentSecurityPolicy)
	if !strings.Contains(csp, cspDirectiveScriptSrcPrefix) {
		t.Errorf("%s = %q, want script-src nonce", headerContentSecurityPolicy, csp)
	}
	if strings.Contains(csp, cspDirectiveScriptUnsafeInline) {
		t.Errorf("%s = %q, script-src must not allow unsafe-inline", headerContentSecurityPolicy, csp)
	}
}

// getJSON issues a GET against b's server at path, asserts a 200, and decodes the
// JSON body into T.
func getJSON[T any](t *testing.T, b Backend, path string) T {
	t.Helper()
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got T
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

func TestListServices(t *testing.T) {
	b := &fakeBackend{services: []Service{{Name: "web", Category: "frontend", Status: "active", Monitored: true}}}
	got := getJSON[[]Service](t, b, apiPathServices)
	if len(got) != 1 || got[0].Name != "web" || got[0].Category != "frontend" || !got[0].Monitored {
		t.Fatalf("unexpected services: %+v", got)
	}
}

func TestListApplications(t *testing.T) {
	b := &fakeBackend{applications: []Application{{
		Name: "nginx", DisplayName: "Nginx", Category: "web", Binary: "/usr/bin/nginx",
		Permissions: "-rwxr-xr-x (0755)", User: "root", Group: "root",
		Version:      "nginx version: nginx/1.30.2",
		VersionShort: "1.30.2", VersionSource: "nginx-bin", Status: apiStatusOK,
	}}}
	got := getJSON[[]Application](t, b, apiPathApplications)
	if len(got) != 1 || got[0].Name != "nginx" || got[0].VersionShort != "1.30.2" ||
		got[0].Binary != "/usr/bin/nginx" || got[0].Permissions != "-rwxr-xr-x (0755)" ||
		got[0].User != "root" || got[0].Group != "root" || got[0].Category != "web" ||
		got[0].VersionSource != "nginx-bin" {
		t.Fatalf("unexpected applications: %+v", got)
	}
}

func TestListLibraries(t *testing.T) {
	b := &fakeBackend{libraries: []Library{{
		Name: "openssl", DisplayName: "OpenSSL", Category: "crypto", Binary: "/usr/lib64/libssl.so",
		Permissions: "-rwxr-xr-x (0755)", User: "root", Group: "root",
		Version: "OpenSSL 3.5.1", VersionShort: "3.5.1", Status: apiStatusOK,
	}}}
	got := getJSON[[]Library](t, b, apiPathLibraries)
	if len(got) != 1 || got[0].Name != "openssl" || got[0].VersionShort != "3.5.1" ||
		got[0].Binary != "/usr/lib64/libssl.so" || got[0].Category != "crypto" {
		t.Fatalf("unexpected libraries: %+v", got)
	}
}

func TestListMounts(t *testing.T) {
	b := &fakeBackend{mounts: []Mount{{
		Name: "mount-backup", Path: "/mnt/backup", Mounted: true, Refcount: 2, State: "active", Refcounted: true,
		Operation: &MountOperation{Action: mountctl.ActionUmount, State: "unmounting", StartedAt: "2026-07-13T10:00:00Z"},
	}}}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, apiPathMounts, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got []Mount
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if strings.Contains(rec.Body.String(), `"source"`) {
		t.Fatalf("mount response should not expose source: %s", rec.Body.String())
	}
	if len(got) != 1 || got[0].Name != "mount-backup" || !got[0].Mounted || got[0].Refcount != 2 {
		t.Fatalf("unexpected mounts: %+v", got)
	}
	if got[0].Operation == nil || got[0].Operation.Action != mountctl.ActionUmount || got[0].Operation.State != "unmounting" {
		t.Fatalf("mount operation = %+v, want unmounting", got[0].Operation)
	}
}

func TestMountAction(t *testing.T) {
	b := &fakeBackend{}
	rec := httptest.NewRecorder()
	q := testQueryParam(apiQueryKill, queryBoolOne)
	q += "&" + apiQueryForce + "=" + queryBoolOne
	q += "&" + apiQueryLazy + "=" + queryBoolOne
	newServer(b).ServeHTTP(rec, postReq(
		testPathQuery(testMountPath("mount-backup", mountctl.ActionUmount), q),
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := strings.Join(b.mountOperated, ","); got != "mount-backup/umount?force&lazy&kill" {
		t.Fatalf("mount actions = %q", got)
	}
	var res MountActionResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.OK || res.Action != mountctl.ActionUmount {
		t.Fatalf("unexpected response: %+v", res)
	}
}

func TestMountActionRejectsUnknown(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{}).ServeHTTP(rec, postReq(testMountPath("mount-backup", "reboot")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

func TestMountBlockers(t *testing.T) {
	b := &fakeBackend{mountBlockers: MountBlockersResult{
		OK:            true,
		Name:          "mount-backup",
		Path:          "/mnt/backup",
		Mounted:       true,
		HasKillPolicy: true,
		CanKill:       true,
		Blockers: []MountBlocker{{
			PID: 123, User: "backup", UID: 1000, Group: "backup", GID: 1000, Exe: "/usr/bin/rsync", ExeResolved: true, Killable: true,
		}},
	}}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, postReq(testMountPath("mount-backup", apiActionBlockers)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got MountBlockersResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.OK || !got.HasKillPolicy || !got.CanKill || len(got.Blockers) != 1 || !got.Blockers[0].Killable || got.Blockers[0].Group != "backup" {
		t.Fatalf("unexpected blockers: %+v", got)
	}
}

func TestMountAlert(t *testing.T) {
	b := &fakeBackend{mountAlert: MountAlertResult{
		OK: true, Name: "mount-backup", Path: "/mnt/backup", Users: []string{"backup"}, Delivered: 1, Message: "alert sent",
	}}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, postReq(testMountPath("mount-backup", apiActionAlert)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got MountAlertResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.OK || got.Delivered != 1 || len(got.Users) != 1 || got.Users[0] != "backup" {
		t.Fatalf("unexpected alert: %+v", got)
	}
}

func TestServiceDetail(t *testing.T) {
	b := &fakeBackend{services: []Service{{Name: "web", Status: "active", Monitored: true}}}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, testServicePath("web"), nil))
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
	newServer(&fakeBackend{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, testServicePath("ghost"), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown detail = %d, want 404", rec.Code)
	}
}

func TestSLASeries(t *testing.T) {
	b := &fakeBackend{services: []Service{{Name: "web"}}}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		testPathQuery(testServicePath("web", apiSegmentSLA), testQueryParam(apiQuerySince, "168h")),
		nil,
	))
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
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, testServicePath("web", apiSegmentSLA), nil))
	if b.seriesSince != 24*time.Hour {
		t.Fatalf("default since = %v, want 24h", b.seriesSince)
	}
	// absurd since -> capped at the retention window
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(
		http.MethodGet,
		testPathQuery(testServicePath("web", apiSegmentSLA), testQueryParam(apiQuerySince, "99999h")),
		nil,
	))
	if b.seriesSince != maxSeriesWindow {
		t.Fatalf("since not capped: %v", b.seriesSince)
	}
}

func TestSLASeriesUnknown(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, testServicePath("ghost", apiSegmentSLA), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown series = %d, want 404", rec.Code)
	}
}

func TestGlobalEvents(t *testing.T) {
	b := &fakeBackend{}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		testPathQuery(apiPathEvents, testQueryParam(apiQueryLimit, "50")),
		nil,
	))
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
	if len(got) != 1 || got[0].Kind != eventKindAction {
		t.Fatalf("unexpected events: %+v", got)
	}
}

func TestGlobalEventsCursorPage(t *testing.T) {
	b := &fakeBackend{events: []Event{
		{ID: 9, Service: "web", Kind: eventKindError, Status: eventStatusFailed},
		{ID: 8, Service: "web", Kind: eventKindAction, Status: eventStatusOK},
	}}
	rec := httptest.NewRecorder()
	query := testQueryParams(apiQueryPage, queryBoolOne, apiQueryBeforeID, "10", apiParamService, "web", apiQueryOnlyErrors, queryBoolOne, apiQueryLimit, queryBoolOne)
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, testPathQuery(apiPathEvents, query), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("events page status %d: %s", rec.Code, rec.Body.String())
	}
	if b.eventQuery.BeforeID != 10 || b.eventQuery.Limit != 1 || b.eventQuery.Service != "web" || !b.eventQuery.OnlyErrors {
		t.Fatalf("event query = %+v", b.eventQuery)
	}
	var got EventPage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Events) != 1 || got.Events[0].ID != 9 || !got.HasMore || got.NextBeforeID != 9 {
		t.Fatalf("event page = %+v", got)
	}
}

func TestGlobalEventsPageParsesTimeRange(t *testing.T) {
	b := &fakeBackend{}
	rec := httptest.NewRecorder()
	query := testQueryParams(apiQueryPage, queryBoolOne, apiQuerySince, "24h")
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, testPathQuery(apiPathEvents, query), nil))
	if rec.Code != http.StatusOK || b.eventQuery.Since != 24*time.Hour {
		t.Fatalf("status=%d event query=%+v", rec.Code, b.eventQuery)
	}

	rec = httptest.NewRecorder()
	query = testQueryParams(apiQueryPage, queryBoolOne, apiQuerySince, "forever")
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, testPathQuery(apiPathEvents, query), nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid since status = %d, want 400", rec.Code)
	}
}

func TestGlobalEventsRejectsInvalidCursor(t *testing.T) {
	rec := httptest.NewRecorder()
	query := testQueryParams(apiQueryPage, queryBoolOne, apiQueryBeforeID, "invalid")
	newServer(&fakeBackend{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, testPathQuery(apiPathEvents, query), nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status = %d, want 400", rec.Code)
	}
}

func TestEventLimitCapAndDefault(t *testing.T) {
	b := &fakeBackend{}
	h := newServer(b)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, apiPathEvents, nil))
	if b.eventLimit != defaultEventLimit {
		t.Fatalf("default limit = %d, want %d", b.eventLimit, defaultEventLimit)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(
		http.MethodGet,
		testPathQuery(apiPathEvents, testQueryParam(apiQueryLimit, "99999")),
		nil,
	))
	if b.eventLimit != maxEventLimit {
		t.Fatalf("limit not capped: %d", b.eventLimit)
	}
}

func TestGlobalEventsFilters(t *testing.T) {
	events := []Event{
		{Time: "2026-06-07T10:00:04Z", Service: "web", Kind: eventKindAction, Action: apiActionRestart, Status: eventStatusOK, Message: "done"},
		{Time: "2026-06-07T10:00:03Z", Service: "db", Kind: eventKindError, Action: apiActionRestart, Status: eventStatusFailed, Message: "blocked"},
		{Time: "2026-06-07T10:00:02Z", Watch: "storage-root", Kind: eventKindHookFailed, Status: eventStatusFailed, Message: "hook failed"},
		{Time: "2026-06-07T10:00:01Z", Watch: "storage-root", Kind: eventKindHook, Status: eventStatusOK, Message: "hook ok"},
	}
	tests := []struct {
		name       string
		query      string
		wantLimit  int
		wantCount  int
		wantFirst  string
		wantStatus string
	}{
		{name: "service", query: testQueryParam(apiParamService, "db"), wantLimit: maxEventLimit, wantCount: 1, wantFirst: "db"},
		{name: "watch kind", query: testQueryParams(apiQueryWatch, "storage-root", apiQueryKind, eventKindHookFailed), wantLimit: maxEventLimit, wantCount: 1, wantFirst: "storage-root"},
		{name: "status", query: testQueryParam(apiQueryStatus, eventStatusFailed), wantLimit: maxEventLimit, wantCount: 2, wantStatus: eventStatusFailed},
		{name: "only errors", query: testQueryParam(apiQueryOnlyErrors, queryBoolOne), wantLimit: maxEventLimit, wantCount: 2},
		{name: "filtered limit", query: testQueryParams(apiQueryOnlyErrors, queryBoolTrue, apiQueryLimit, queryBoolOne), wantLimit: maxEventLimit, wantCount: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &fakeBackend{events: events}
			rec := httptest.NewRecorder()
			newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, testPathQuery(apiPathEvents, tt.query), nil))
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
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, testServicePath("web", apiSegmentEvents), nil))
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
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, testServicePath("ghost", apiSegmentEvents), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown service events = %d, want 404", rec.Code)
	}
}

func TestMetrics(t *testing.T) {
	b := &fakeBackend{services: []Service{{Name: "web"}}}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		testPathQuery(
			testServicePath("web", apiSegmentMetrics),
			testQueryParams(apiQueryCheck, "http", apiQuerySince, "168h"),
		),
		nil,
	))
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
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, testServicePath("web", apiSegmentMetrics), nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing check = %d, want 400", rec.Code)
	}
	// unknown service -> 404
	rec = httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		testPathQuery(testServicePath("ghost", apiSegmentMetrics), testQueryParam(apiQueryCheck, "http")),
		nil,
	))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown service = %d, want 404", rec.Code)
	}
}

func TestServiceRuntimeMetrics(t *testing.T) {
	b := &fakeBackend{services: []Service{{Name: "web"}}}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		testPathQuery(testServicePath("web", apiSegmentRuntime), testQueryParam(apiQuerySince, "168h")),
		nil,
	))
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
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, testServicePath("ghost", apiSegmentRuntime), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown runtime service = %d, want 404", rec.Code)
	}
}

func TestDaemonMetrics(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{}).ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet,
		testPathQuery(testAPIPath(apiSegmentDaemon, apiSegmentMetrics), testQueryParam(apiQuerySince, "1h")),
		nil,
	))
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
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, apiPathOps, nil))
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
	newServer(b).ServeHTTP(rec, postReq(
		testPathQuery(testLockPath(apiActionRelease), testQueryParam(apiParamName, "backup")),
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("release status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(b.releasedLocks) != 1 || b.releasedLocks[0] != "mysql.backup" {
		t.Fatalf("releasedLocks = %v", b.releasedLocks)
	}
}

func TestReleaseLockEndpointConflict(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{}).ServeHTTP(rec, postReq(testLockPath(apiActionRelease)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("blocked release status = %d, want 409", rec.Code)
	}
}

func TestOperateActions(t *testing.T) {
	b := &fakeBackend{}
	h := newServer(b)
	for _, action := range []string{apiActionStart, apiActionStop, apiActionRestart} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postReq(testServicePath("web", action)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", action, rec.Code)
		}
	}
	want := []string{"web/" + apiActionStart, "web/" + apiActionStop, "web/" + apiActionRestart}
	if strings.Join(b.operated, ",") != strings.Join(want, ",") {
		t.Fatalf("operated = %v, want %v", b.operated, want)
	}
}

func TestOperateNoCascadeQuery(t *testing.T) {
	b := &fakeBackend{}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, postReq(
		testPathQuery(testServicePath("web", apiActionRestart), testQueryParam(apiQueryNoCascade, queryBoolOne)),
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("restart no_cascade = %d", rec.Code)
	}
	if len(b.operated) != 1 || b.operated[0] != "web/restart?no_cascade" {
		t.Fatalf("operated = %v, want web/restart?no_cascade", b.operated)
	}
}

func TestStateCompact(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{}).ServeHTTP(rec, postReq(
		testPathQuery(testAPIPath(apiSegmentState, apiActionCompact), testQueryParam(apiQueryBefore, "720h")),
	))
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

// assertMonitorToggle POSTs unmonitor then monitor to path(action) and asserts
// the backend state map (fetched via state, which may be lazily created) flips.
func assertMonitorToggle(t *testing.T, h http.Handler, path func(action string) string, state func() map[string]bool, key string) {
	t.Helper()
	for _, tc := range []struct {
		action string
		want   bool
	}{{apiActionUnmonitor, false}, {apiActionMonitor, true}} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postReq(path(tc.action)))
		if rec.Code != http.StatusOK || state()[key] != tc.want {
			t.Fatalf("%s: code=%d monitored=%v", tc.action, rec.Code, state())
		}
	}
}

func TestMonitorActions(t *testing.T) {
	b := &fakeBackend{}
	assertMonitorToggle(t, newServer(b),
		func(action string) string { return testServicePath("web", action) },
		func() map[string]bool { return b.monitored }, "web")
}

func TestWatchMonitorActions(t *testing.T) {
	b := &fakeBackend{}
	assertMonitorToggle(t, newServer(b),
		func(action string) string { return testWatchPath("storage-root", action) },
		func() map[string]bool { return b.watchMonitored }, "storage-root")
}

func TestWatchExpandAction(t *testing.T) {
	b := &fakeBackend{}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, postReq(testWatchPath("storage-root", apiActionExpand)))
	if rec.Code != http.StatusOK {
		t.Fatalf("watch expand: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(b.watchExpanded) != 1 || b.watchExpanded[0] != "storage-root" {
		t.Fatalf("watchExpanded = %v, want storage-root", b.watchExpanded)
	}
}

func TestWatchProbeAndRAIDActions(t *testing.T) {
	b := &fakeBackend{}
	h := newServer(b)
	for _, tc := range []struct{ action, confirmation string }{
		{apiActionProbe, ""},
		{apiActionPause, "md0"},
		{apiActionResume, ""},
	} {
		rec := httptest.NewRecorder()
		req := postReq(testWatchPath("raid-md0", tc.action))
		if tc.confirmation != "" {
			req.Header.Set("X-Sermo-Confirm", tc.confirmation)
		}
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code=%d body=%s", tc.action, rec.Code, rec.Body.String())
		}
	}
	if got := b.watchProbed; len(got) != 1 || got[0] != "raid-md0" {
		t.Fatalf("probed = %v", got)
	}
	if got := b.raidControlled; len(got) != 2 || got[0] != "raid-md0/pause/md0" || got[1] != "raid-md0/resume/" {
		t.Fatalf("RAID controls = %v", got)
	}
}

func TestUnknownActionIsBadRequest(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{}).ServeHTTP(rec, postReq(testServicePath("web", "destroy")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown action = %d, want 400", rec.Code)
	}
}

func TestEventsClear(t *testing.T) {
	b := &fakeBackend{
		events: []Event{
			{Time: "2026-06-01T00:00:00Z", Kind: eventKindAction},
			{Time: "2026-06-10T00:00:00Z", Kind: eventKindAlert},
			{Time: "2026-06-12T00:00:00Z", Kind: eventKindAction},
		},
	}
	h := newServer(b)

	// clear all
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postReq(testAPIPath(apiSegmentEvents, apiActionClear)))
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
	h.ServeHTTP(rec, postReq(
		testPathQuery(testAPIPath(apiSegmentEvents, apiActionClear), testQueryParam(apiQueryBefore, "2026-06-05T00:00:00Z")),
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("clear before status %d", rec.Code)
	}
	if len(b.events) != 1 || b.events[0].Kind != "keep" {
		t.Fatalf("after prune before, left=%v", b.events)
	}
}

func TestFailedOperateIsConflict(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&fakeBackend{failOp: true}).ServeHTTP(rec, postReq(testServicePath("web", apiActionRestart)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("failed operate = %d, want 409", rec.Code)
	}
}

func TestPreflightEndpoint(t *testing.T) {
	b := &fakeBackend{services: []Service{{Name: "web"}}}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, postReq(testServicePath("web", apiSegmentPreflight)))
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
	newServer(&fakeBackend{}).ServeHTTP(rec, postReq(testServicePath("ghost", apiSegmentPreflight)))
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

func TestActionWriteTimeoutUsesReloadedSource(t *testing.T) {
	server := &Server{
		OperationTimeout: 10 * time.Second,
		OperationTimeoutSource: func() time.Duration {
			return 90 * time.Second
		},
	}
	if got, want := server.actionWriteTimeout(), serverWriteTimeout(90*time.Second); got != want {
		t.Fatalf("action write timeout = %s, want %s from current source", got, want)
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

	req := postReq(testServicePath("web", apiActionRestart))
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

func TestOperateContextCancelsOnDaemonShutdown(t *testing.T) {
	shutdown, cancel := context.WithCancel(context.Background())
	cancel()
	b := &ctxCapturingBackend{delay: time.Hour}
	srv := &Server{Backend: b, shutdown: shutdown}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, postReq(testServicePath("web", apiActionRestart)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("operate after shutdown = %d, want %d", rec.Code, http.StatusConflict)
	}
	if b.operCtx == nil || b.operCtx.Err() == nil {
		t.Fatal("operation context must be cancelled with daemon shutdown")
	}
}

func TestGetOnActionRouteNotAllowed(t *testing.T) {
	// Only POST is registered for the action route; GET must not operate.
	b := &fakeBackend{}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, testServicePath("web", apiActionStart), nil))
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

func TestParseBeforeQueryRejectsUnsafeCutoffs(t *testing.T) {
	tests := []string{
		"0",
		"-1h",
		time.Now().Add(time.Hour).Format(time.RFC3339),
	}
	for _, input := range tests {
		if got, err := parseBeforeQuery(input); err == nil {
			t.Fatalf("parseBeforeQuery(%q) = %v, want error", input, got)
		}
	}
}

// assertQueryParse feeds in as query param on path and checks parse returns want.
func assertQueryParse[T comparable](t *testing.T, path, param, in string, parse func(*http.Request) T, want T) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, testPathQuery(path, testQueryParam(param, in)), nil)
	if got := parse(req); got != want {
		t.Errorf("%s=%s -> %v, want %v", param, in, got, want)
	}
}

func TestEventLimitParsing(t *testing.T) {
	check := func(in string, want int) {
		assertQueryParse(t, apiPathEvents, apiQueryLimit, in, eventLimit, want)
	}
	check("5", 5)
	// A non-positive limit is ignored (n > 0 guard), keeping the default.
	check("0", defaultEventLimit)
	// Over the cap is clamped.
	check("100000", maxEventLimit)
}

func TestSeriesSinceParsing(t *testing.T) {
	check := func(in string, want time.Duration) {
		assertQueryParse(t, routePathRoot, apiQuerySince, in, seriesSince, want)
	}
	check("2h", 2*time.Hour)
	// A non-positive duration is ignored (d > 0 guard), keeping the default.
	check("0s", defaultSeriesWindow)
	check("100000h", maxSeriesWindow)
}

func TestIsErrorEventClassification(t *testing.T) {
	errorEvents := []Event{
		{Kind: eventKindError},
		{Kind: eventKindHookFailed},
		{Kind: "notify-failed"},
		{Kind: eventKindAction, Status: eventStatusFailed},
		{Kind: eventKindAction, Status: eventStatusError},
		{Kind: eventKindAction, Status: "blocked"},
		{Kind: eventKindAction, Status: "orphan_processes"},
		{Kind: eventKindAction, Status: "preflight_failed"},
		{Kind: eventKindAction, Status: "postflight_failed"},
	}
	for _, e := range errorEvents {
		if !IsErrorEvent(e) {
			t.Fatalf("IsErrorEvent(%+v) = false, want true", e)
		}
	}
	okEvents := []Event{
		{Kind: eventKindAction, Status: "ok"},
		{Kind: eventKindAlert},
		{Kind: "recovered"},
		{Kind: "reload"},
	}
	for _, e := range okEvents {
		if IsErrorEvent(e) {
			t.Fatalf("IsErrorEvent(%+v) = true, want false", e)
		}
	}
}

func TestFilterEventsByKind(t *testing.T) {
	events := []Event{{Kind: eventKindAlert}, {Kind: "recovered"}, {Kind: eventKindAlert}}
	got := filterEvents(events, eventFilter{Kind: eventKindAlert}, 100)
	if len(got) != 2 {
		t.Fatalf("filtered %d events, want 2 alerts", len(got))
	}
	for _, e := range got {
		if e.Kind != eventKindAlert {
			t.Fatalf("kept a non-alert event: %+v", e)
		}
	}
}
