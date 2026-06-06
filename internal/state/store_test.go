package state

import (
	"path/filepath"
	"testing"
)

func TestStoreActiveDefaultsToNotFound(t *testing.T) {
	s := openTemp(t)

	active, found, err := s.Active("web")
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if found {
		t.Errorf("a service with no recorded state must report found=false (got active=%v)", active)
	}
}

func TestStoreSetActiveRoundTrip(t *testing.T) {
	s := openTemp(t)

	if err := s.SetActive("web", false, SourceCLI); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	active, found, err := s.Active("web")
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if !found || active {
		t.Errorf("want found=true active=false, got found=%v active=%v", found, active)
	}

	// Upsert flips the state without duplicating the row.
	if err := s.SetActive("web", true, SourceConfig); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if active, found, _ = s.Active("web"); !found || !active {
		t.Errorf("want found=true active=true after re-set, got found=%v active=%v", found, active)
	}
}

// State must survive a daemon restart/reboot — this is what `monitor: previous`
// relies on. Reopening the same file must preserve the recorded state.
func TestStorePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), Filename)

	first, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := first.SetActive("db", false, SourceCLI); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	active, found, err := second.Active("db")
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if !found || active {
		t.Errorf("state did not persist across reopen: found=%v active=%v", found, active)
	}
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), Filename))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
