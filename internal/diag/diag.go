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
	"cmp"
	"fmt"
	"slices"

	"sermo/internal/config"
	"sermo/internal/severity"
)

// Finding is one diagnostic result.
type Finding struct {
	Level   severity.Level `json:"level"`
	Scope   string         `json:"scope"`
	Message string         `json:"message"`
}

// Result is the full set of findings.
type Result struct {
	Findings []Finding `json:"findings"`
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

func (b *builder) addf(level severity.Level, scope, format string, args ...any) {
	b.findings = append(b.findings, Finding{Level: level, Scope: scope, Message: fmt.Sprintf(format, args...)})
}

func (b *builder) sort() {
	slices.SortStableFunc(b.findings, func(a, b Finding) int {
		return cmp.Or(cmp.Compare(b.Level.Rank(), a.Level.Rank()), cmp.Compare(a.Scope, b.Scope))
	})
}

func diagConfig(b *builder, cfg *config.Config) {
	for _, iss := range config.Validate(cfg) {
		b.addf(severity.Error, iss.Scope, "%s", iss.Msg)
	}
	for _, iss := range config.Warnings(cfg) {
		b.addf(severity.Warning, iss.Scope, "%s", iss.Msg)
	}
}
