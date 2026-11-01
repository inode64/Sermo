package app

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/appinspect"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/notify"
)

func TestArtifactCheckMapsStatus(t *testing.T) {
	errc := artifactCheck{name: "x", inspect: func(context.Context) appinspect.Report {
		return appinspect.Report{Status: "error: exit 1 (want 0): boom", Output: "stderr:\nboom"}
	}}
	r := errc.Run(context.Background())
	if r.OK || r.Message == "" {
		t.Fatalf("error status must map to not-OK with the detail message: %+v", r)
	}
	if resultOutput(r) != "stderr:\nboom" {
		t.Fatalf("error result must carry the probe output, got %q", resultOutput(r))
	}
	okc := artifactCheck{name: "x", inspect: func(context.Context) appinspect.Report {
		return appinspect.Report{Status: appinspect.StatusOK}
	}}
	if r := okc.Run(context.Background()); !r.OK {
		t.Fatal("ok status must map to OK")
	}
}

// hungLibraryConfig is a catalog with one library on path.
func hungLibraryConfig(name, path string) *config.Config {
	return &config.Config{
		LibraryNames: []string{name},
		Libraries: map[string]*config.Document{
			name: {Name: name, Body: map[string]any{
				"name":      name,
				"preflight": map[string]any{"file": map[string]any{"type": "file", "path": path}},
			}},
		},
	}
}

// The stat of a library on a dead mount takes no context, so the inspection has
// to stop waiting for it or the watch's stuck cycle wedges a reload and a shutdown.
func TestLibraryHungStatLeavesTheMonitorRunning(t *testing.T) {
	hung := newHungRead(t)
	path := filepath.Join(t.TempDir(), "libhung.so")
	cfg := hungLibraryConfig("libhung", path)
	stat := func(string) (fs.FileInfo, error) {
		hung.block()
		return nil, fs.ErrNotExist
	}
	stuck := &Watch{Name: libraryWatchNamePrefix + "libhung", FireOnFail: true, Check: artifactCheck{
		name: "libhung",
		inspect: func(ctx context.Context) appinspect.Report {
			return appinspect.InspectCategoryOne(ctx, nil, cfg, config.CategoryLibrary, "libhung", appinspect.WithStat(stat, 20*time.Millisecond))
		},
	}}
	assertMonitorSurvives(t, hung, stuck)
}

// A hung binary is no sample: it keeps what the last one said, and a firing watch
// neither recovers nor gets a second opening until a real reading arrives.
func TestArtifactCheckReadsAHungBinaryAsUnavailable(t *testing.T) {
	samples := NewArtifactSamples()
	reports := []appinspect.Report{
		{Name: "demo", Status: "error: exit 1"},
		{Name: "demo", Installed: true, Hung: true, Status: "error: /mnt/data/demo: hung path: no answer within 5s"},
		{Name: "demo", Installed: true, Hung: true, Status: "error: /mnt/data/demo: hung path: an earlier stat is still blocked"},
	}
	check := artifactCheck{
		name: "demo", samples: samples, store: (*ArtifactSamples).StoreAppReport,
		inspect: func(context.Context) appinspect.Report {
			report := reports[0]
			reports = reports[1:]
			return report
		},
		activeUsers: func() []string { t.Fatal("a hung binary has no grade to look up"); return nil },
	}
	broken := check
	broken.activeUsers = nil
	var kinds []string
	w := &Watch{Name: "demo", App: "demo", FireOnFail: true, Sleep: func(time.Duration) {}, Emit: func(e Event) { kinds = append(kinds, e.Kind) }}

	w.Check = broken
	w.RunCycle(context.Background())
	if len(kinds) != 1 || kinds[0] != eventKindFiring {
		t.Fatalf("first cycle events = %v, want one firing", kinds)
	}
	sampled, _ := samples.AppReport("demo")

	w.Check = check
	w.RunCycle(context.Background())
	if len(kinds) != 2 || kinds[1] != eventKindError || !w.firing {
		t.Fatalf("hung cycle events = %v firing = %v, want the check unavailable and the episode open", kinds, w.firing)
	}
	if got, _ := samples.AppReport("demo"); got.Status != sampled.Status {
		t.Fatalf("hung cycle replaced the sample: %+v", got)
	}
}

// TestAppWatchNotifiesOnceAndRecovers verifies the app-watch reuses the Watch
// cycle: it notifies once on the rising edge (first error), does not re-notify
// while it stays in error, emits recovered when it returns to ok, and tags every
// event on the App dimension (not Watch).
func TestAppWatchStartupObserveOnlySkipsNotify(t *testing.T) {
	n := &fakeNotifier{name: "ops"}
	var events []Event
	settling := NewSettling(nil)
	settling.Reset([]string{SettlingAppKey("salt-minion")})
	check := &scriptedCheck{results: []checks.Result{
		{Check: "salt-minion", OK: false, Message: "error: exit 1 (want 0): boom"},
		{Check: "salt-minion", OK: false, Message: "error: exit 1 (want 0): boom"},
	}}
	w := &Watch{
		Name:       "salt-minion",
		App:        "salt-minion",
		CheckType:  "app",
		Check:      check,
		FireOnFail: true,
		Notifiers:  []notify.Notifier{n},
		Settling:   settling,
		Emit:       func(e Event) { events = append(events, e) },
	}

	w.RunCycle(context.Background())
	if len(n.msgs) != 0 || hasEventKind(events, eventKindFiring) {
		t.Fatalf("observe-only app-watch must not notify or fire, events=%v msgs=%d", events, len(n.msgs))
	}
	if !settling.Observed(SettlingAppKey("salt-minion")) {
		t.Fatal("observe-only app-watch must mark the app observed")
	}

	w.RunCycle(context.Background())
	if len(n.msgs) != 1 {
		t.Fatalf("second cycle must notify once, got %d", len(n.msgs))
	}
}

func TestAppWatchNotifiesOnceAndRecovers(t *testing.T) {
	n := &fakeNotifier{name: "ops"}
	var events []Event
	check := &scriptedCheck{results: []checks.Result{
		{Check: "salt-minion", OK: false, Message: "error: exit 1 (want 0): boom"},
		{Check: "salt-minion", OK: false, Message: "error: exit 1 (want 0): boom"},
		{Check: "salt-minion", OK: true, Message: "ok"},
	}}
	w := &Watch{
		Name:       "salt-minion",
		App:        "salt-minion",
		CheckType:  "app",
		Check:      check,
		FireOnFail: true,
		Notifiers:  []notify.Notifier{n},
		Emit:       func(e Event) { events = append(events, e) },
	}

	w.RunCycle(context.Background()) // error  -> firing + notify (rising edge)
	w.RunCycle(context.Background()) // error  -> firing, no re-notify
	w.RunCycle(context.Background()) // ok     -> recovered + recovery notice

	if len(n.msgs) != 2 {
		t.Fatalf("app-watch must notify once on the rising edge and once on recovery, got %d", len(n.msgs))
	}
	if !strings.Contains(n.msgs[1].Subject, "salt-minion: "+recoveredMessagePrefix) {
		t.Fatalf("second notification = %q, want the recovery", n.msgs[1].Subject)
	}
	for _, e := range events {
		if e.App != "salt-minion" || e.Watch != "" {
			t.Fatalf("app-watch event must be on the App dimension, got %+v", e)
		}
	}
	if !hasEventKind(events, eventKindFiring) || !hasEventKind(events, eventKindRecovered) {
		t.Fatalf("want firing and recovered, kinds = %v", eventKinds(events))
	}
	if !hasEvent(events, eventKindNotify) {
		t.Fatalf("expected a notify event, got %v", events)
	}
	if !hasEventMessage(events, eventKindFiring, "error: exit 1") {
		t.Fatalf("firing event must carry the error detail, got %v", events)
	}
}
