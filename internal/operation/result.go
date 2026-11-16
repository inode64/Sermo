// Package operation is the safe start/stop/restart/reload/resume engine. It is the
// single path both sermoctl and sermod use, so a manual action and an automatic
// remediation are protected identically: internal operation lock, active named
// runtime locks, required preflight, guards, residual-process handling with
// signal escalation, and postflight.
//
// The engine itself is pure orchestration over injected capability closures, so
// the operation flow can be tested without real services; New wires the real
// components from a resolved service.
package operation

import (
	"fmt"
	"strings"

	"sermo/internal/checks"
	"sermo/internal/locks"
	"sermo/internal/process"
	"sermo/internal/rules"
)

// ResultStatus is the outcome of an operation.
type ResultStatus string

// Operation result statuses.
const (
	ResultOK               ResultStatus = "ok"
	ResultSkipped          ResultStatus = "skipped"
	ResultBlocked          ResultStatus = "blocked"
	ResultPreflightFailed  ResultStatus = "preflight_failed"
	ResultPostflightFailed ResultStatus = "postflight_failed"
	ResultFailed           ResultStatus = "failed"
	ResultOrphanProcesses  ResultStatus = "orphan_processes"
)

// Result is the single outcome emitted per operation.
type Result struct {
	Service string       `json:"service"`
	Action  string       `json:"action"`
	Status  ResultStatus `json:"status"`
	Message string       `json:"message,omitempty"`
	// RequestedAction preserves the operator/rule intent when an already stopped
	// Docker target only needs a start. Guards still use the requested action.
	RequestedAction  string `json:"requested_action,omitempty"`
	CreatedContainer string `json:"created_container,omitempty"`
	// Guard names the guard rule that blocked the action, when one did;
	// GuardEvidence holds the readings that made it hold, apart from Message
	// because they quote the host.
	Guard         string                  `json:"guard,omitempty"`
	GuardEvidence string                  `json:"guard_evidence,omitempty"`
	Backend       string                  `json:"backend,omitempty"`
	Warnings      []string                `json:"warnings,omitempty"`
	Checks        []checks.Result         `json:"checks,omitempty"`
	Locks         []locks.Lock            `json:"locks,omitempty"`
	Processes     []process.Process       `json:"processes,omitempty"`
	Signals       []process.SignalAttempt `json:"signals,omitempty"`
	// Relaunched reports that a successful action left a new generation of the
	// service running, which is what takes in a changed artifact. A restart always
	// does; a start only when it found the service down, because on one already
	// running it succeeds without touching it. An unknown status is no relaunch.
	Relaunched bool `json:"-"`
}

// Summary is the message followed by the readings that held a blocking guard,
// as sermoctl, the Web UI and notifications show a result.
func (r Result) Summary() string {
	return r.guardBlock().MessageWith(r.Message)
}

func (r Result) guardBlock() rules.GuardBlock {
	return rules.GuardBlock{Rule: r.Guard, Reason: r.Message, Evidence: r.GuardEvidence}
}

// AuditMessage preserves process identities, policy refusals, the checks that
// failed and each attempted signal in the operation's single persisted event,
// including successful cleanup. A preflight_failed or postflight_failed event
// thus says which check refused the action and why.
func (r Result) AuditMessage() string {
	return r.AuditMessageWith(r.Message)
}

// AuditMessageWith is AuditMessage with message in place of the result's own:
// a caller that expands the message's runtime built-ins (a guard's ${event})
// passes the expansion, while the details, which quote the host, stay as
// written.
func (r Result) AuditMessageWith(message string) string {
	return strings.Join(append([]string{message}, r.auditDetails()...), "; ")
}

// auditDetails is what the audit text adds to the message: the readings that
// held a blocking guard, the requested action, a created container, the
// failed checks, the residual processes and the signals.
func (r Result) auditDetails() []string {
	parts := make([]string, 0, len(r.Checks)+len(r.Processes)+len(r.Signals))
	if readings := r.guardBlock().Readings(); readings != "" {
		parts = append(parts, readings)
	}
	if r.RequestedAction != "" {
		parts = append(parts, "requested "+r.RequestedAction)
	}
	if r.CreatedContainer != "" {
		parts = append(parts, "created container "+r.CreatedContainer)
	}
	for _, c := range r.Checks {
		if text := FailedCheckText(c); text != "" {
			parts = append(parts, text)
		}
	}
	for _, proc := range r.Processes {
		exe := proc.Exe
		if proc.ExePrev != "" {
			exe = proc.ExePrev + " (deleted)"
		}
		parts = append(parts, fmt.Sprintf("residual pid=%d uid=%d role=%s exe=%q blocked=%q", proc.PID, proc.UID, proc.Role, exe, proc.SignalBlockReason))
	}
	for _, attempt := range r.Signals {
		message := fmt.Sprintf("pid=%d %s", attempt.PID, attempt.Signal)
		if attempt.Error != "" {
			message += ": " + attempt.Error
		}
		parts = append(parts, message)
	}
	return parts
}

// FailedCheckText words a check that failed in an operation, the same in its
// event and in sermoctl's output; a check that passed has nothing to say.
func FailedCheckText(c checks.Result) string {
	switch {
	case c.OK:
		return ""
	case c.Optional:
		return fmt.Sprintf("optional check %s failed: %s", c.Check, c.Message)
	default:
		return fmt.Sprintf("check %s failed: %s", c.Check, c.Message)
	}
}

// OK reports whether the operation completed successfully.
func (r Result) OK() bool { return r.Status == ResultOK }

// RecordsRemediation reports whether an automatic remediation attempt ran far
// enough through the engine to count against cooldown, rate limits and backoff.
// Blocked and preflight-failed operations never execute the service action.
func (r Result) RecordsRemediation() bool {
	switch r.Status {
	case ResultOK, ResultFailed, ResultPostflightFailed, ResultOrphanProcesses:
		return true
	default:
		return false
	}
}
