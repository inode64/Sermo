package app

import (
	"context"
	"slices"
	"time"

	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/severity"
)

// owedStartPending is appended to the event of the operation that owes a start.
const owedStartPending = "; start follows once these processes exit"

// owedStart is the start a service is owed after an automatic start or restart
// left it stopped beside residual processes. The engine never starts beside
// them (orphan_processes), and that attempt arms the per-service cooldown, which
// would hold the service down long after they exit.
type owedStart struct {
	// rule is looked up again when the start runs, so a configuration reload in
	// between applies its own dry_run.
	rule      string
	severity  severity.Level
	residuals []process.Process
}

// noteOwedStart records the start owed after result. Each residual needs a
// known generation, or its exit could never be proven.
func (w *Worker) noteOwedStart(firing firingRule, action string, result operation.Result) bool {
	if result.Status != operation.ResultOrphanProcesses || len(result.Processes) == 0 || !rules.ActionType(action).MayRelaunch() {
		return false
	}
	if slices.ContainsFunc(result.Processes, func(p process.Process) bool { return p.StartTicks == 0 }) {
		return false
	}
	w.owedStart = &owedStart{rule: firing.Name, severity: firing.severity, residuals: slices.Clone(result.Processes)}
	return true
}

// runOwedStart makes the owed start once every residual generation has exited,
// exempt from the cooldown but not from max_actions, and reports whether it ran
// an operation, which spends this cycle's remediation; one held back (dry run,
// panic, max_actions) leaves the cycle to the rules. It is a single attempt that owes nothing more:
// the engine still applies guards, locks, preflight and the reconciliation that
// refuses to start beside a live orphan, and a refused or failed start leaves
// the service to its rules and their cooldown.
func (w *Worker) runOwedStart(ctx context.Context, now func() time.Time) bool {
	owed := w.owedStart
	if owed == nil || w.settlingUnread || !w.residualsExited(owed.residuals) {
		return false
	}
	w.owedStart = nil
	i := slices.IndexFunc(w.Rules, func(r rules.Rule) bool { return r.Name == owed.rule })
	if i < 0 {
		return false // a reload removed the rule that owed it
	}
	// A reload that made the rule alert-only or gave it another operation took
	// the start out of its hands.
	if op, ok := w.Rules[i].Operation(); !ok || !op.Type.MayRelaunch() {
		return false
	}
	firing, action := firingRule{Rule: w.Rules[i], severity: owed.severity}, string(rules.ActionStart)
	event := Event{Severity: firing.severity, Rule: firing.Name, Action: action}
	allowed, reason := w.Policy.AllowWithinCooldown(w.State, now())
	switch {
	case w.ruleDryRun(firing.Rule):
		event.Kind, event.Message = eventKindDryRun, "would start (residual processes exited)"
		w.emitLiteral(event)
	case w.InPanic != nil && w.InPanic():
		event.Kind, event.Message = eventKindSuppressed, panicRemediationSuppressed
		w.emit(event)
	case !allowed:
		event.Kind, event.Message = eventKindSuppressed, reason
		w.emit(event)
	default:
		w.executeRemediation(ctx, now, firing, action, nil, false)
		return true
	}
	return false
}

// residualsExited proves every generation gone. An unreadable process counts as
// alive, so a start never follows an unproven exit.
func (w *Worker) residualsExited(procs []process.Process) bool {
	exited := w.generationExited
	if exited == nil {
		exited = process.GenerationExited
	}
	for _, p := range procs {
		if gone, err := exited(p.PID, p.StartTicks); err != nil || !gone {
			return false
		}
	}
	return true
}
