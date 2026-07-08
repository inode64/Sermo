package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/rules"
	"sermo/internal/servicemgr"
)

func TestWebBackendDetailRemediation(t *testing.T) {
	reg := NewRemediationRegistry()
	t0 := time.Date(2026, 6, 7, 14, 0, 0, 0, time.UTC)
	policy := rules.Policy{Cooldown: 5 * time.Minute}
	state := &rules.RemediationState{LastActionAt: t0}
	reg.Publish("web", policy, state, t0.Add(2*time.Minute))

	b := &WebBackend{
		order:       []string{"web"},
		entries:     map[string]*webEntry{"web": {}},
		remediation: reg,
	}

	detail, ok := b.Detail(context.Background(), "web")
	if !ok {
		t.Fatal("detail not found")
	}
	if detail.Remediation == nil {
		t.Fatal("remediation missing from detail")
	}
	if detail.Remediation.Allowed || detail.Remediation.Reason != "cooldown" {
		t.Fatalf("remediation = %+v, want cooldown suppression", detail.Remediation)
	}
	if detail.Remediation.CooldownUntil == "" {
		t.Fatal("expected cooldown_until in detail")
	}
	wantUntil := t0.Add(5 * time.Minute).UTC().Format(time.RFC3339)
	if detail.Remediation.CooldownUntil != wantUntil {
		t.Fatalf("CooldownUntil = %q, want %q", detail.Remediation.CooldownUntil, wantUntil)
	}
}

func TestWebBackendServicesExposeRemediationAndLastEventSummary(t *testing.T) {
	reg := NewRemediationRegistry()
	events := NewEventLog(10)
	t0 := time.Date(2026, 6, 7, 14, 0, 0, 0, time.UTC)

	policy := rules.Policy{Cooldown: 5 * time.Minute}
	state := &rules.RemediationState{LastActionAt: t0}
	reg.Publish("web", policy, state, t0.Add(time.Minute))

	events.now = func() time.Time { return t0.Add(2 * time.Minute) }
	events.Add(Event{Service: "web", Kind: eventKindAction, Action: string(rules.ActionRestart), Status: eventStatusOK, Message: "restart completed"})

	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {
				unit:           "nginx.service",
				backend:        string(servicemgr.BackendSystemd),
				policyCooldown: 5 * time.Minute,
			},
		},
		remediation: reg,
		events:      events,
	}

	services := b.Services(context.Background())
	if len(services) != 1 {
		t.Fatalf("Services length = %d, want 1", len(services))
	}
	svc := services[0]
	if svc.PolicyCooldown != "5m" || svc.RemediationState != "cooldown" {
		t.Fatalf("service remediation summary = cooldown %q state %q", svc.PolicyCooldown, svc.RemediationState)
	}
	wantNext := t0.Add(5 * time.Minute).Format(time.RFC3339)
	if svc.NextEligibleAt != wantNext {
		t.Fatalf("NextEligibleAt = %q, want %q", svc.NextEligibleAt, wantNext)
	}
	if svc.LastEvent == nil || svc.LastEvent.Action != string(rules.ActionRestart) || svc.LastEvent.Status != eventStatusOK {
		t.Fatalf("LastEvent = %+v, want restart/ok", svc.LastEvent)
	}
}

func TestWorkerPublishesRemediationWhenPaused(t *testing.T) {
	reg := NewRemediationRegistry()
	t0 := time.Date(2026, 6, 7, 14, 0, 0, 0, time.UTC)
	w := &Worker{
		Service:     "web",
		Policy:      rules.Policy{Cooldown: time.Minute},
		State:       &rules.RemediationState{LastActionAt: t0},
		Remediation: reg,
		IsPaused:    func() bool { return true },
		Now:         func() time.Time { return t0.Add(30 * time.Second) },
	}
	w.RunCycle(context.Background())

	rep, ok := reg.Get("web")
	if !ok {
		t.Fatal("remediation not published for paused worker")
	}
	if rep.Allowed || rep.Reason != "cooldown" {
		t.Fatalf("rep = %+v, want cooldown", rep)
	}
}
