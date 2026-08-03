package process

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestReadExeAgreesWithProcOnDeletedBinaries validates the real /proc parsing
// path — the fake Reader used elsewhere cannot catch a mistake in how the
// kernel's " (deleted)" suffix is recognised or trimmed.
//
// It compares readExe against the raw symlink for every visible process, so it
// is meaningful on any host and simply finds nothing to assert on one where no
// binary has been replaced.
func TestReadExeAgreesWithProcOnDeletedBinaries(t *testing.T) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Skipf("no /proc on this host: %v", err)
	}

	checked, deleted := 0, 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		target, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe"))
		if err != nil || target == "" {
			continue // kernel thread, or not ours to read
		}
		checked++

		exe, ok, prev := readExe(pid)
		wantDeleted := strings.HasSuffix(target, " (deleted)")
		if (prev != "") != wantDeleted {
			t.Errorf("pid %d: exe %q -> prev=%q, want deleted=%v", pid, target, prev, wantDeleted)
			continue
		}
		if !wantDeleted {
			continue
		}
		deleted++

		// The safety invariant: a replaced binary resolves nothing, so it can
		// match no exe selector and can never be signalled.
		if ok || exe != "" {
			t.Errorf("pid %d: a deleted exe must not resolve, got ok=%v exe=%q", pid, ok, exe)
		}
		// The diagnostic: the former path is preserved, without the suffix.
		if want := filepath.Clean(strings.TrimSuffix(target, " (deleted)")); prev != want {
			t.Errorf("pid %d: prev = %q, want %q", pid, prev, want)
		}
		if strings.Contains(prev, "(deleted)") {
			t.Errorf("pid %d: prev must not keep the kernel suffix, got %q", pid, prev)
		}
	}

	if checked == 0 {
		t.Skip("no readable /proc/<pid>/exe links on this host")
	}
	t.Logf("checked %d processes, %d running a replaced binary", checked, deleted)
}
