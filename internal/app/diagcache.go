package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"sermo/internal/config"
	"sermo/internal/diag"
	"sermo/internal/logfile"
)

// DiagnosticLog runs scheduled diagnostics and appends snapshots to
// engine.diagnostics when configured.
type DiagnosticLog struct {
	mu     sync.Mutex
	cfg    *config.Config
	host   diag.Host
	opGate *OpGate
	file   *logfile.Writer
	now    func() time.Time
}

// NewDiagnosticLog builds a scheduled diagnostics exporter. file must be set.
func NewDiagnosticLog(cfg *config.Config, host diag.Host, opGate *OpGate, file *logfile.Writer, now func() time.Time) *DiagnosticLog {
	if now == nil {
		now = time.Now
	}
	if host == nil {
		host = diag.OSHost{}
	}
	return &DiagnosticLog{
		cfg:    cfg,
		host:   host,
		opGate: opGate,
		file:   file,
		now:    now,
	}
}

// UpdateConfig swaps the configuration used for the next export (config reload).
func (l *DiagnosticLog) UpdateConfig(cfg *config.Config) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.cfg = cfg
	l.mu.Unlock()
}

// Export runs diagnostics and appends one JSON line to the log file.
func (l *DiagnosticLog) Export() {
	if l == nil || l.file == nil {
		return
	}
	l.mu.Lock()
	cfg := l.cfg
	host := l.host
	opGate := l.opGate
	file := l.file
	now := l.now
	l.mu.Unlock()

	findings := collectDiagnosticFindings(cfg, host, opGate)
	at := now().UTC()
	errors, warnings := countDiagFindingLevels(findings)
	_ = file.Write(map[string]any{
		"time":     at.Format(time.RFC3339),
		"errors":   errors,
		"warnings": warnings,
		"findings": findings,
	})
}

// Run exports immediately and then on every interval until ctx is cancelled.
func (l *DiagnosticLog) Run(ctx context.Context, interval time.Duration) {
	if l == nil || interval <= 0 {
		return
	}
	l.Export()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.Export()
		}
	}
}

func collectDiagnosticFindings(cfg *config.Config, host diag.Host, opGate *OpGate) []diag.Finding {
	r := diag.Diagnose(cfg, host)
	out := make([]diag.Finding, 0, len(r.Findings)+4)
	out = append(out, r.Findings...)
	if opGate != nil {
		inUse, total := opGate.Usage()
		out = append(out, operationSlotDiagFindings(inUse, total)...)
	}
	out = append(out, lockDiagFindings(cfg)...)
	return out
}

func countDiagFindingLevels(findings []diag.Finding) (errors, warnings int) {
	for _, f := range findings {
		switch f.Level {
		case diag.LevelError:
			errors++
		case diag.LevelWarning:
			warnings++
		}
	}
	return errors, warnings
}

func lockDiagFindings(cfg *config.Config) []diag.Finding {
	if cfg == nil {
		return nil
	}
	warnings, err := locksScanner(cfg).ScanDir()
	var out []diag.Finding
	if err != nil {
		out = append(out, diag.Finding{Level: diag.LevelError, Scope: "locks", Message: err.Error()})
	}
	for _, w := range warnings {
		out = append(out, diag.Finding{Level: diag.LevelWarning, Scope: "locks", Message: w})
	}
	return out
}

func operationSlotDiagFindings(inUse, total int) []diag.Finding {
	if total <= 0 || inUse <= 0 {
		return nil
	}
	if inUse >= total {
		return []diag.Finding{{
			Level:   diag.LevelWarning,
			Scope:   "operations",
			Message: fmt.Sprintf("operation slots saturated (%d/%d in use)", inUse, total),
		}}
	}
	return []diag.Finding{{
		Level:   diag.LevelInfo,
		Scope:   "operations",
		Message: fmt.Sprintf("operation slots %d/%d in use", inUse, total),
	}}
}
