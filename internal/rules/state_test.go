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
	st.Record(t0, time.Hour)
	st.Record(t0.Add(time.Minute), time.Hour)

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
	st.Record(t0, time.Hour)
	st.Record(t0.Add(2*time.Hour), time.Hour) // first entry now outside the window
	if len(st.RecentActions) != 1 {
		t.Fatalf("RecentActions = %d, want 1 (old entry trimmed)", len(st.RecentActions))
	}
	if !st.LastActionAt.Equal(t0.Add(2 * time.Hour)) {
		t.Errorf("LastActionAt = %v", st.LastActionAt)
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
