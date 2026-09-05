package state

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// WatchRuntimeRecord is the durable control state for one watch result slot.
type WatchRuntimeRecord struct {
	Firing       bool
	Unavailable  bool
	LastNotifyAt time.Time
	Window       RuleWindowRecord
	Policy       RemediationRecord
}

// WatchRuntimeState returns one watch slot's persisted episode and pacing state.
func (s *Store) WatchRuntimeState(watch, slot string) (WatchRuntimeRecord, bool, error) {
	var (
		firing             int
		unavailable        int
		lastNotifyAt       int64
		consecutive        int
		rawHistory         string
		trueSince          int64
		rawTimed           string
		lastActionAt       int64
		rawRecentActions   string
		currentBackoffNano int64
		clearSince         int64
		clearConsecutive   int
	)
	err := s.reads().QueryRowContext(s.sqlCtx(),
		`SELECT firing, unavailable, last_notify_at, consecutive, history, true_since,
		        timed_history, last_action_at, recent_actions, current_backoff_ns,
		        clear_since, clear_consecutive
		   FROM watch_runtime_state WHERE watch = ? AND slot = ?;`,
		watch, slot,
	).Scan(
		&firing, &unavailable, &lastNotifyAt, &consecutive, &rawHistory, &trueSince,
		&rawTimed, &lastActionAt, &rawRecentActions, &currentBackoffNano,
		&clearSince, &clearConsecutive,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return WatchRuntimeRecord{}, false, nil
	case err != nil:
		return WatchRuntimeRecord{}, false, fmt.Errorf("load watch runtime state for %s/%s: %w", watch, slot, err)
	}

	var history []bool
	if err := json.Unmarshal([]byte(rawHistory), &history); err != nil {
		return WatchRuntimeRecord{}, false, fmt.Errorf("decode watch runtime history for %s/%s: %w", watch, slot, err)
	}
	timed, err := decodeRuleWindowSamples(rawTimed)
	if err != nil {
		return WatchRuntimeRecord{}, false, err
	}
	recent, err := decodeUnixNanos(rawRecentActions)
	if err != nil {
		return WatchRuntimeRecord{}, false, err
	}
	return WatchRuntimeRecord{
		Firing:       firing != 0,
		Unavailable:  unavailable != 0,
		LastNotifyAt: unixNanoTime(lastNotifyAt),
		Window: RuleWindowRecord{
			Consecutive:  consecutive,
			History:      history,
			TrueSince:    unixNanoTime(trueSince),
			TimedHistory: timed,
			// The watch's episode column doubles as the window's: both track the
			// same firing episode, kept in sync by Watch.evaluateFiring.
			Firing:           firing != 0,
			ClearSince:       unixNanoTime(clearSince),
			ClearConsecutive: clearConsecutive,
		},
		Policy: RemediationRecord{
			LastActionAt:   unixNanoTime(lastActionAt),
			RecentActions:  recent,
			CurrentBackoff: time.Duration(currentBackoffNano),
		},
	}, true, nil
}

// SetWatchRuntimeState upserts one watch slot's episode and pacing state. An
// empty record deletes any existing row.
func (s *Store) SetWatchRuntimeState(watch, slot string, rec WatchRuntimeRecord) error {
	if watchRuntimeRecordEmpty(rec) {
		_, err := s.exec(s.sqlCtx(), `DELETE FROM watch_runtime_state WHERE watch = ? AND slot = ?;`, watch, slot)
		if err != nil {
			return fmt.Errorf("clear watch runtime state for %s/%s: %w", watch, slot, err)
		}
		return nil
	}
	history, err := json.Marshal(rec.Window.History)
	if err != nil {
		return fmt.Errorf("encode watch runtime history for %s/%s: %w", watch, slot, err)
	}
	timed, err := encodeRuleWindowSamples(rec.Window.TimedHistory)
	if err != nil {
		return err
	}
	recent, err := encodeUnixNanos(rec.Policy.RecentActions)
	if err != nil {
		return err
	}
	firing := boolInt(rec.Firing)
	_, err = s.exec(s.sqlCtx(),
		`INSERT INTO watch_runtime_state (
		   watch, slot, firing, unavailable, last_notify_at, consecutive, history, true_since,
		   timed_history, last_action_at, recent_actions, current_backoff_ns,
		   clear_since, clear_consecutive
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(watch, slot) DO UPDATE SET
		   firing             = excluded.firing,
		   unavailable        = excluded.unavailable,
		   last_notify_at      = excluded.last_notify_at,
		   consecutive         = excluded.consecutive,
		   history             = excluded.history,
		   true_since          = excluded.true_since,
		   timed_history       = excluded.timed_history,
		   last_action_at      = excluded.last_action_at,
		   recent_actions      = excluded.recent_actions,
		   current_backoff_ns  = excluded.current_backoff_ns,
		   clear_since         = excluded.clear_since,
		   clear_consecutive   = excluded.clear_consecutive;`,
		watch, slot, firing, boolInt(rec.Unavailable), timeUnixNano(rec.LastNotifyAt), rec.Window.Consecutive,
		string(history), timeUnixNano(rec.Window.TrueSince), timed,
		timeUnixNano(rec.Policy.LastActionAt), recent, int64(rec.Policy.CurrentBackoff),
		timeUnixNano(rec.Window.ClearSince), rec.Window.ClearConsecutive,
	)
	if err != nil {
		return fmt.Errorf("set watch runtime state for %s/%s: %w", watch, slot, err)
	}
	return nil
}

func watchRuntimeRecordEmpty(rec WatchRuntimeRecord) bool {
	return !rec.Firing && !rec.Unavailable && rec.LastNotifyAt.IsZero() &&
		rec.Window.Consecutive == 0 && len(rec.Window.History) == 0 &&
		rec.Window.TrueSince.IsZero() && len(rec.Window.TimedHistory) == 0 &&
		!rec.Window.Firing && rec.Window.ClearSince.IsZero() && rec.Window.ClearConsecutive == 0 &&
		rec.Policy.LastActionAt.IsZero() && len(rec.Policy.RecentActions) == 0 &&
		rec.Policy.CurrentBackoff == 0
}
