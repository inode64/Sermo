package assist

import (
	"strings"

	"sermo/internal/config"
)

// chooseNotifiers asks which configured notifiers to alert. The numbered list
// shows only the notifiers defined in the config; the reserved answers ride in
// the question instead of occupying rows. They are ALWAYS selectable, even when
// the config defines no notifiers and no global notify default exists:
//
//   - a notifier selection writes those names;
//   - "all" selects every configured notifier;
//   - "none" writes the reserved sentinel so the entry is monitor-only
//     (suppresses any inherited default);
//   - "default" inherits the global notify default when one is configured
//     (returns nil), and otherwise — there is nothing to inherit — falls back to
//     monitor-only with a one-line note, instead of erroring or re-asking.
//
// This is the general rule (see docs/wizards.md): the wizard never blocks on the
// notifier question; an unresolved "default" simply degrades to monitor-only.
func chooseNotifiers(p *Prompt, env Env) []string {
	question := "Notify which targets? ('default' inherits global notify; not configured)"
	if len(env.DefaultNotify) > 0 {
		question = "Notify which targets? ('default' inherits global notify: " + strings.Join(env.DefaultNotify, ", ") + ")"
	}
	idx, kw := p.MultiChooseKeyword(question, env.Notifiers, config.NotifyNone, config.NotifyKeywordDefault)
	switch kw {
	case config.NotifyNone:
		return []string{config.NotifyNone}
	case config.NotifyKeywordDefault:
		if len(env.DefaultNotify) > 0 {
			return nil // inherit the configured global default
		}
		p.printf("  no global notify default is configured — this will be monitor-only (no notification).\n")
		return []string{config.NotifyNone}
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
