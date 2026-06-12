package assist

import (
	"strings"

	"sermo/internal/config"
)

// chooseNotifiers asks which configured notifiers to alert. The numbered list
// shows only the notifiers defined in the config; the reserved answers ride in
// the question instead of occupying rows: "all" selects every configured
// notifier, "none" writes the reserved sentinel so the generated watch
// suppresses any inherited default, and "default" leaves notify unset (returns
// nil) so the runtime inherits the global notify default. The keywords are
// accepted even when the config defines no notifiers, so an expand-only or
// opt-out watch still has a valid answer.
func chooseNotifiers(p *Prompt, env Env) []string {
	question := "Notify which targets? ('default' inherits global notify; not configured)"
	if len(env.DefaultNotify) > 0 {
		question = "Notify which targets? ('default' inherits global notify: " + strings.Join(env.DefaultNotify, ", ") + ")"
	}
	idx, kw := p.MultiChooseKeyword(question, env.Notifiers, "none", "default")
	switch kw {
	case "none":
		return []string{config.NotifyNone}
	case "default":
		return nil
	}
	out := make([]string, 0, len(idx))
	for _, i := range idx {
		out = append(out, env.Notifiers[i])
	}
	if len(out) == 0 {
		return []string{config.NotifyNone}
	}
	return out
}

// ensureNotifyAction guarantees the notifier answer is never silently inert,
// re-asking with an explanation instead of aborting the wizard after the
// fact. The reserved 'none' is always accepted — it is a deliberate
// monitor-only choice, valid with or without a global notify default. Only
// 'default' with no global notify configured re-asks (it would generate a
// watch the daemon rejects as having no action), unless hasOtherAction
// reports the watch carries one (e.g. auto-expand). Every assistant that asks
// for notifiers must route its requirement through this helper so the
// validation cannot drift per wizard. On EOF the re-prompt aborts with
// ErrInputClosed like every other Prompt loop.
func ensureNotifyAction(p *Prompt, env Env, current []string, hasOtherAction bool) []string {
	for !hasOtherAction && !config.HasEffectiveNotifyAction(current, env.DefaultNotify) && !config.NotifyOptedOut(current) {
		p.printf("  no global notify default is configured, so 'default' would leave this watch with no action — choose at least one notifier, or 'none' to monitor without notifying\n")
		p.abortIfClosed()
		current = chooseNotifiers(p, env)
	}
	return current
}
