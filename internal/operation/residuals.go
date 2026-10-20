package operation

import (
	"fmt"
	"slices"

	"sermo/internal/process"
)

// checkExternalResiduals prevents a predictable failed restart from first
// taking down the healthy unit. Current unit members still get their normal
// graceful stop; an external orphan cannot be cleared by that backend stop.
func (e Engine) checkExternalResiduals(before process.Observation, result *Result) bool {
	if !before.Trusted {
		return true
	}
	var blocked []process.Process
	for _, proc := range before.Processes {
		if !proc.External || proc.Delegated {
			continue
		}
		proc.SignalBlockReason = e.KillPolicy.BlockReason(proc, e.userResolver())
		if proc.SignalBlockReason != "" {
			blocked = append(blocked, proc)
		}
	}
	if len(blocked) == 0 {
		return true
	}
	result.Status = ResultBlocked
	result.Message = "external residuals would prevent restart; service was not stopped"
	result.Processes = blocked
	return false
}

func (e Engine) recordResidualOutcome(result *Result, outcome residualOutcome) residualOutcome {
	for _, failure := range outcome.failed {
		result.Warnings = append(result.Warnings, fmt.Sprintf("residual pid %d: %v", failure.PID, failure.Err))
	}
	result.Signals = append(result.Signals, outcome.signals...)
	outcome.remaining = slices.Clone(outcome.remaining)
	for i := range outcome.remaining {
		proc := &outcome.remaining[i]
		proc.SignalBlockReason = e.KillPolicy.BlockReason(*proc, e.userResolver())
	}
	return outcome
}

// reattributeUnitMembers lifts the lost-attribution refusal from a tracked
// survivor the kernel still places in the service's own control group. Losing
// the backend's process list is not losing ownership — an OpenRC unit loses it
// as soon as its daemon takes the pidfile along — and membership is the same
// evidence every other residual is signalled on. A survivor that moved to
// another group keeps the refusal, and the pidfd revalidates the group before
// any signal.
func (e Engine) reattributeUnitMembers(procs []process.Process) []process.Process {
	for i := range procs {
		if procs[i].SignalBlockReason == process.ReasonTrackedOutsideDiscovery && e.KillPolicy.IsMember(procs[i]) {
			procs[i].SignalBlockReason = ""
		}
	}
	return procs
}

// withoutGenerations returns procs minus every generation drop holds.
func withoutGenerations(procs, drop []process.Process) []process.Process {
	return slices.DeleteFunc(slices.Clone(procs), func(p process.Process) bool {
		return process.ContainsGeneration(drop, p)
	})
}
