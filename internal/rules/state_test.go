package rules

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)

func TestPolicyCooldown(t *testing.T) {
	p := Policy{Cooldown: time.Minute}
	st := &RemediationState{LastActionAt: t0}

	if ok, reason := p.Allow(st, t0.Add(30*time.Second)); ok || reason != "cooldown" {
		t.Fatalf("within cooldown: ok=%v reason=%q, want suppressed/cooldown", ok, reason)
	}
	if ok, _ := p.Allow(st, t0.Add(2*time.Minute)); !ok {
		t.Fatal("after cooldown: should allow")
	}
}

func TestPolicyFirstActionAllowed(t *testing.T) {
	p := Policy{Cooldown: time.Minute}
	if ok, _ := p.Allow(&RemediationState{}, t0); !ok {
		t.Fatal("first action (no history) should be allowed")
	}
}

func TestPolicyMaxActions(t *testing.T) {
	p := Policy{MaxActions: 2, MaxActionsWindow: time.Hour}
	st := &RemediationState{}
	st.Record(t0, Policy{MaxActionsWindow: time.Hour})
	st.Record(t0.Add(time.Minute), Policy{MaxActionsWindow: time.Hour})

	if ok, reason := p.Allow(st, t0.Add(2*time.Minute)); ok || reason != "rate limit" {
		t.Fatalf("at max_actions: ok=%v reason=%q, want suppressed/rate limit", ok, reason)
	}
	// Once the window slides past the old actions, allow again.
	if ok, _ := p.Allow(st, t0.Add(2*time.Hour)); !ok {
		t.Fatal("after window slides, should allow")
	}
}

func TestRecordTrimsWindow(t *testing.T) {
	st := &RemediationState{}
	st.Record(t0, Policy{MaxActionsWindow: time.Hour})
	st.Record(t0.Add(2*time.Hour), Policy{MaxActionsWindow: time.Hour}) // first entry now outside the window
	if len(st.RecentActions) != 1 {
		t.Fatalf("RecentActions = %d, want 1 (old entry trimmed)", len(st.RecentActions))
	}
	if !st.LastActionAt.Equal(t0.Add(2 * time.Hour)) {
		t.Errorf("LastActionAt = %v", st.LastActionAt)
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	p := Policy{Cooldown: time.Minute, Backoff: &Backoff{Initial: time.Minute, Factor: 2, Max: 5 * time.Minute}}
	st := &RemediationState{}

	st.Record(t0, p)
	if st.CurrentBackoff != time.Minute {
		t.Fatalf("first backoff = %v, want 1m", st.CurrentBackoff)
	}
	st.Record(t0, p)
	if st.CurrentBackoff != 2*time.Minute {
		t.Fatalf("second backoff = %v, want 2m", st.CurrentBackoff)
	}
	st.Record(t0, p) // 4m
	st.Record(t0, p) // 8m -> capped at 5m
	if st.CurrentBackoff != 5*time.Minute {
		t.Fatalf("backoff = %v, want capped at 5m", st.CurrentBackoff)
	}
}

func TestBackoffExtendsEffectiveCooldown(t *testing.T) {
	p := Policy{Cooldown: time.Minute, Backoff: &Backoff{Initial: 10 * time.Minute, Factor: 2}}
	st := &RemediationState{}
	st.Record(t0, p) // CurrentBackoff = 10m, LastActionAt = t0

	// 5 minutes later: past the 1m cooldown, but inside the 10m backoff.
	if ok, reason := p.Allow(st, t0.Add(5*time.Minute)); ok || reason != "cooldown" {
		t.Fatalf("within backoff: ok=%v reason=%q, want suppressed/cooldown", ok, reason)
	}
	// 11 minutes later: past the backoff.
	if ok, _ := p.Allow(st, t0.Add(11*time.Minute)); !ok {
		t.Fatal("after backoff window: should allow")
	}
}

func TestRecoverResetsBackoff(t *testing.T) {
	st := &RemediationState{CurrentBackoff: 8 * time.Minute}
	st.Recover()
	if st.CurrentBackoff != 0 {
		t.Fatalf("Recover should reset backoff, got %v", st.CurrentBackoff)
	}
}

func TestParseBackoff(t *testing.T) {
	tree := map[string]any{"policy": map[string]any{
		"cooldown": "5m",
		"backoff":  map[string]any{"initial": "1m", "factor": 3, "max": "30m"},
	}}
	p := ParsePolicy(tree)
	if p.Backoff == nil || p.Backoff.Initial != time.Minute || p.Backoff.Factor != 3 || p.Backoff.Max != 30*time.Minute {
		t.Fatalf("backoff = %+v", p.Backoff)
	}
}

func TestParsePolicy(t *testing.T) {
	tree := map[string]any{"policy": map[string]any{
		"cooldown":           "5m",
		"max_actions":        3,
		"max_actions_window": "1h",
	}}
	p := ParsePolicy(tree)
	if p.Cooldown != 5*time.Minute || p.MaxActions != 3 || p.MaxActionsWindow != time.Hour {
		t.Fatalf("policy = %+v", p)
	}
}
