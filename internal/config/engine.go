package config

import (
	"math"
	"time"

	"sermo/internal/cfgval"
)

// EngineSection returns the decoded `engine:` mapping, or nil when cfg is nil
// or the block is absent. Callers read typed fields through EngineDuration,
// EngineInt and the other helpers so a missing section cannot panic.
func EngineSection(cfg *Config) map[string]any {
	if cfg == nil {
		return nil
	}
	engine, _ := cfg.Global.Raw[SectionEngine].(map[string]any)
	return engine
}

func engineValue(cfg *Config, key string) any {
	return EngineSection(cfg)[key]
}

const defaultServiceRestartNoticeSubject = "[sermo] ${restart.service}: main process restarted"

// ServiceRestartNotice configures a one-shot global notification when a
// service's principal process is younger than UptimeBelow. A zero value means
// the notice is disabled.
type ServiceRestartNotice struct {
	UptimeBelow time.Duration
	Notify      []string
	Subject     string
	Message     string
}

// EngineServiceRestartNotice returns the resolved global restart-notice policy.
// A missing engine.service_restart_notice block disables the feature.
func EngineServiceRestartNotice(cfg *Config) (ServiceRestartNotice, bool) {
	raw, ok := EngineSection(cfg)[EngineKeyServiceRestartNotice].(map[string]any)
	if !ok {
		return ServiceRestartNotice{}, false
	}
	subject := cfgval.AsString(raw[ServiceRestartNoticeKeySubject])
	if subject == "" {
		subject = defaultServiceRestartNoticeSubject
	}
	return ServiceRestartNotice{
		UptimeBelow: cfgval.Duration(raw[ServiceRestartNoticeKeyUptimeBelow]),
		Notify:      NotifyDefault(map[string]any{sectionNotify: raw[ServiceRestartNoticeKeyNotify]}),
		Subject:     subject,
		Message:     cfgval.AsString(raw[ServiceRestartNoticeKeyMessage]),
	}, true
}

const (
	// DefaultEngineInterval is the fallback for engine.interval.
	DefaultEngineInterval = 30 * time.Second
	// DefaultEngineDiagnosticsInterval is the fallback for engine.diagnostics_interval.
	DefaultEngineDiagnosticsInterval = time.Hour
)

// EngineLogPath returns an engine log file path (access, events, diagnostics).
// An empty string means that log channel is disabled.
func EngineLogPath(cfg *Config, key string) string {
	return EngineString(cfg, key)
}

// EngineString reads a string field from the engine block ("" when unset).
func EngineString(cfg *Config, key string) string {
	return cfgval.AsString(engineValue(cfg, key))
}

// EngineInt reads an int field from the engine block, falling back when the
// value is absent or not an integer form.
func EngineInt(cfg *Config, key string, fallback int) int {
	if v, ok := cfgval.Int(engineValue(cfg, key)); ok {
		return v
	}
	return fallback
}

// EngineBoolDefaultTrue reads a boolean field from the engine block that is on
// unless the operator explicitly turns it off. An absent or non-boolean value
// keeps the feature enabled; validation is what rejects the non-boolean.
func EngineBoolDefaultTrue(cfg *Config, key string) bool {
	enabled, ok := engineValue(cfg, key).(bool)
	return !ok || enabled
}

// EngineByteSize reads a byte-size field (e.g. "64M", "1G") from the engine
// block, falling back when unset or unparseable and saturating at int64 max.
func EngineByteSize(cfg *Config, key string, fallback int64) int64 {
	v, ok := cfgval.ByteSize(engineValue(cfg, key))
	if !ok {
		return fallback
	}
	if v > uint64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(v)
}

// EngineDiagnosticsInterval returns engine.diagnostics_interval, or fallback
// when unset/invalid.
func EngineDiagnosticsInterval(cfg *Config, fallback time.Duration) time.Duration {
	return EngineDuration(cfg, EngineKeyDiagnosticsInterval, fallback)
}

// EngineInterval returns engine.interval, or fallback when unset/invalid.
func EngineInterval(cfg *Config, fallback time.Duration) time.Duration {
	return EngineDuration(cfg, keyInterval, fallback)
}

// EngineDuration returns the engine.<key> duration, or fallback when the field
// is unset, unparseable or not positive.
func EngineDuration(cfg *Config, key string, fallback time.Duration) time.Duration {
	if d := cfgval.Duration(engineValue(cfg, key)); d > 0 {
		return d
	}
	return fallback
}
