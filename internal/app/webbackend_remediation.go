package app

import (
	"time"

	"sermo/internal/rules"
	"sermo/internal/web"
)

func ruleWindowToWeb(rep rules.RuleWindowReport) web.RuleWindow {
	return web.RuleWindow{
		Name:          rep.Name,
		Type:          rep.Type,
		Action:        rep.Action,
		Condition:     rep.Condition,
		ConditionTrue: rep.ConditionTrue,
		Window:        rep.Window,
		Progress:      rep.Progress,
		Firing:        rep.Firing,
	}
}

func remediationToWeb(rep rules.RemediationReport) web.Remediation {
	r := web.Remediation{
		Allowed:       rep.Allowed,
		Reason:        rep.Reason,
		MaxActions:    rep.MaxActions,
		RecentActions: rep.RecentActions,
	}
	if rep.Cooldown > 0 {
		r.Cooldown = rep.Cooldown.String()
	}
	if rep.EffectiveCooldown > 0 {
		r.EffectiveCooldown = rep.EffectiveCooldown.String()
	}
	if rep.CurrentBackoff > 0 {
		r.CurrentBackoff = rep.CurrentBackoff.String()
	}
	if !rep.LastActionAt.IsZero() {
		r.LastActionAt = rep.LastActionAt.UTC().Format(time.RFC3339)
	}
	if !rep.CooldownUntil.IsZero() {
		r.CooldownUntil = rep.CooldownUntil.UTC().Format(time.RFC3339)
	}
	if !rep.NextEligibleAt.IsZero() {
		r.NextEligibleAt = rep.NextEligibleAt.UTC().Format(time.RFC3339)
	}
	if rep.MaxActionsWindow > 0 {
		r.MaxActionsWindow = rep.MaxActionsWindow.String()
	}
	return r
}
