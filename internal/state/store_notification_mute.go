package state

import (
	"fmt"
	"time"
)

// NotificationMuteRecord is one operator mute of a service's or watch's
// notifications. It mutes only while the clock reads before Until.
type NotificationMuteRecord struct {
	Until     time.Time
	Source    string
	UpdatedAt time.Time
}

// MutedAt reports whether the mute still holds at now.
func (r NotificationMuteRecord) MutedAt(now time.Time) bool {
	return now.Before(r.Until)
}

// SetNotificationMute mutes key's notifications until until, replacing any
// earlier mute of the same key. source notes who set it (SourceCLI, SourceWeb).
func (s *Store) SetNotificationMute(key string, until time.Time, source string) error {
	_, err := s.exec(s.sqlCtx(),
		`INSERT INTO notification_mute (key, until, source, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET
		   until      = excluded.until,
		   source     = excluded.source,
		   updated_at = excluded.updated_at;`,
		key, until.UTC().Format(time.RFC3339), source, s.now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("set notification mute for %s: %w", key, err)
	}
	return nil
}

// ClearNotificationMute removes key's mute, expired or not. It reports
// whether a row existed.
func (s *Store) ClearNotificationMute(key string) (bool, error) {
	res, err := s.exec(s.sqlCtx(), `DELETE FROM notification_mute WHERE key = ?;`, key)
	if err != nil {
		return false, fmt.Errorf("clear notification mute for %s: %w", key, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("clear notification mute for %s: %w", key, err)
	}
	return n > 0, nil
}

// NotificationMute returns key's mute row. found is false when the key was
// never muted or was unmuted; an expired row is still returned, and
// MutedAt says whether it holds.
func (s *Store) NotificationMute(key string) (rec NotificationMuteRecord, found bool, err error) {
	var until, updated string
	found, err = scanOne(func() error {
		return s.reader.QueryRowContext(s.sqlCtx(),
			`SELECT until, source, updated_at FROM notification_mute WHERE key = ?;`, key,
		).Scan(&until, &rec.Source, &updated)
	})
	if err != nil {
		return NotificationMuteRecord{}, false, fmt.Errorf("load notification mute for %s: %w", key, err)
	}
	if !found {
		return NotificationMuteRecord{}, false, nil
	}
	return parseNotificationMute(rec, until, updated), true, nil
}

// NotificationMutes reads every mute row once for a dashboard snapshot.
func (s *Store) NotificationMutes() (map[string]NotificationMuteRecord, error) {
	rows, err := s.reader.QueryContext(s.sqlCtx(), `SELECT key, until, source, updated_at FROM notification_mute;`)
	if err != nil {
		return nil, fmt.Errorf("load notification mutes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]NotificationMuteRecord{}
	for rows.Next() {
		var key, until, updated string
		var rec NotificationMuteRecord
		if err := rows.Scan(&key, &until, &rec.Source, &updated); err != nil {
			return nil, fmt.Errorf("scan notification mute: %w", err)
		}
		out[key] = parseNotificationMute(rec, until, updated)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read notification mutes: %w", err)
	}
	return out, nil
}

// parseNotificationMute fills a row's times. An unparsable until reads as the
// zero time, which mutes nothing: a damaged row must not hide notifications.
func parseNotificationMute(rec NotificationMuteRecord, until, updated string) NotificationMuteRecord {
	rec.Until, _ = time.Parse(time.RFC3339, until)
	rec.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return rec
}
