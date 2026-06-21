// Package diag runs configuration- and host-consistency diagnostics on a loaded
// Sermo config: it validates the config, checks the state database, flags stored
// control state for services/watches that no longer exist, warns about
// per-check intervals that are not aligned with the global resolution, and
// reports referenced network interfaces, files/directories and mount points that
// do not exist on the host.
//
// Host and database access go through small interfaces so the diagnostics are
// testable without a real machine or store.
package diag

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"time"

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

// Store is the database access diagnostics need (implemented by state.Store).
type Store interface {
	IntegrityCheck() error
	TrackedControlStates() ([]string, error)
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
	gi := config.EngineInterval(cfg, 30*time.Second)

	diagConfig(b, cfg)
	diagDatabase(b, cfg, store)
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

func diagDatabase(b *builder, cfg *config.Config, store Store) {
	if store == nil {
		b.add(LevelInfo, "database", "no state store available; skipping database checks")
		return
	}
	if err := store.IntegrityCheck(); err != nil {
		b.add(LevelError, "database", "state database is unhealthy: %v", err)
	}
	tracked, err := store.TrackedControlStates()
	if err != nil {
		b.add(LevelWarning, "database", "could not list stored control state: %v", err)
		return
	}
	known := map[string]struct{}{}
	for _, name := range ConfiguredStoredNames(cfg) {
		known[name] = struct{}{}
	}
	for _, name := range tracked {
		if _, ok := known[name]; !ok {
			b.add(LevelWarning, "database", "stored control state for target %q which is no longer configured", name)
		}
	}
}

// ConfiguredStoredNames returns the state-store target names currently backed by
// config: services by name plus host watches under the daemon's `watch:` prefix.
func ConfiguredStoredNames(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	services := cfg.SortedServiceNames()
	watches, _ := cfg.ResolveWatches()
	names := make([]string, 0, len(services)+len(watches))
	names = append(names, services...)
	for _, name := range slices.Sorted(maps.Keys(watches)) {
		names = append(names, "watch:"+name)
	}
	return names
}
