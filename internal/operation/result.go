// Package operation is the safe start/stop/restart/reload/resume engine. It is the
// single path both sermoctl and sermod use, so a manual action and an automatic
// remediation are protected identically: internal operation lock, active named
// runtime locks, required preflight, guards, residual-process handling with
// signal escalation, and postflight.
//
// The engine itself is pure orchestration over injected capability closures, so
// the section-18 flow can be tested without real services; New wires the real
// components from a resolved service.
package operation

import (
	"sermo/internal/checks"
	"sermo/internal/locks"
	"sermo/internal/process"
)

// ResultStatus is the outcome of an operation.
type ResultStatus string

// Operation result statuses.
const (
	ResultOK               ResultStatus = "ok"
	ResultBlocked          ResultStatus = "blocked"
	ResultPreflightFailed  ResultStatus = "preflight_failed"
	ResultPostflightFailed ResultStatus = "postflight_failed"
	ResultFailed           ResultStatus = "failed"
	ResultOrphanProcesses  ResultStatus = "orphan_processes"
)

// Result is the single outcome emitted per operation.
type Result struct {
	Service   string            `json:"service"`
	Action    string            `json:"action"`
	Status    ResultStatus      `json:"status"`
	Message   string            `json:"message,omitempty"`
	Backend   string            `json:"backend,omitempty"`
	Checks    []checks.Result   `json:"checks,omitempty"`
	Locks     []locks.Lock      `json:"locks,omitempty"`
	Processes []process.Process `json:"processes,omitempty"`
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
