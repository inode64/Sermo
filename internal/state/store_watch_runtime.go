package state

import (
	"encoding/json"
	"fmt"
	"time"
)

// WatchRuntimeRecord is the durable control state for one watch result slot.
type WatchRuntimeRecord struct {
	Firing       bool
	Unavailable  bool
	LastNotifyAt time.Time
	// Severity is the open episode's high-water mark ("" outside an episode);
	// it owns the severity column the way Firing owns firing.
	Severity string
	// NotifiedSeverity is the gravest level the open episode notified at.
	NotifiedSeverity string
	// TransitionSeverity is the gravest failing RAID/LVM transition a
	// notify_on watch delivered since its last healthy one.
	TransitionSeverity string
	Window             WindowRecord
	Policy             RemediationRecord
	// Offenders are the processes a set-evaluating process watch already
	// announced, so a daemon restart announces only new ones.
	Offenders ProcessOffenders
}

// ProcessOffenders is the announced process set of one watch slot. A PID's
// start ticks count from boot, so the set is only valid within Boot.
type ProcessOffenders struct {
	Boot      string               `json:"boot"`
	Processes []ProcessIncarnation `json:"processes"`
}

// ProcessIncarnation is one announced process and when it was last notified.
type ProcessIncarnation struct {
	PID        int    `json:"pid"`
	StartTicks uint64 `json:"start_ticks"`
	LastNotify int64  `json:"last_notify,omitempty"` // Unix nanoseconds; 0: never
}

func encodeProcessOffenders(o ProcessOffenders) (string, error) {
	if len(o.Processes) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return "", fmt.Errorf("encode process offenders: %w", err)
	}
	return string(raw), nil
}

func decodeProcessOffenders(raw string) (ProcessOffenders, error) {
	var o ProcessOffenders
	if raw == "" {
		return o, nil
	}
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		return ProcessOffenders{}, fmt.Errorf("decode process offenders: %w", err)
	}
	return o, nil
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
		level              string
		rawRungs           string
		notified           string
		transition         string
		rawOffenders       string
	)
	found, err := scanOne(func() error {
		return s.reader.QueryRowContext(s.sqlCtx(),
			`SELECT firing, unavailable, last_notify_at, consecutive, history, true_since,
		        timed_history, last_action_at, recent_actions, current_backoff_ns,
		        clear_since, clear_consecutive, severity, severity_windows, notified_severity,
		        transition_severity, offenders
		   FROM watch_runtime_state WHERE watch = ? AND slot = ?;`,
			watch, slot,
		).Scan(
			&firing, &unavailable, &lastNotifyAt, &consecutive, &rawHistory, &trueSince,
			&rawTimed, &lastActionAt, &rawRecentActions, &currentBackoffNano,
			&clearSince, &clearConsecutive, &level, &rawRungs, &notified,
			&transition, &rawOffenders,
		)
	})
	if err != nil {
		return WatchRuntimeRecord{}, false, fmt.Errorf("load watch runtime state for %s/%s: %w", watch, slot, err)
	}
	if !found {
		return WatchRuntimeRecord{}, false, nil
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
	rungs, err := decodeSeverityWindows(rawRungs)
	if err != nil {
		return WatchRuntimeRecord{}, false, err
	}
	offenders, err := decodeProcessOffenders(rawOffenders)
	if err != nil {
		return WatchRuntimeRecord{}, false, fmt.Errorf("load watch runtime state for %s/%s: %w", watch, slot, err)
	}
	return WatchRuntimeRecord{
		Firing:             firing != 0,
		Unavailable:        unavailable != 0,
		LastNotifyAt:       unixNanoTime(lastNotifyAt),
		Severity:           level,
		NotifiedSeverity:   notified,
		TransitionSeverity: transition,
		Window: WindowRecord{
			Rungs:            rungs,
			Consecutive:      consecutive,
			History:          history,
			TrueSince:        unixNanoTime(trueSince),
			TimedHistory:     timed,
			ClearSince:       unixNanoTime(clearSince),
			ClearConsecutive: clearConsecutive,
		},
		Policy: RemediationRecord{
			LastActionAt:   unixNanoTime(lastActionAt),
			RecentActions:  recent,
			CurrentBackoff: time.Duration(currentBackoffNano),
		},
		Offenders: offenders,
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
	rungs, err := encodeSeverityWindows(rec.Window.Rungs)
	if err != nil {
		return err
	}
	offenders, err := encodeProcessOffenders(rec.Offenders)
	if err != nil {
		return err
	}
	firing := boolInt(rec.Firing)
	_, err = s.exec(s.sqlCtx(),
		`INSERT INTO watch_runtime_state (
		   watch, slot, firing, unavailable, last_notify_at, consecutive, history, true_since,
		   timed_history, last_action_at, recent_actions, current_backoff_ns,
		   clear_since, clear_consecutive, severity, severity_windows, notified_severity,
		   transition_severity, offenders
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
		   clear_consecutive   = excluded.clear_consecutive,
		   severity            = excluded.severity,
		   severity_windows    = excluded.severity_windows,
		   notified_severity   = excluded.notified_severity,
		   transition_severity = excluded.transition_severity,
		   offenders           = excluded.offenders;`,
		watch, slot, firing, boolInt(rec.Unavailable), timeUnixNano(rec.LastNotifyAt), rec.Window.Consecutive,
		string(history), timeUnixNano(rec.Window.TrueSince), timed,
		timeUnixNano(rec.Policy.LastActionAt), recent, int64(rec.Policy.CurrentBackoff),
		timeUnixNano(rec.Window.ClearSince), rec.Window.ClearConsecutive, rec.Severity, rungs, rec.NotifiedSeverity,
		rec.TransitionSeverity, offenders,
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
		rec.Window.ClearSince.IsZero() && rec.Window.ClearConsecutive == 0 &&
		rec.Policy.LastActionAt.IsZero() && len(rec.Policy.RecentActions) == 0 &&
		rec.Policy.CurrentBackoff == 0 && rec.Severity == "" && rec.NotifiedSeverity == "" &&
		rec.TransitionSeverity == "" && severityWindowsIdle(rec.Window.Rungs) &&
		len(rec.Offenders.Processes) == 0
}
