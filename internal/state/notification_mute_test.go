package state

import (
	"testing"
	"time"
)

func TestStoreNotificationMuteRoundTrip(t *testing.T) {
	s := openTemp(t)
	at := time.Date(2026, 6, 7, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return at }
	until := at.Add(time.Hour)

	if err := s.SetNotificationMute("web", until, SourceWeb); err != nil {
		t.Fatalf("SetNotificationMute: %v", err)
	}
	rec, found, err := s.NotificationMute("web")
	if err != nil || !found {
		t.Fatalf("NotificationMute: found=%v err=%v", found, err)
	}
	if !rec.Until.Equal(until) || rec.Source != SourceWeb || !rec.UpdatedAt.Equal(at) {
		t.Fatalf("record = %+v", rec)
	}
	if !rec.MutedAt(at) || rec.MutedAt(until) {
		t.Fatalf("MutedAt must hold before until and end at it: %+v", rec)
	}

	// A second mute replaces the first instead of keeping the longer one.
	shorter := at.Add(15 * time.Minute)
	if err := s.SetNotificationMute("web", shorter, SourceCLI); err != nil {
		t.Fatalf("SetNotificationMute: %v", err)
	}
	all, err := s.NotificationMutes()
	if err != nil {
		t.Fatalf("NotificationMutes: %v", err)
	}
	if len(all) != 1 || !all["web"].Until.Equal(shorter) || all["web"].Source != SourceCLI {
		t.Fatalf("NotificationMutes = %+v", all)
	}

	removed, err := s.ClearNotificationMute("web")
	if err != nil || !removed {
		t.Fatalf("ClearNotificationMute: removed=%v err=%v", removed, err)
	}
	if _, found, _ := s.NotificationMute("web"); found {
		t.Fatal("an unmuted key must report found=false")
	}
	if removed, _ := s.ClearNotificationMute("web"); removed {
		t.Fatal("clearing a key with no row must report removed=false")
	}
}

// A row whose until cannot be parsed must not mute: a damaged row hiding a
// service's notifications forever is the failure the mute's expiry exists to
// prevent.
func TestStoreNotificationMuteDamagedUntilMutesNothing(t *testing.T) {
	s := openTemp(t)
	if _, err := s.db.Exec(`INSERT INTO notification_mute (key, until, source, updated_at) VALUES ('web', 'never', 'cli', '2026-06-07T09:00:00Z');`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	rec, found, err := s.NotificationMute("web")
	if err != nil || !found {
		t.Fatalf("NotificationMute: found=%v err=%v", found, err)
	}
	if rec.MutedAt(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("a damaged until muted: %+v", rec)
	}
}
