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
		proc.SignalBlockReason = e.KillPolicy.BlockReason(proc, e.reapResolver())
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
		proc.SignalBlockReason = e.KillPolicy.BlockReason(*proc, e.reapResolver())
	}
	return outcome
}
