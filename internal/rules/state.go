package rules

import (
	"strconv"
	"time"
)

// Policy is the resolved remediation policy that gates how often AUTOMATIC
// actions may run for a service (section 16). Backoff is post-MVP.
type Policy struct {
	Cooldown         time.Duration
	MaxActions       int // 0 means unlimited
	MaxActionsWindow time.Duration
}

// RemediationState is the per-service remediation history (section 16).
type RemediationState struct {
	LastActionAt  time.Time
	RecentActions []time.Time
}

// Allow decides whether an automatic action may run now, returning a reason when
// suppressed (section 16):
//
//  1. within cooldown of the last action -> suppress;
//  2. else max_actions reached inside the window -> suppress;
//  3. else allow.
func (p Policy) Allow(state *RemediationState, now time.Time) (bool, string) {
	if p.Cooldown > 0 && !state.LastActionAt.IsZero() && now.Sub(state.LastActionAt) < p.Cooldown {
		return false, "cooldown"
	}
	if p.MaxActions > 0 {
		if state.countWithin(now, p.MaxActionsWindow) >= p.MaxActions {
			return false, "rate limit"
		}
	}
	return true, ""
}

// Record updates the state after an action runs, trimming entries outside the
// rate-limit window (section 16).
func (s *RemediationState) Record(now time.Time, window time.Duration) {
	s.LastActionAt = now
	s.RecentActions = append(s.RecentActions, now)
	if window > 0 {
		cutoff := now.Add(-window)
		kept := s.RecentActions[:0]
		for _, t := range s.RecentActions {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		s.RecentActions = kept
	}
}

func (s *RemediationState) countWithin(now time.Time, window time.Duration) int {
	if window <= 0 {
		return len(s.RecentActions)
	}
	cutoff := now.Add(-window)
	n := 0
	for _, t := range s.RecentActions {
		if t.After(cutoff) {
			n++
		}
	}
	return n
}

// ParsePolicy reads the resolved `policy` section into a Policy.
func ParsePolicy(tree map[string]any) Policy {
	p := Policy{}
	section, ok := tree["policy"].(map[string]any)
	if !ok {
		return p
	}
	p.Cooldown = parseDuration(section["cooldown"])
	p.MaxActionsWindow = parseDuration(section["max_actions_window"])
	if n, ok := parseInt(section["max_actions"]); ok {
		p.MaxActions = n
	}
	return p
}

func parseDuration(v any) time.Duration {
	s, ok := v.(string)
	if !ok {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
}

// scalarString renders a YAML scalar as a string. A metric `value` is logically
// a string (section 14) but `0` decodes as an int, so it must be stringified.
func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case uint64:
		return strconv.FormatUint(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

func parseInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case uint64:
		return int(t), true
	case float64:
		return int(t), true
	case string:
		n, err := strconv.Atoi(t)
		return n, err == nil
	default:
		return 0, false
	}
}
