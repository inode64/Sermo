package app

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/severity"
	"sermo/internal/state"
)

// publishForTest keeps older test setup concise. Production code provides check
// types through publishWithCheckTypes, so web consumers never receive an
// untyped snapshot.
func (s *Snapshots) publishForTest(service string, cache map[string]checks.Result, ran map[string]bool) {
	s.publishConfigured(service, cache, ran, nil, "")
}

// publishWithCheckTypes is test-only shorthand for snapshots without a
// configuration generation. Production publishers always carry a config ID.
func (s *Snapshots) publishWithCheckTypes(service string, cache map[string]checks.Result, ran map[string]bool, checkTypes map[string]string) {
	typedCache := make(map[string]checks.Result, len(cache))
	typedRan := make(map[string]bool, len(ran))
	for name, result := range cache {
		if checkTypes[name] == "" {
			continue
		}
		typedCache[name], typedRan[name] = result, ran[name]
	}
	s.publishConfigured(service, typedCache, typedRan, checkTypes, "")
}

// publish is test-only shorthand for watch snapshots without a configuration
// generation. Production publishers always carry a config ID.
func (s *WatchSnapshots) publish(watch, checkType string, result checks.Result) {
	s.publishConfigured(watch, checkType, result, "")
}

func TestSnapshotsRoundTrip(t *testing.T) {
	s := NewSnapshots()
	if s.Get("web") != nil {
		t.Fatal("unobserved service should have no snapshot")
	}
	s.publishForTest("web", map[string]checks.Result{
		"http": {Check: "http", OK: true, Message: "status 200"},
		"warn": {Check: "warn", OK: false, Optional: true},
	}, map[string]bool{"http": true, "warn": true})
	got := s.Get("web")
	if len(got) != 2 || !got["http"].OK || got["http"].Message != "status 200" {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
	if got["http"].Observation != checks.ObservationHealthy || got["warn"].Observation != checks.ObservationFailing {
		t.Fatalf("snapshot observations = http:%q warn:%q", got["http"].Observation, got["warn"].Observation)
	}
	if got["warn"].OK || !got["warn"].Optional {
		t.Fatalf("optional/failed not preserved: %+v", got["warn"])
	}
}

func TestSnapshotsPublishRanFlag(t *testing.T) {
	s := NewSnapshots()
	s.publishForTest("web", map[string]checks.Result{
		"fast": {Check: "fast", OK: true},
		"slow": {Check: "slow", OK: true, Message: "cached"},
	}, map[string]bool{"fast": true})

	got := s.Get("web")
	if !got["fast"].Ran {
		t.Fatal("fast check should show ran=true")
	}
	if got["slow"].Ran {
		t.Fatal("interval-deferred slow check should show ran=false")
	}
}

func TestSnapshotsPublishWithCheckTypes(t *testing.T) {
	s := NewSnapshots()
	s.publishWithCheckTypes("web", map[string]checks.Result{
		"http": {Check: "http", OK: true},
	}, map[string]bool{"http": true}, map[string]string{"http": checks.CheckTypeHTTP})

	if got := s.Get("web")["http"].CheckType; got != checks.CheckTypeHTTP {
		t.Fatalf("snapshot CheckType = %q, want %q", got, checks.CheckTypeHTTP)
	}
}

func TestSnapshotsPublishWithCheckTypesDropsUntypedEntries(t *testing.T) {
	s := NewSnapshots()
	s.publishWithCheckTypes("web", map[string]checks.Result{
		"http": {Check: "http", OK: true},
	}, map[string]bool{"http": true}, nil)
	if got := s.Get("web"); len(got) != 0 {
		t.Fatalf("untyped snapshots = %+v, want none", got)
	}
}

func TestSnapshotsPublishAtOnlyWhenRan(t *testing.T) {
	t0 := time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(30 * time.Second)
	s := NewSnapshots()
	s.now = func() time.Time { return t0 }

	s.publishForTest("web", map[string]checks.Result{
		"fast": {Check: "fast", OK: true},
		"slow": {Check: "slow", OK: true},
	}, map[string]bool{"fast": true, "slow": true})

	s.now = func() time.Time { return t1 }
	s.publishForTest("web", map[string]checks.Result{
		"fast": {Check: "fast", OK: true},
		"slow": {Check: "slow", OK: true, Message: "cached"},
	}, map[string]bool{"fast": true})

	got := s.Get("web")
	if got["fast"].At != t1 {
		t.Fatalf("fast At = %v, want %v", got["fast"].At, t1)
	}
	if got["slow"].At != t0 {
		t.Fatalf("cached slow At = %v, want prior %v", got["slow"].At, t0)
	}
	if got["slow"].Ran {
		t.Fatal("slow should not show ran on second cycle")
	}
}

func TestPersistentSnapshotsHydrateAndStore(t *testing.T) {
	t0 := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	store := &snapshotStoreFake{
		service: map[string]map[string]state.CheckSnapshotRecord{
			"web": {
				"http": {CheckType: checks.CheckTypeHTTP, Observation: checks.ObservationUnavailable, OK: true, Unavailable: true, Message: "status unavailable", Data: map[string]any{"status": float64(200)}, Ran: true, At: t0, Severity: string(severity.Warning)},
			},
		},
	}
	s, err := NewPersistentSnapshots(store, nil)
	if err != nil {
		t.Fatalf("NewPersistentSnapshots: %v", err)
	}
	if got := s.Get("web")["http"]; got.CheckType != checks.CheckTypeHTTP || got.Observation != checks.ObservationUnavailable || !got.OK || !got.Unavailable || got.Message != "status unavailable" || got.Data["status"] != float64(200) || !got.At.Equal(t0) || got.Severity != string(severity.Warning) {
		t.Fatalf("hydrated snapshot = %+v", got)
	}

	t1 := t0.Add(time.Minute)
	s.now = func() time.Time { return t1 }
	s.publishForTest("web", map[string]checks.Result{
		"tcp": {Check: "tcp", OK: false, Unavailable: true, Message: "connection refused", Data: map[string]any{"port": float64(443)}, Severity: severity.Warning},
	}, map[string]bool{"tcp": true})

	service := store.service["web"]
	if len(service) != 1 {
		t.Fatalf("stored service snapshots = %+v, want replaced current rows", service)
	}
	if got := service["tcp"]; got.Observation != checks.ObservationUnavailable || got.OK || !got.Unavailable || got.Message != "connection refused" || got.Data["port"] != float64(443) || !got.At.Equal(t1) || got.Severity != string(severity.Warning) {
		t.Fatalf("stored snapshot = %+v", got)
	}
}

// A load that skipped an undecodable row still restores every other snapshot,
// so a restart does not re-open the episodes they carry.
func TestPersistentSnapshotsHydrateAPartialLoad(t *testing.T) {
	record := state.CheckSnapshotRecord{CheckType: checks.CheckTypeService, Observation: checks.ObservationFailing}
	store := &snapshotStoreFake{
		service: map[string]map[string]state.CheckSnapshotRecord{"web": {"service": record}},
		watch:   map[string]map[string]state.CheckSnapshotRecord{"disk": {"result": record}},
		loadErr: errors.New("web/http skipped: invalid observation \"\""),
	}
	services, err := NewPersistentSnapshots(store, nil)
	if err == nil || services.Get("web")["service"].Observation != checks.ObservationFailing {
		t.Fatalf("service snapshots = %+v, err %v; want the decodable row and the error", services.Get("web"), err)
	}
	watches, err := NewPersistentWatchSnapshots(store, nil)
	if err == nil || watches.byWatch["disk"]["result"].Observation != checks.ObservationFailing {
		t.Fatalf("watch snapshots = %+v, err %v; want the decodable row and the error", watches.byWatch, err)
	}
}

func TestWatchSnapshotsKeepMetricSlots(t *testing.T) {
	t0 := time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC)
	s := NewWatchSnapshots()
	s.now = func() time.Time { return t0 }

	s.publish("uplink", checks.CheckTypeICMP, checks.Result{
		Check: "uplink",
		Data:  map[string]any{checks.DataKeyMetric: checks.NetMetricState, checks.DataKeyValue: checks.NetStateUp},
	})
	s.publish("uplink", checks.CheckTypeICMP, checks.Result{
		Check: "uplink",
		Data:  map[string]any{checks.DataKeyMetric: checks.IcmpMetricLatency, checks.DataKeyValue: 12.5},
	})

	got := s.Get("uplink", checks.CheckTypeICMP)
	if len(got) != 2 {
		t.Fatalf("watch snapshots = %+v, want two metric slots", got)
	}
	if got[0].Data[checks.DataKeyMetric] != checks.IcmpMetricLatency || got[1].Data[checks.DataKeyMetric] != checks.NetMetricState {
		t.Fatalf("watch snapshot order/data = %+v", got)
	}
	if wrongType := s.Get("uplink", checks.CheckTypeNet); len(wrongType) != 0 {
		t.Fatalf("wrong check type returned snapshots: %+v", wrongType)
	}
}

func TestPersistentWatchSnapshotsHydrateAndStore(t *testing.T) {
	t0 := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	store := &snapshotStoreFake{
		watch: map[string]map[string]state.CheckSnapshotRecord{
			"clock": {
				checks.DataKeyResult: {
					CheckType: "clock", Observation: checks.ObservationFailing, OK: false, Message: "offset 1200ms",
					Data: map[string]any{"offset_ms": float64(1200)}, Ran: true, At: t0, Severity: string(severity.Warning),
				},
			},
		},
	}
	s, err := NewPersistentWatchSnapshots(store, nil)
	if err != nil {
		t.Fatalf("NewPersistentWatchSnapshots: %v", err)
	}
	if got := s.Get("clock", "clock"); len(got) != 1 || got[0].Message != "offset 1200ms" || got[0].Data["offset_ms"] != float64(1200) || got[0].Severity != string(severity.Warning) {
		t.Fatalf("hydrated watch snapshots = %+v", got)
	}

	t1 := t0.Add(time.Minute)
	s.now = func() time.Time { return t1 }
	s.publish("clock", "clock", checks.Result{
		Check:       "clock",
		OK:          true,
		Unavailable: true,
		Message:     "offset 4ms",
		Data:        map[string]any{"offset_ms": float64(4)},
		Severity:    severity.Warning,
	})

	got := store.watch["clock"]["clock"]
	if got.CheckType != "clock" || !got.OK || !got.Unavailable || got.Message != "offset 4ms" || got.Data["offset_ms"] != float64(4) || !got.At.Equal(t1) || got.Severity != string(severity.Warning) {
		t.Fatalf("stored watch snapshot = %+v", got)
	}
}

func TestWorkerPublishesCache(t *testing.T) {
	var published map[string]checks.Result
	w := &Worker{
		Service: "web",
		Checks: func(context.Context, checks.Deps) map[string]checks.Result {
			return map[string]checks.Result{"http": {Check: "http", OK: true}}
		},
		Publish: func(c map[string]checks.Result, _ map[string]bool) { published = c },
	}
	w.RunCycle(context.Background())
	if published == nil || !published["http"].OK {
		t.Fatalf("worker did not publish its cache: %+v", published)
	}
}

func TestWorkerPausedDoesNotPublish(t *testing.T) {
	called := false
	w := &Worker{
		Service:  "web",
		IsPaused: func() bool { return true },
		Checks:   func(context.Context, checks.Deps) map[string]checks.Result { return nil },
		Publish:  func(map[string]checks.Result, map[string]bool) { called = true },
	}
	w.RunCycle(context.Background())
	if called {
		t.Fatal("a paused cycle must not publish a snapshot")
	}
}

type snapshotStoreFake struct {
	service map[string]map[string]state.CheckSnapshotRecord
	watch   map[string]map[string]state.CheckSnapshotRecord
	// loadErr is returned with the rows, the way a load that skipped an
	// undecodable row reports it.
	loadErr error
	// writeErr fails every update; writes counts the update calls.
	writeErr error
	writes   int
}

func (s *snapshotStoreFake) ServiceCheckSnapshots() (map[string]map[string]state.CheckSnapshotRecord, error) {
	return s.service, s.loadErr
}

func (s *snapshotStoreFake) UpdateServiceCheckSnapshots(deltas map[string]state.CheckSnapshotDelta) error {
	return s.update(&s.service, deltas)
}

func (s *snapshotStoreFake) WatchCheckSnapshots() (map[string]map[string]state.CheckSnapshotRecord, error) {
	return s.watch, s.loadErr
}

func (s *snapshotStoreFake) UpdateWatchCheckSnapshots(deltas map[string]state.CheckSnapshotDelta) error {
	return s.update(&s.watch, deltas)
}

func (s *snapshotStoreFake) update(table *map[string]map[string]state.CheckSnapshotRecord, deltas map[string]state.CheckSnapshotDelta) error {
	s.writes++
	if s.writeErr != nil {
		return s.writeErr
	}
	if *table == nil {
		*table = map[string]map[string]state.CheckSnapshotRecord{}
	}
	for group, delta := range deltas {
		rows := (*table)[group]
		if rows == nil {
			rows = map[string]state.CheckSnapshotRecord{}
			(*table)[group] = rows
		}
		for _, name := range delta.Delete {
			delete(rows, name)
		}
		maps.Copy(rows, delta.Upsert)
	}
	return nil
}

func TestSnapshotPersistencePreservesAllFields(t *testing.T) {
	for _, data := range []map[string]any{nil, {}, {"count": 3}} {
		record := state.CheckSnapshotRecord{
			CheckType: checks.CheckTypeLog, ConfigID: "config-v1", Observation: checks.ObservationUnavailable,
			OK: true, Condition: true, Optional: true, Skipped: true, Unavailable: true,
			Message: "read failed", Data: data, Ran: true, At: time.Unix(123, 0), Severity: string(severity.Warning),
		}
		snapshot := snapshotFromRecord(record)
		persisted := snapshotRecord(snapshot)
		if !reflect.DeepEqual(record, persisted) {
			t.Fatalf("snapshot round trip = %+v, want %+v", persisted, record)
		}
	}
}

func TestCheckSnapshotIsolatesMutableResultData(t *testing.T) {
	result := checks.Result{Data: map[string]any{"count": 3}}
	snapshot := checkSnapshotFromResult(result)
	result.Data["count"] = 4
	if snapshot.Data["count"] != 3 {
		t.Fatalf("snapshot changed with cached result: %v", snapshot.Data)
	}
}

// A publish writes only readings that changed: re-running a check with the
// same reading leaves the stored row (and its time) alone, a changed reading
// rewrites that row, and a check the service no longer publishes is deleted.
func TestPersistentSnapshotsWriteOnlyChangedReadings(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	store := &snapshotStoreFake{}
	s, err := NewPersistentSnapshots(store, nil)
	if err != nil {
		t.Fatalf("NewPersistentSnapshots: %v", err)
	}
	publishAt := func(at time.Time, cache map[string]checks.Result) {
		t.Helper()
		s.now = func() time.Time { return at }
		ran := map[string]bool{}
		for name := range cache {
			ran[name] = true
		}
		s.publishForTest("web", cache, ran)
	}
	healthy := map[string]checks.Result{
		"http":    {Check: "http", OK: true, Message: "status 200", Data: map[string]any{"status": 200}},
		"service": {Check: "service", OK: true, Message: "active"},
	}
	publishAt(t0, healthy)
	if store.writes != 1 || len(store.service["web"]) != 2 {
		t.Fatalf("first publish: writes=%d rows=%+v, want one write of both rows", store.writes, store.service["web"])
	}

	publishAt(t0.Add(time.Minute), healthy)
	if store.writes != 1 {
		t.Fatalf("unchanged publish wrote: writes=%d", store.writes)
	}
	if got := s.Get("web")["http"].At; !got.Equal(t0.Add(time.Minute)) {
		t.Fatalf("published time = %v, want the latest run in memory", got)
	}
	if got := store.service["web"]["http"].At; !got.Equal(t0) {
		t.Fatalf("stored time = %v, want the last written one", got)
	}

	changed := map[string]checks.Result{
		"http":    {Check: "http", OK: false, Message: "status 503", Data: map[string]any{"status": 503}},
		"service": healthy["service"],
	}
	publishAt(t0.Add(2*time.Minute), changed)
	if store.writes != 2 || store.service["web"]["http"].Message != "status 503" || !store.service["web"]["service"].At.Equal(t0) {
		t.Fatalf("changed publish: writes=%d rows=%+v, want only the http row rewritten", store.writes, store.service["web"])
	}

	publishAt(t0.Add(3*time.Minute), map[string]checks.Result{"service": healthy["service"]})
	if _, kept := store.service["web"]["http"]; kept || store.writes != 3 {
		t.Fatalf("removed check: writes=%d rows=%+v, want the http row deleted", store.writes, store.service["web"])
	}
}

// A reading decoded from the store (numbers as float64) equals the same
// reading fresh from a check, so a restart does not rewrite every row.
func TestPersistentSnapshotsHydratedReadingIsNotRewritten(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	store := &snapshotStoreFake{service: map[string]map[string]state.CheckSnapshotRecord{"web": {
		"http": {Observation: checks.ObservationHealthy, OK: true, Message: "status 200", Data: map[string]any{"status": float64(200)}, Ran: true, At: t0},
	}}}
	s, err := NewPersistentSnapshots(store, nil)
	if err != nil {
		t.Fatalf("NewPersistentSnapshots: %v", err)
	}
	s.now = func() time.Time { return t0.Add(time.Minute) }
	s.publishForTest("web", map[string]checks.Result{
		"http": {Check: "http", OK: true, Message: "status 200", Data: map[string]any{"status": 200}},
	}, map[string]bool{"http": true})
	if store.writes != 0 {
		t.Fatalf("hydrated reading rewritten: writes=%d", store.writes)
	}
}

// A failed write is reported and retried by the next publish of the same
// reading, since the stored rows did not change.
func TestPersistentSnapshotsRetryAFailedWrite(t *testing.T) {
	store := &snapshotStoreFake{writeErr: errors.New("disk full")}
	var reported []error
	s, err := NewPersistentSnapshots(store, func(err error) { reported = append(reported, err) })
	if err != nil {
		t.Fatalf("NewPersistentSnapshots: %v", err)
	}
	cache := map[string]checks.Result{"http": {Check: "http", OK: true}}
	s.publishForTest("web", cache, map[string]bool{"http": true})
	if len(reported) != 1 || store.writes != 1 {
		t.Fatalf("failed write: reported=%v writes=%d", reported, store.writes)
	}
	store.writeErr = nil
	s.publishForTest("web", cache, map[string]bool{"http": true})
	if store.writes != 2 || !store.service["web"]["http"].OK {
		t.Fatalf("retry: writes=%d rows=%+v, want the reading written", store.writes, store.service["web"])
	}
}

// Flush writes the times unchanged publishes left behind, once, so a clean
// restart hydrates current readings.
func TestSnapshotsFlushWritesNewerTimes(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	services := &snapshotStoreFake{}
	s, err := NewPersistentSnapshots(services, nil)
	if err != nil {
		t.Fatalf("NewPersistentSnapshots: %v", err)
	}
	watchStore := &snapshotStoreFake{}
	w, err := NewPersistentWatchSnapshots(watchStore, nil)
	if err != nil {
		t.Fatalf("NewPersistentWatchSnapshots: %v", err)
	}
	for i := range 3 {
		at := t0.Add(time.Duration(i) * time.Minute)
		s.now, w.now = func() time.Time { return at }, func() time.Time { return at }
		s.publishForTest("web", map[string]checks.Result{"http": {Check: "http", OK: true}}, map[string]bool{"http": true})
		w.publish("clock", checks.CheckTypeClock, checks.Result{Check: "clock", OK: true, Message: "offset 4ms"})
	}
	if services.writes != 1 || watchStore.writes != 1 {
		t.Fatalf("unchanged publishes wrote: services=%d watches=%d", services.writes, watchStore.writes)
	}

	s.Flush()
	w.Flush()
	latest := t0.Add(2 * time.Minute)
	if got := services.service["web"]["http"].At; services.writes != 2 || !got.Equal(latest) {
		t.Fatalf("service flush: writes=%d at=%v, want %v", services.writes, got, latest)
	}
	if got := watchStore.watch["clock"]["clock"].At; watchStore.writes != 2 || !got.Equal(latest) {
		t.Fatalf("watch flush: writes=%d at=%v, want %v", watchStore.writes, got, latest)
	}
	s.Flush()
	w.Flush()
	if services.writes != 2 || watchStore.writes != 2 {
		t.Fatalf("second flush wrote again: services=%d watches=%d", services.writes, watchStore.writes)
	}
}

// Every stored field takes part in the change comparison except the time.
func TestStoredSnapshotSameReadingComparesEveryField(t *testing.T) {
	base := CheckSnapshot{CheckType: checks.CheckTypeHTTP, ConfigID: "v1", Observation: checks.ObservationHealthy, OK: true,
		Message: "ok", Data: map[string]any{"status": 200}, Ran: true, At: time.Unix(100, 0), Severity: string(severity.Warning)}
	if !newStoredSnapshot(base).sameReading(newStoredSnapshot(base), true) {
		t.Fatal("identical snapshots differ")
	}
	later := base
	later.At = base.At.Add(time.Second)
	if !newStoredSnapshot(base).sameReading(newStoredSnapshot(later), false) || newStoredSnapshot(base).sameReading(newStoredSnapshot(later), true) {
		t.Fatal("time must matter only withTime")
	}
	record := reflect.ValueOf(state.CheckSnapshotRecord{})
	for i := range record.NumField() {
		field := record.Type().Field(i)
		if field.Name == "At" {
			continue
		}
		changed := base
		value := reflect.ValueOf(&changed).Elem().FieldByName(field.Name)
		switch value.Kind() {
		case reflect.String:
			value.SetString(value.String() + "-changed")
		case reflect.Bool:
			value.SetBool(!value.Bool())
		case reflect.Map:
			value.Set(reflect.ValueOf(map[string]any{"status": 503}))
		default:
			t.Fatalf("field %s of kind %s is not covered by this test", field.Name, value.Kind())
		}
		if newStoredSnapshot(base).sameReading(newStoredSnapshot(changed), false) {
			t.Fatalf("a change of %s is not detected", field.Name)
		}
	}
}
