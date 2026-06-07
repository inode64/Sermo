// Package diag runs configuration- and host-consistency diagnostics on a loaded
// Sermo config: it validates the config, checks the state database, flags stored
// data for services that no longer exist, warns about per-check intervals that are
// not aligned with the global resolution, and reports referenced network
// interfaces, files/directories and mount points that do not exist on the host.
//
// Host and database access go through small interfaces so the diagnostics are
// testable without a real machine or store.
package diag

import (
	"fmt"
	"sort"
	"time"

	"sermo/internal/config"
)

// Level is a finding's severity.
type Level string

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

// Errors and Warnings count findings by level.
func (r Result) Errors() int   { return r.count(LevelError) }
func (r Result) Warnings() int { return r.count(LevelWarning) }
func (r Result) count(l Level) int {
	n := 0
	for _, f := range r.Findings {
		if f.Level == l {
			n++
		}
	}
	return n
}

// Store is the database access diagnostics need (implemented by state.Store).
type Store interface {
	IntegrityCheck() error
	TrackedServices() ([]string, error)
}

// Host is the host access diagnostics need (implemented by OSHost).
type Host interface {
	PathExists(path string) bool
	InterfaceExists(name string) bool
	IsMountPoint(path string) bool
}

// Diagnose runs every diagnostic and returns the findings, ordered by severity
// then scope. store may be nil (database checks become informational); host must
// be non-nil.
func Diagnose(cfg *config.Config, store Store, host Host) Result {
	b := &builder{}
	gi := globalInterval(cfg)

	diagConfig(b, cfg)
	diagDatabase(b, cfg, store)
	for _, name := range sortedServiceNames(cfg) {
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
	rank := map[Level]int{LevelError: 0, LevelWarning: 1, LevelInfo: 2}
	sort.SliceStable(b.findings, func(i, j int) bool {
		if rank[b.findings[i].Level] != rank[b.findings[j].Level] {
			return rank[b.findings[i].Level] < rank[b.findings[j].Level]
		}
		return b.findings[i].Scope < b.findings[j].Scope
	})
}

func diagConfig(b *builder, cfg *config.Config) {
	for _, iss := range config.Validate(cfg) {
		b.add(LevelError, iss.Scope, "%s", iss.Msg)
	}
}

func diagDatabase(b *builder, cfg *config.Config, store Store) {
	if store == nil {
		b.add(LevelInfo, "database", "no state store available; skipping database checks")
		return
	}
	if err := store.IntegrityCheck(); err != nil {
		b.add(LevelError, "database", "state database is unhealthy: %v", err)
	}
	tracked, err := store.TrackedServices()
	if err != nil {
		b.add(LevelWarning, "database", "could not list stored services: %v", err)
		return
	}
	for _, name := range tracked {
		if _, ok := cfg.Services[name]; !ok {
			b.add(LevelWarning, "database", "stored data (monitoring state / SLA) for service %q which is no longer configured", name)
		}
	}
}

func sortedServiceNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Services))
	for name := range cfg.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// globalInterval reads engine.interval, defaulting to 30s.
func globalInterval(cfg *config.Config) time.Duration {
	if engine, ok := cfg.Global.Raw["engine"].(map[string]any); ok {
		if d := parseDuration(engine["interval"]); d > 0 {
			return d
		}
	}
	return 30 * time.Second
}
