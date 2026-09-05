package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// RemediationRecord is the persisted automatic-remediation control state for one
// service.
type RemediationRecord struct {
	LastActionAt   time.Time
	RecentActions  []time.Time
	CurrentBackoff time.Duration
}

// RuleWindowRecord is the persisted for/within progress for one rule, plus its
// firing episode and clear-window progress.
type RuleWindowRecord struct {
	Consecutive      int
	History          []bool
	TrueSince        time.Time
	TimedHistory     []RuleWindowSample
	Firing           bool
	ClearConsecutive int
	ClearSince       time.Time
}

// RuleWindowSample is one persisted sample for a duration-based within window.
type RuleWindowSample struct {
	At    time.Time
	Match bool
}

// RemediationState returns a service's persisted automatic-remediation state.
// found is false when no action state has been recorded yet.
func (s *Store) RemediationState(service string) (RemediationRecord, bool, error) {
	var (
		lastActionAt     int64
		recentActions    string
		currentBackoffNS int64
	)
	err := s.reads().QueryRowContext(s.sqlCtx(),
		`SELECT last_action_at, recent_actions, current_backoff_ns
		   FROM remediation_state WHERE service = ?;`,
		service,
	).Scan(&lastActionAt, &recentActions, &currentBackoffNS)
	switch {
	case err == sql.ErrNoRows:
		return RemediationRecord{}, false, nil
	case err != nil:
		return RemediationRecord{}, false, fmt.Errorf("load remediation state for %s: %w", service, err)
	default:
		recent, err := decodeUnixNanos(recentActions)
		if err != nil {
			return RemediationRecord{}, false, err
		}
		return RemediationRecord{
			LastActionAt:   unixNanoTime(lastActionAt),
			RecentActions:  recent,
			CurrentBackoff: time.Duration(currentBackoffNS),
		}, true, nil
	}
}

// SetRemediationState upserts a service's automatic-remediation state. An empty
// record deletes any existing row.
func (s *Store) SetRemediationState(service string, rec RemediationRecord) error {
	if rec.LastActionAt.IsZero() && len(rec.RecentActions) == 0 && rec.CurrentBackoff == 0 {
		_, err := s.exec(s.sqlCtx(), `DELETE FROM remediation_state WHERE service = ?;`, service)
		if err != nil {
			return fmt.Errorf("clear remediation state for %s: %w", service, err)
		}
		return nil
	}
	recent, err := encodeUnixNanos(rec.RecentActions)
	if err != nil {
		return err
	}
	_, err = s.exec(s.sqlCtx(),
		`INSERT INTO remediation_state (service, last_action_at, recent_actions, current_backoff_ns)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(service) DO UPDATE SET
		   last_action_at     = excluded.last_action_at,
		   recent_actions     = excluded.recent_actions,
		   current_backoff_ns = excluded.current_backoff_ns;`,
		service, timeUnixNano(rec.LastActionAt), recent, int64(rec.CurrentBackoff),
	)
	if err != nil {
		return fmt.Errorf("set remediation state for %s: %w", service, err)
	}
	return nil
}

// RuleWindowStates returns the persisted for/within progress for a service's
// rules, keyed by rule name.
func (s *Store) RuleWindowStates(service string) (map[string]RuleWindowRecord, error) {
	rows, err := s.reads().QueryContext(s.sqlCtx(),
		`SELECT rule_name, consecutive, history, true_since, timed_history,
		        firing, clear_since, clear_consecutive
		   FROM rule_window_state WHERE service = ? ORDER BY rule_name;`,
		service,
	)
	if err != nil {
		return nil, fmt.Errorf("load rule window states for %s: %w", service, err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]RuleWindowRecord{}
	for rows.Next() {
		var (
			name             string
			consecutive      int
			rawHistory       string
			trueSince        int64
			rawTimed         string
			firing           int
			clearSince       int64
			clearConsecutive int
		)
		if err := rows.Scan(&name, &consecutive, &rawHistory, &trueSince, &rawTimed, &firing, &clearSince, &clearConsecutive); err != nil {
			return nil, fmt.Errorf("scan rule window state for %s: %w", service, err)
		}
		var history []bool
		if err := json.Unmarshal([]byte(rawHistory), &history); err != nil {
			return nil, fmt.Errorf("decode rule window history for %s/%s: %w", service, name, err)
		}
		timed, err := decodeRuleWindowSamples(rawTimed)
		if err != nil {
			return nil, err
		}
		out[name] = RuleWindowRecord{
			Consecutive:      consecutive,
			History:          history,
			TrueSince:        unixNanoTime(trueSince),
			TimedHistory:     timed,
			Firing:           firing != 0,
			ClearSince:       unixNanoTime(clearSince),
			ClearConsecutive: clearConsecutive,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rule window states for %s: %w", service, err)
	}
	return out, nil
}

// SetRuleWindowStates replaces the persisted rule-window state for a service.
// Passing an empty map removes stale rows for rules that no longer exist.
func (s *Store) SetRuleWindowStates(service string, records map[string]RuleWindowRecord) error {
	return replaceServiceRows(s, service, `DELETE FROM rule_window_state WHERE service = ?;`,
		"rule window state", records, func(tx *sql.Tx, name string, rec RuleWindowRecord) error {
			history, err := json.Marshal(rec.History)
			if err != nil {
				return fmt.Errorf("encode rule window history for %s/%s: %w", service, name, err)
			}
			timed, err := encodeRuleWindowSamples(rec.TimedHistory)
			if err != nil {
				return err
			}
			firing := boolInt(rec.Firing)
			if _, err := tx.ExecContext(s.sqlCtx(),
				`INSERT INTO rule_window_state (service, rule_name, consecutive, history, true_since, timed_history,
				                                firing, clear_since, clear_consecutive)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);`,
				service, name, rec.Consecutive, string(history), timeUnixNano(rec.TrueSince), timed,
				firing, timeUnixNano(rec.ClearSince), rec.ClearConsecutive,
			); err != nil {
				return fmt.Errorf("insert rule window state for %s/%s: %w", service, name, err)
			}
			return nil
		})
}

// The two JSON-encoded history columns (rule window timestamps and rule window
// samples) share one round-trip shape: marshal the rows that carry a timestamp,
// and on the way back drop any row whose timestamp did not survive. encodeRows
// and decodeRows own that shape so each column only describes its own row.

// column* name the JSON-encoded history columns in encode/decode errors.
const (
	columnUnixNanos         = "unix nanos"
	columnRuleWindowSamples = "rule window samples"
)

type ruleWindowSampleJSON struct {
	At    int64 `json:"at"`
	Match bool  `json:"match"`
}

func encodeRuleWindowSamples(samples []RuleWindowSample) (string, error) {
	return encodeRows(columnRuleWindowSamples, samples, func(s RuleWindowSample) (ruleWindowSampleJSON, bool) {
		return ruleWindowSampleJSON{At: s.At.UTC().UnixNano(), Match: s.Match}, !s.At.IsZero()
	})
}

func decodeRuleWindowSamples(raw string) ([]RuleWindowSample, error) {
	return decodeRows(columnRuleWindowSamples, raw, func(s ruleWindowSampleJSON) (RuleWindowSample, bool) {
		return RuleWindowSample{At: time.Unix(0, s.At).UTC(), Match: s.Match}, s.At != 0
	})
}
