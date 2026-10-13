package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/severity"
)

func TestFileWatchSizeMessageMatchesSnapshotAndHook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dead.letter")
	writeSize(t, path, 10)
	h := &fileWatchHarness{}
	w := h.watcher(path, false, fileCond{sizeOp: ">", sizeValue: 0.000001})
	var latest checks.Result
	w.publish = func(_, _ string, res checks.Result) { latest = res }
	w.runCycle(t.Context())
	w.runCycle(t.Context())
	if latest.OK || len(h.fired) != 1 {
		t.Fatalf("snapshot=%+v, hooks=%v; want one firing and a persistent breach", latest, h.fired)
	}
	if latest.Message != h.fired[0][sermoEnvMessage] || !strings.HasSuffix(latest.Message, "size 10 > 0.000001") {
		t.Fatalf("snapshot %q differs from hook %q or the configured threshold", latest.Message, h.fired[0][sermoEnvMessage])
	}
}

func TestFileWatchHealthIncidentLifecycle(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "monitor", true: "dry-run"}[dryRun], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "dead.letter")
			writeSize(t, path, 100)
			var events []Event
			watch, warning := buildFileWatch("dead-letter", map[string]any{"dry_run": dryRun}, map[string]any{
				"paths": []any{path}, "absent_ok": true, "size": map[string]any{"op": ">", "value": 0},
			}, Deps{Emit: func(e Event) { events = append(events, e) }}, time.Minute)
			if warning != "" {
				t.Fatal(warning)
			}
			var latest checks.Result
			watch.Publish = func(_, _ string, res checks.Result) { latest = res }
			// Startup observation publishes the real verdict but stays silent.
			watch.Cycle(withObserveOnly(t.Context(), true))
			if latest.OK || len(events) != 0 {
				t.Fatalf("startup snapshot=%+v, events=%+v", latest, events)
			}
			watch.RunCycle(t.Context())
			watch.RunCycle(t.Context())
			if latest.OK {
				t.Fatal("breach published as healthy")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			watch.RunCycle(t.Context())
			watch.RunCycle(t.Context())
			if !latest.OK {
				t.Fatal("absent dead-letter file must recover")
			}
			var routed []Event
			for _, event := range events {
				if eventNeedsNotification(event) {
					routed = append(routed, event)
				}
			}
			if len(routed) != 2 || routed[0].Kind != eventKindFiring || routed[1].Kind != eventKindRecovered {
				t.Fatalf("global incidents=%+v; want one firing and one recovery", routed)
			}
		})
	}
}

func TestFileWatchHealthIncludesEveryPath(t *testing.T) {
	dir := t.TempDir()
	fresh, stale := filepath.Join(dir, "fresh"), filepath.Join(dir, "stale")
	now := time.Now()
	writeSize(t, fresh, 1)
	writeSize(t, stale, 1)
	if err := os.Chtimes(stale, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	w := &fileWatcher{paths: []string{fresh, stale}, cond: fileCond{olderThan: time.Hour}, now: func() time.Time { return now }}
	var latest checks.Result
	w.publish = func(_, _ string, res checks.Result) { latest = res }
	w.runCycle(context.Background())
	if latest.OK {
		t.Fatal("a fresh first path must not hide a stale second path")
	}
	if err := os.Remove(stale); err != nil {
		t.Fatal(err)
	}
	w.runCycle(context.Background())
	if latest.OK {
		t.Fatal("a present first path must not hide a missing second root")
	}
	w.absentOK = true
	w.runCycle(context.Background())
	if !latest.OK {
		t.Fatal("only a fresh path remains and missing paths are allowed")
	}
}

func TestFileWatchHealthSummaryNamesFailingPath(t *testing.T) {
	for _, recursive := range []bool{false, true} {
		t.Run(map[bool]string{false: "multiple roots", true: "recursive"}[recursive], func(t *testing.T) {
			dir := t.TempDir()
			fresh, stale := filepath.Join(dir, "fresh"), filepath.Join(dir, "stale")
			writeSize(t, fresh, 1)
			writeSize(t, stale, 20)
			now := time.Now()
			if err := os.Chtimes(stale, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
				t.Fatal(err)
			}
			w := (&fileWatchHarness{}).watcher(fresh, recursive, fileCond{olderThan: time.Hour})
			w.paths = []string{fresh, stale}
			if recursive {
				w.paths = []string{dir}
			}
			w.summary = "${path}: ${trigger} ${value}"
			w.now = func() time.Time { return now }
			var latest checks.Result
			w.publish = func(_, _ string, res checks.Result) { latest = res }
			w.runCycle(t.Context())
			if latest.OK || latest.Message != stale+": older_than 2h" || latest.Data[checks.DataKeyPath] != stale || latest.Data[checks.DataKeySize] != int64(20) {
				t.Fatalf("stale path must own the diagnostic: %+v", latest)
			}
			if recursive {
				if latest.Data[watchReadingFieldEntries] != 2 {
					t.Fatalf("selecting a failing child must preserve the entry count: %+v", latest)
				}
				return
			}
			w.cond.sizeOp, w.cond.sizeValue = ">", 10
			w.runCycle(t.Context())
			if latest.OK || latest.Message != stale+": size_threshold 20" {
				t.Fatalf("size breach must use size rather than age: %+v", latest)
			}
			if err := os.Remove(stale); err != nil {
				t.Fatal(err)
			}
			w.runCycle(t.Context())
			if latest.OK || latest.Message != stale+": not found" || latest.Data[checks.DataKeySize] != nil {
				t.Fatalf("missing root must not report another root's sample: %+v", latest)
			}
			if err := os.Remove(fresh); err != nil {
				t.Fatal(err)
			}
			w.absentOK = true
			w.runCycle(t.Context())
			if !latest.OK || latest.Message != fresh+": absent (ok)" {
				t.Fatalf("allowed absence must recover without an unresolved summary: %+v", latest)
			}
		})
	}
}

func TestFileWatchRestoresIncidentAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dead.letter")
	writeSize(t, path, 100)
	var events []Event
	deps := Deps{WatchState: openRuleStateStore(t), Emit: func(e Event) {
		if eventNeedsNotification(e) {
			events = append(events, e)
		}
	}}
	build := func() *Watch {
		t.Helper()
		watch, warning := buildFileWatch("dead-letter", nil, map[string]any{
			"paths": []any{path}, "absent_ok": true, "size": map[string]any{"op": ">", "value": 0},
		}, deps, time.Minute)
		if warning != "" {
			t.Fatal(warning)
		}
		return watch
	}
	build().RunCycle(t.Context())
	restarted := build()
	restarted.Cycle(withObserveOnly(t.Context(), true))
	restarted.RunCycle(t.Context())
	writeSize(t, path, 0)
	restarted.RunCycle(t.Context())
	if len(events) != 2 || events[0].Kind != eventKindFiring || events[1].Kind != eventKindRecovered {
		t.Fatalf("restart duplicated or lost the incident: %+v", events)
	}
}

func TestFileWatchRecoversOnlyAfterEveryPathIsHealthy(t *testing.T) {
	dir := t.TempDir()
	large, stale := filepath.Join(dir, "large"), filepath.Join(dir, "stale")
	writeSize(t, large, 20)
	writeSize(t, stale, 1)
	now := time.Now()
	if err := os.Chtimes(stale, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	check := map[string]any{
		"paths": []any{large, stale}, "size": map[string]any{"op": ">", "value": 10},
		"older_than": "1h", "summary": "${path}: ${trigger} ${value}", "severity": "warning",
	}
	settling := NewSettling(nil)
	settling.Reset([]string{SettlingWatchKey("files")})
	var events []Event
	watch, warning := buildFileWatch("files", map[string]any{"check": check}, check, Deps{
		Now: func() time.Time { return now }, Settling: settling,
		Emit: func(e Event) {
			if eventNeedsNotification(e) {
				events = append(events, e)
			}
		},
	}, time.Minute)
	if warning != "" {
		t.Fatal(warning)
	}
	var latest checks.Result
	watch.Publish = func(_, _ string, res checks.Result) { latest = res }
	paused := false
	watch.IsPaused = func() bool { return paused }
	watch.RunCycle(t.Context())
	if latest.OK || len(events) != 0 || !settling.Observed(SettlingWatchKey("files")) {
		t.Fatalf("startup: snapshot=%+v, events=%+v", latest, events)
	}
	for _, step := range []struct {
		name, path string
		update     func() error
		ok         bool
		events     int
	}{
		{name: "size breach", path: large, events: 1},
		{name: "age breach remains", path: stale, events: 1, update: func() error { return os.Truncate(large, 1) }},
		{name: "all healthy", path: large, ok: true, events: 2, update: func() error { return os.Chtimes(stale, now, now) }},
		{name: "paused deletion", path: large, ok: true, events: 2, update: func() error { paused = true; return os.Remove(stale) }},
		{name: "resumed missing root", path: stale, events: 3, update: func() error { paused = false; return nil }},
		{name: "root restored", path: large, ok: true, events: 4, update: func() error { return os.WriteFile(stale, []byte("a"), 0o644) }},
	} {
		t.Run(step.name, func(t *testing.T) {
			if step.update != nil {
				if err := step.update(); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				watch.RunCycle(t.Context())
				if latest.OK != step.ok || latest.Data[checks.DataKeyPath] != step.path || len(events) != step.events {
					t.Fatalf("snapshot=%+v, events=%+v", latest, events)
				}
			}
		})
	}
	for i, kind := range []string{eventKindFiring, eventKindRecovered, eventKindFiring, eventKindRecovered} {
		if len(events) <= i || events[i].Kind != kind || events[i].Severity != severity.Warning {
			t.Fatalf("episode lifecycle or severity changed: %+v", events)
		}
	}
}
