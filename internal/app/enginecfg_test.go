package app

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/execx/execxtest"
	"sermo/internal/process"
)

// sermod's first generation and every reload derive their engine settings and
// /proc readers from the same owner; the boot path used to leave the SSH
// samplers and the terminal reader unset until the first SIGHUP.
func TestApplyEngineConfigWiresEngineSettingsAndReaders(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	if err := os.WriteFile(global, fmt.Appendf(nil, `engine:
  runtime: %q
  interval: 20s
  max_parallel_checks: 3
defaults:
  policy: { cooldown: 1m }
`, filepath.Join(dir, "run")), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := loadTestConfig(t, global)
	limiter := checks.NewLimiter(1)
	deps := Deps{CheckLimiter: limiter, ExecxRunner: &execxtest.Runner{}}

	ApplyEngineConfig(&deps, cfg, func(process.OSReader) process.Reader { return emptyProcTable{} })

	if deps.Interval != 20*time.Second || deps.MaxParallel != 3 || deps.SystemFreshness != deps.Interval/SystemFreshnessIntervalDivisor {
		t.Fatalf("interval=%v max_parallel=%d freshness=%v", deps.Interval, deps.MaxParallel, deps.SystemFreshness)
	}
	if deps.CheckLimiter != limiter {
		t.Fatal("the shared check limiter was replaced instead of resized")
	}
	if deps.UserLookup == nil || deps.ProcReader == nil || deps.ZombieSampler == nil {
		t.Fatalf("discovery readers not wired: %+v", deps)
	}
	if deps.TerminalProcessReader == nil || deps.SSHIdleSampler == nil || deps.SSHSessionSampler == nil {
		t.Fatal("terminal reader and SSH samplers not wired")
	}
}
