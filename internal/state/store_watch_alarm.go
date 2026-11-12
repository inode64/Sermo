package state

import (
	"encoding/json"
	"fmt"
	"time"
)

// WatchAlarmSlot is a confirmed alarm state, independent of measurement values.
type WatchAlarmSlot struct {
	Active      bool   `json:"active"`
	Unavailable bool   `json:"unavailable,omitempty"`
	Severity    string `json:"severity,omitempty"`
	Health      string `json:"health,omitempty"`
}

// WatchAlarmRecord keeps an acknowledgement tied to the exact observed state.
// The separate row prevents cycle-runtime writes from overwriting operator input.
type WatchAlarmRecord struct {
	ConfigID   string                    `json:"config_id"`
	Revision   string                    `json:"revision"`
	Epoch      string                    `json:"epoch,omitempty"`
	Slots      map[string]WatchAlarmSlot `json:"slots"`
	Silenced   bool                      `json:"silenced"`
	SilencedAt time.Time                 `json:"silenced_at,omitzero"`
	Source     string                    `json:"source,omitempty"`
}

// WatchAlarmState reads one watch's durable alarm acknowledgement.
func (s *Store) WatchAlarmState(watch string) (WatchAlarmRecord, error) {
	var raw string
	found, err := scanOne(func() error {
		return s.reader.QueryRowContext(s.sqlCtx(), `SELECT record FROM watch_alarm_state WHERE watch = ?`, watch).Scan(&raw)
	})
	if err != nil {
		return WatchAlarmRecord{}, fmt.Errorf("read watch alarm %s: %w", watch, err)
	}
	var rec WatchAlarmRecord
	if found {
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			return rec, fmt.Errorf("decode watch alarm %s: %w", watch, err)
		}
	}
	return rec, nil
}

// SetWatchAlarmState atomically replaces one watch's control record.
func (s *Store) SetWatchAlarmState(watch string, rec WatchAlarmRecord) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode watch alarm %s: %w", watch, err)
	}
	_, err = s.exec(s.sqlCtx(), `INSERT INTO watch_alarm_state (watch, record) VALUES (?, ?)
		ON CONFLICT(watch) DO UPDATE SET record = excluded.record`, watch, string(raw))
	if err != nil {
		return fmt.Errorf("save watch alarm %s: %w", watch, err)
	}
	return nil
}
