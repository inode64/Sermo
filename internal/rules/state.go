package rules

import (
	"sermo/internal/cfgval"
	"sort"
	"time"
)

// Backoff grows the effective cooldown after consecutive remediations (§16).
type Backoff struct {
	Initial time.Duration
	Factor  float64
	Max     time.Duration
}

// Policy is the resolved remediation policy that gates how often AUTOMATIC
// actions may run for a service (section 16).
type Policy struct {
	Cooldown         time.Duration
	MaxActions       int // 0 means unlimited
	MaxActionsWindow time.Duration
	Backoff          *Backoff // nil when disabled
}

// RemediationState is the per-service remediation history (section 16).
type RemediationState struct {
	LastActionAt   time.Time
	RecentActions  []time.Time
	CurrentBackoff time.Duration // 0 when backoff is disabled or has decayed
}

// RemediationReport is a read-only operator view of policy gating (section 16).
type RemediationReport struct {
	Allowed           bool
	Reason            string // cooldown | rate limit
	Cooldown          time.Duration
	EffectiveCooldown time.Duration
	CurrentBackoff    time.Duration
	LastActionAt      time.Time
	CooldownUntil     time.Time // zero when not in cooldown/backoff
	NextEligibleAt    time.Time // zero when currently allowed or unknown
	MaxActions        int
	MaxActionsWindow  time.Duration
	RecentActions     int
}

// Report returns whether an automatic remediation may run now and the timing
// fields that explain a suppression.
func (p Policy) Report(state *RemediationState, now time.Time) RemediationReport {
	if state == nil {
		state = &RemediationState{}
	}
	allowed, reason := p.Allow(state, now)
	effective := p.effectiveCooldown(state)
	var until time.Time
	if effective > 0 && !state.LastActionAt.IsZero() {
		until = state.LastActionAt.Add(effective)
		if !now.Before(until) {
			until = time.Time{}
		}
	}
	rateUntil := p.rateLimitUntil(state, now)
	next := maxTime(until, rateUntil)
	if allowed {
		next = time.Time{}
	}
	return RemediationReport{
		Allowed:           allowed,
		Reason:            reason,
		Cooldown:          p.Cooldown,
		EffectiveCooldown: effective,
		CurrentBackoff:    state.CurrentBackoff,
		LastActionAt:      state.LastActionAt,
		CooldownUntil:     until,
		NextEligibleAt:    next,
		MaxActions:        p.MaxActions,
		MaxActionsWindow:  p.MaxActionsWindow,
		RecentActions:     state.countWithin(now, p.MaxActionsWindow),
	}
}

// effectiveCooldown is the gate between actions: the configured cooldown, or the
// grown backoff when it is longer.
func (p Policy) effectiveCooldown(state *RemediationState) time.Duration {
	if state.CurrentBackoff > p.Cooldown {
		return state.CurrentBackoff
	}
	return p.Cooldown
}

func (p Policy) rateLimitUntil(state *RemediationState, now time.Time) time.Time {
	if p.MaxActions <= 0 || p.MaxActionsWindow <= 0 {
		return time.Time{}
	}
	cutoff := now.Add(-p.MaxActionsWindow)
	var relevant []time.Time
	for _, t := range state.RecentActions {
		if t.After(cutoff) {
			relevant = append(relevant, t)
		}
	}
	if len(relevant) < p.MaxActions {
		return time.Time{}
	}
	sort.Slice(relevant, func(i, j int) bool { return relevant[i].Before(relevant[j]) })
	until := relevant[len(relevant)-p.MaxActions].Add(p.MaxActionsWindow)
	if !now.Before(until) {
		return time.Time{}
	}
	return until
}

func maxTime(a, b time.Time) time.Time {
	switch {
	case a.IsZero():
		return b
	case b.IsZero():
		return a
	case a.After(b):
		return a
	default:
		return b
	}
}

// Allow decides whether an automatic action may run now, returning a reason when
// suppressed (section 16):
//
//  1. within cooldown of the last action -> suppress;
//  2. else max_actions reached inside the window -> suppress;
//  3. else allow.
func (p Policy) Allow(state *RemediationState, now time.Time) (bool, string) {
	effective := p.effectiveCooldown(state)
	if effective > 0 && !state.LastActionAt.IsZero() && now.Sub(state.LastActionAt) < effective {
		return false, "cooldown"
	}
	if p.MaxActions > 0 {
		if state.countWithin(now, p.MaxActionsWindow) >= p.MaxActions {
			return false, "rate limit"
		}
	}
	return true, ""
}

// Record updates the state after an executed automatic remediation: stamps the
// time, trims the rate-limit window, and grows the backoff if enabled
// (section 16). Blocked and preflight-failed operations must not call Record.
func (s *RemediationState) Record(now time.Time, p Policy) {
	s.LastActionAt = now
	s.RecentActions = append(s.RecentActions, now)
	if p.MaxActionsWindow > 0 {
		s.trimRecentActions(now.Add(-p.MaxActionsWindow))
	}
	if p.Backoff != nil {
		s.growBackoff(p.Backoff)
	}
}

func (s *RemediationState) trimRecentActions(cutoff time.Time) {
	kept := s.RecentActions[:0]
	for _, t := range s.RecentActions {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	s.RecentActions = kept
}

// growBackoff advances the effective cooldown: initial, then ×factor each
// consecutive remediation, capped at max (section 16).
func (s *RemediationState) growBackoff(b *Backoff) {
	if s.CurrentBackoff <= 0 {
		s.CurrentBackoff = b.Initial
	} else {
		factor := b.Factor
		if factor <= 0 {
			factor = 2
		}
		s.CurrentBackoff = time.Duration(float64(s.CurrentBackoff) * factor)
	}
	if b.Max > 0 && s.CurrentBackoff > b.Max {
		s.CurrentBackoff = b.Max
	}
}

// Recover resets the backoff after a healthy cycle with no firing rule
// (section 16).
func (s *RemediationState) Recover() {
	s.CurrentBackoff = 0
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
	p.Cooldown = cfgval.Duration(section["cooldown"])
	p.MaxActionsWindow = cfgval.Duration(section["max_actions_window"])
	if n, ok := cfgval.Int(section["max_actions"]); ok {
		p.MaxActions = n
	}
	if bo, ok := section["backoff"].(map[string]any); ok {
		b := &Backoff{
			Initial: cfgval.Duration(bo["initial"]),
			Factor:  floatOrZero(bo["factor"]),
			Max:     cfgval.Duration(bo["max"]),
		}
		if b.Factor <= 0 {
			b.Factor = 2
		}
		p.Backoff = b
	}
	return p
}

func floatOrZero(v any) float64 {
	f, _ := cfgval.Float(v)
	return f
}
