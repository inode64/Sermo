package locks

import (
	"path/filepath"
	"testing"
)

func TestPauseStore(t *testing.T) {
	s := NewPauseStore(filepath.Join(t.TempDir(), "paused"))

	if s.Paused("web") {
		t.Fatal("service should not be paused initially")
	}
	if _, err := s.Pause("web"); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	if !s.Paused("web") {
		t.Fatal("service should be paused after Pause")
	}
	// Pause is idempotent; another service is unaffected.
	if _, err := s.Pause("web"); err != nil {
		t.Fatalf("second Pause() error = %v", err)
	}
	if s.Paused("db") {
		t.Fatal("unrelated service must not be paused")
	}

	was, err := s.Resume("web")
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if !was {
		t.Error("Resume should report the service had been paused")
	}
	if s.Paused("web") {
		t.Fatal("service should be resumed")
	}
	if was, _ := s.Resume("web"); was {
		t.Error("resuming a non-paused service should report false")
	}
}
