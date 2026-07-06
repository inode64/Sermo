// Package diag runs configuration- and host-consistency diagnostics on a loaded
// Sermo config: it validates the config, warns about per-check intervals that
// are not aligned with the global resolution, and reports referenced network
// interfaces, files/directories and mount points that do not exist on the host.
// Scheduled exports write findings to engine.diagnostics; there is no web UI or
// CLI surface for on-demand diagnostics.
//
// Host access goes through a small interface so the diagnostics are testable
// without a real machine.
package diag

import (
	"fmt"
	"sort"

	"sermo/internal/config"
)

// Level is a finding's severity.
type Level string

// Finding severity levels.
const (
	LevelError   Level = "error"
	LevelWarning Level = "warning"
	LevelInfo    Level = "info"
)

// Finding is one diagnostic result.
type Finding struct {
	Level   Level  `json:"level"`
	Scope   string `json:"scope"`
	Message string `json:"message"`
}

// Result is the full set of findings.
type Result struct {
	Findings []Finding `json:"findings"`
}

// Errors returns the number of error-level findings.
func (r Result) Errors() int { return r.count(LevelError) }

// Warnings returns the number of warning-level findings.
func (r Result) Warnings() int { return r.count(LevelWarning) }
func (r Result) count(level Level) (count int) {
	for _, finding := range r.Findings {
		if finding.Level == level {
			count++
		}
	}
	return count
}

// Host is the host access diagnostics need (implemented by OSHost).
type Host interface {
	PathExists(path string) bool
	InterfaceExists(name string) bool
	IsMountPoint(path string) bool
}

// Diagnose runs every diagnostic and returns the findings, ordered by severity
// then scope. host must be non-nil.
func Diagnose(cfg *config.Config, host Host) Result {
	b := &builder{}
	gi := config.EngineInterval(cfg, config.DefaultEngineInterval)

	diagConfig(b, cfg)
	for _, name := range cfg.SortedServiceNames() {
		diagService(b, cfg, name, gi, host)
	}
	diagWatches(b, cfg, gi, host)

	b.sort()
	return Result{Findings: b.findings}
}

type builder struct{ findings []Finding }

func (b *builder) add(level Level, scope, format string, args ...any) {
	b.findings = append(b.findings, Finding{Level: level, Scope: scope, Message: fmt.Sprintf(format, args...)})
}

func (b *builder) sort() {
	sort.SliceStable(b.findings, func(i, j int) bool {
		left, right := levelRank(b.findings[i].Level), levelRank(b.findings[j].Level)
		if left != right {
			return left < right
		}
		return b.findings[i].Scope < b.findings[j].Scope
	})
}

func levelRank(level Level) int {
	switch level {
	case LevelError:
		return 0
	case LevelWarning:
		return 1
	default:
		return 2
	}
}

func diagConfig(b *builder, cfg *config.Config) {
	for _, iss := range config.Validate(cfg) {
		b.add(LevelError, iss.Scope, "%s", iss.Msg)
	}
}
