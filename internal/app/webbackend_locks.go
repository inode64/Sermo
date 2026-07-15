package app

import (
	"fmt"

	"sermo/internal/config"
	"sermo/internal/locks"
)

// lockProcProber answers lock-owner liveness for the web backend's lock views.
// Production uses the real /proc-backed prober; tests substitute a deterministic
// one so lock state does not depend on the host's /proc.
var lockProcProber locks.ProcessProber = locks.OSProcessProber{}

func locksScanner(cfg *config.Config) locks.Scanner {
	s := locks.NewScanner(locks.RuntimeLocksDir(cfg.Global.RuntimeDir()))
	s.Proc = lockProcProber
	return s
}

func serviceLocksReport(cfg *config.Config, service string) (locks.Report, error) {
	if cfg == nil {
		return locks.Report{Service: service}, nil
	}
	report, err := locksScanner(cfg).Scan(service)
	if err != nil {
		return locks.Report{Service: service}, fmt.Errorf("scan locks for %s: %w", service, err)
	}
	return report, nil
}

// activeLockNames returns the names of named runtime locks currently blocking
// actions for service (parity with `sermoctl locks SERVICE`, active only).
func activeLockNames(cfg *config.Config, service string) []string {
	report, err := serviceLocksReport(cfg, service)
	if err != nil {
		return nil
	}
	return activeLockNamesFromReport(report)
}

func activeLockNamesFromReport(report locks.Report) []string {
	var names []string
	for i := range report.Locks {
		if report.Locks[i].State != locks.StateActive {
			continue
		}
		name := report.Locks[i].Name
		if name == "" {
			name = watchDefaultLockName
		}
		names = append(names, name)
	}
	return names
}

func (b *WebBackend) activeLockNamesByService() map[string][]string {
	reports := b.lockReportsByService()
	if len(reports) == 0 {
		return nil
	}
	out := make(map[string][]string, len(reports))
	for name, report := range reports {
		out[name] = activeLockNamesFromReport(report)
	}
	return out
}

func (b *WebBackend) lockReportsByService() map[string]locks.Report {
	if b.cfg == nil || len(b.order) == 0 {
		return nil
	}
	names := make([]string, 0, len(b.order))
	for _, name := range b.order {
		entry := b.entries[name]
		if entry == nil || entry.disabled {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil
	}
	reports, err := locksScanner(b.cfg).ScanServices(names)
	if err != nil {
		return nil
	}
	return reports
}
