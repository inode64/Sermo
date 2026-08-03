//go:build semgreptest

// Package tests holds fixtures that prove every rule in .semgrep/rules/ still
// fires. `semgrep --test` matches each `ruleid:` annotation against a real
// finding and each `ok:` line against the absence of one, and exits non-zero
// when they disagree — so a rule that silently stops matching fails the build
// instead of passing as a no-op, which is how govet and revive were dormant
// here for a long time.
//
// Go tooling never sees this file: the toolchain skips directories whose name
// begins with a dot, and the build tag keeps it out of any explicit build.
package tests

import (
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

func compareManagers(a, b servicemgr.Manager) bool {
	// ruleid: uncomparable-servicemgr-manager
	return a == b
}

func compareManagerToNil(a servicemgr.Manager) bool {
	// ok: uncomparable-servicemgr-manager
	return a == nil
}

// WebBackend stands in for the real one; semgrep parses rather than compiles.
type WebBackend struct{}

func (b *WebBackend) serviceWarningReason(name string) string {
	// ruleid: web-request-must-not-discover-processes
	_, _ = process.PIDsByComm(name)
	return ""
}

func mutateSharedSnapshot(r *process.CachingReader, pid int) {
	// ruleid: shared-process-snapshot-must-not-be-mutated
	snap := r.Snapshot()
	delete(snap, pid)
}

func readSharedSnapshot(r *process.CachingReader, pid int) bool {
	// ok: shared-process-snapshot-must-not-be-mutated
	snap := r.Snapshot()
	_, ok := snap[pid]
	return ok
}

type Blocker struct{ Cmdline string }

func redactInPlace(items []Blocker) []Blocker {
	for i := range items {
		// ruleid: redactor-must-not-mutate-its-argument
		items[i].Cmdline = ""
	}
	return items
}

// ok: redactor-must-not-mutate-its-argument
func redactViaHelper(items []Blocker) []Blocker {
	return redactCloned(items, func(b *Blocker) { b.Cmdline = "" })
}

func redactCloned[T any](items []T, redact func(*T)) []T { return items }
