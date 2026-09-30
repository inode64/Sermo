package app

import (
	"context"

	"sermo/internal/config"
	"sermo/internal/notify"
	"sermo/internal/severity"
)

// notifySubjectTag opens every notification subject Sermo generates.
const notifySubjectTag = "[sermo]"

// subjectPrefix is the tag a notification subject starts with. An error keeps
// the unmarked "[sermo]" form it always had; every other grade names itself
// ("[sermo][warning]", "[sermo][critical]"), because the subject is the one
// string an operator reads in mail or chat before deciding whether to get up.
func subjectPrefix(level severity.Level) string {
	resolved := level.Resolved()
	if resolved == severity.Error {
		return notifySubjectTag
	}
	return notifySubjectTag + "[" + resolved.String() + "]"
}

// recoveredMessagePrefix opens the message of a recovery notification.
const recoveredMessagePrefix = "recovered: "

// deliverNotification sends msg to every notifier that allow admits (nil
// admits all) and whose min_severity the message meets, reporting each
// delivery. It is the one monitoring delivery loop: operator actions (a
// notifier test, an inventory report) call Send directly and are never
// filtered.
func deliverNotification(ctx context.Context, notifiers []notify.Notifier, msg notify.Message, allow func(notify.Notifier) bool, report func(notify.Notifier, error)) {
	for _, n := range notifiers {
		if (allow != nil && !allow(n)) || !notify.Accepts(n, msg.Severity) {
			continue
		}
		report(n, n.Send(ctx, msg))
	}
}

// deliveryReport is the report deliverNotification calls: one notify or
// notify-failed event per notifier, on the subject that sent the message (a
// watch, a rule).
func deliveryReport(emit func(Event), subject Event) func(notify.Notifier, error) {
	return func(n notify.Notifier, err error) {
		e := subject
		if err != nil {
			e.Kind, e.Message = eventKindNotifyFail, n.Name()+": "+err.Error()
		} else {
			e.Kind, e.Message = eventKindNotify, "notified "+n.Name()
		}
		emit(e)
	}
}

// dryRunFilter is the delivery filter of a site in dry-run mode: a simulated
// notification reaches only the console (wall) notifiers. Nil admits all.
func dryRunFilter(dryRun bool) func(notify.Notifier) bool {
	if dryRun {
		return dryRunConsoleNotifier
	}
	return nil
}

// BuildNotifiers constructs the configured notifiers the daemon delivers
// through: with the configured templates, and linking every message to its
// row on the dashboard when web.public_url is set.
func BuildNotifiers(cfg *config.Config) (map[string]notify.Notifier, []string) {
	return notify.Build(cfg.Notifiers(),
		notify.WithTemplateDir(cfg.Global.TemplateDir()),
		notify.WithPanelURL(cfg.Global.WebPublicURL()))
}
