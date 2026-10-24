package app

import (
	"os/user"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/process"
)

// BenchmarkProcessPolicyCycle measures one process_policy cycle against the
// live host process table, with the daemon's per-cycle /proc snapshot already
// taken (deps.ProcReader), as every cycle after the first sees it.
func BenchmarkProcessPolicyCycle(b *testing.B) {
	current, err := user.Current()
	if err != nil {
		b.Skip(err)
	}
	entry := map[string]any{
		config.WatchKeyCheck: map[string]any{
			checks.CheckKeyType:  checks.CheckTypeProcessPolicy,
			checks.CheckKeyUser:  current.Username,
			checks.CheckKeyAllow: map[string]any{current.Username: map[string]any{checks.CheckKeyExe: "/usr/bin/bash"}},
		},
	}
	shared := process.NewCachingReader(process.OSReader{}, time.Hour)
	if _, err := process.Snapshot(shared); err != nil {
		b.Fatal(err)
	}
	deps := Deps{ProcReader: shared, SystemFreshness: 2 * time.Second, Emit: func(Event) {}}
	watch, msg := buildProcessPolicyWatch("bench-policy", entry, entry[config.WatchKeyCheck].(map[string]any), deps, 30*time.Second)
	if msg != "" {
		b.Fatal(msg)
	}
	for b.Loop() {
		watch.Cycle(b.Context())
	}
}
