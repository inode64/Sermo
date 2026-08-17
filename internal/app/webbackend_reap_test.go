package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
)

// straysSnapshotBackend publishes one strays result and returns a backend whose
// single entry declares that check. Publish stamps the snapshot with the real
// clock, so the backend's own clock is offset from that instant rather than set to
// a fixed one: a clock in the past would make every snapshot look current.
func straysSnapshotBackend(t *testing.T, count int, age time.Duration) *WebBackend {
	t.Helper()
	snaps := NewSnapshots()
	data := map[string]any{checks.DataKeyType: checks.CheckTypeStrays, checks.DataKeyCount: count}
	snaps.PublishWithCheckTypes("web",
		map[string]checks.Result{"strays": {Check: "strays", OK: count == 0, Data: data}},
		map[string]bool{"strays": true},
		map[string]string{"strays": checks.CheckTypeStrays},
	)
	b := webBackendWithEntry(snaps, []string{"strays"}, map[string]string{"strays": checks.CheckTypeStrays})
	b.entries["web"].interval = time.Minute
	publishedAt := time.Now()
	b.now = func() time.Time { return publishedAt.Add(age) }
	return b
}

// The list must read the published count rather than discover processes: one HTTP
// request renders every service, so a /proc walk per row would put the whole
// fleet's discovery on the browser's critical path.
func TestServiceStrayCountComesFromThePublishedSnapshot(t *testing.T) {
	b := straysSnapshotBackend(t, 3, 0)

	if got := b.serviceStrayCount("web", b.entries["web"]); got != 3 {
		t.Fatalf("stray count = %d, want 3", got)
	}
}

// A snapshot older than the freshness window is not evidence of anything: the
// count reads 0 rather than showing a number the daemon may have stopped
// confirming.
func TestServiceStrayCountIgnoresAStaleSnapshot(t *testing.T) {
	b := straysSnapshotBackend(t, 3, time.Hour)

	if got := b.serviceStrayCount("web", b.entries["web"]); got != 0 {
		t.Fatalf("stale snapshot stray count = %d, want 0", got)
	}
}

// A service may declare several strays checks — the injected one plus bounded
// instances — so the reader must pick one reproducibly. Ranging over the checkTypes
// map would answer differently between requests.
func TestServiceStrayCountIsDeterministicWithSeveralStraysChecks(t *testing.T) {
	snaps := NewSnapshots()
	names := []string{"strays", "strays-growing", "strays-high"}
	results := map[string]checks.Result{}
	ran := map[string]bool{}
	types := map[string]string{}
	for i, name := range names {
		// Distinct counts so a different pick would show up as a different number.
		results[name] = checks.Result{Check: name, Data: map[string]any{
			checks.DataKeyType: checks.CheckTypeStrays, checks.DataKeyCount: 10 + i,
		}}
		ran[name] = true
		types[name] = checks.CheckTypeStrays
	}
	snaps.PublishWithCheckTypes("web", results, ran, types)
	b := webBackendWithEntry(snaps, names, types)
	b.entries["web"].interval = time.Minute
	publishedAt := time.Now()
	b.now = func() time.Time { return publishedAt }

	first := b.serviceStrayCount("web", b.entries["web"])
	for range 20 {
		if got := b.serviceStrayCount("web", b.entries["web"]); got != first {
			t.Fatalf("stray count varies between reads: %d then %d", first, got)
		}
	}
	if first != 10 {
		t.Fatalf("stray count = %d, want the first declared check's 10", first)
	}
}

// A service may declare its own stale_binary check beside the injected one. A
// replaced binary that either check found is still a replaced binary, so the
// warning must not depend on which one is declared first.
func TestServiceWarningReasonFiresOnAnyFailingStaleBinaryCheck(t *testing.T) {
	for _, tc := range []struct {
		name              string
		firstOK, secondOK bool
		want              string
	}{
		{name: "second fails", firstOK: true, secondOK: false, want: warningReasonStaleBinary},
		{name: "first fails", firstOK: false, secondOK: true, want: warningReasonStaleBinary},
		{name: "both pass", firstOK: true, secondOK: true, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			names := []string{"stale-binary", "zz-extra-stale"}
			types := map[string]string{names[0]: checks.CheckTypeStaleBinary, names[1]: checks.CheckTypeStaleBinary}
			snaps := NewSnapshots()
			snaps.PublishWithCheckTypes("web", map[string]checks.Result{
				names[0]: {Check: names[0], OK: tc.firstOK},
				names[1]: {Check: names[1], OK: tc.secondOK},
			}, map[string]bool{names[0]: true, names[1]: true}, types)

			b := webBackendWithEntry(snaps, names, types)
			b.entries["web"].interval = time.Minute
			publishedAt := time.Now()
			b.now = func() time.Time { return publishedAt }

			if got := b.serviceWarningReason("web", b.entries["web"]); got != tc.want {
				t.Fatalf("warning reason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestServiceStrayCountZeroWithoutAStraysCheck(t *testing.T) {
	snaps := NewSnapshots()
	b := webBackendWithEntry(snaps, []string{"service"}, map[string]string{"service": checks.CheckTypeService})
	b.now = time.Now

	if got := b.serviceStrayCount("web", b.entries["web"]); got != 0 {
		t.Fatalf("stray count = %d, want 0 when no strays check is declared", got)
	}
}

// Every rejection path must name the service and emit an error event, the same
// contract the session closes follow.
func TestReapStraysRejectsUnknownAndDisabledServices(t *testing.T) {
	for _, tc := range []struct {
		name    string
		service string
		setup   func(*WebBackend)
		want    string
	}{
		{name: "unknown service", service: "ghost", want: "unknown service"},
		{
			name:    "disabled service",
			service: "web",
			setup:   func(b *WebBackend) { b.entries["web"].disabled = true },
			want:    "is disabled in configuration",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []Event
			b := straysSnapshotBackend(t, 1, 0)
			b.emit = func(e Event) { events = append(events, e) }
			if tc.setup != nil {
				tc.setup(b)
			}

			res := b.ReapStrays(context.Background(), tc.service)
			if res.OK {
				t.Fatalf("want a refusal, got %+v", res)
			}
			if !strings.Contains(res.Message, tc.want) {
				t.Fatalf("message = %q, want it to contain %q", res.Message, tc.want)
			}
			if len(events) != 1 || events[0].Kind != eventKindError || events[0].Action != reapActionLabel {
				t.Fatalf("events = %+v, want one reap error event", events)
			}
		})
	}
}
