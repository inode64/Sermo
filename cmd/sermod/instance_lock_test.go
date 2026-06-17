package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestAcquireInstanceLockExclusive(t *testing.T) {
	dir := t.TempDir()

	first, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer first.Close()

	_, err = acquireInstanceLock(dir)
	if err == nil {
		t.Fatal("second acquire: want error, got nil")
	}
	var busy *alreadyRunningError
	if !errors.As(err, &busy) {
		t.Fatalf("second acquire: want *alreadyRunningError, got %T (%v)", err, err)
	}
}

func TestAcquireInstanceLockReleasesAfterClose(t *testing.T) {
	dir := t.TempDir()

	first, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("re-acquire after close: %v", err)
	}
	_ = second.Close()
}

func TestReadDaemonPID(t *testing.T) {
	dir := t.TempDir()
	if pid := readDaemonPID(dir); pid != 0 {
		t.Fatalf("missing pidfile = %d, want 0", pid)
	}

	want := os.Getpid()
	path := filepath.Join(dir, "sermod.pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(want)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readDaemonPID(dir); got != want {
		t.Fatalf("readDaemonPID = %d, want %d", got, want)
	}
}

func TestAlreadyRunningErrorMessage(t *testing.T) {
	if got := (&alreadyRunningError{PID: 4242}).Error(); got != "sermod already running (pid 4242)" {
		t.Fatalf("with pid: %q", got)
	}
	if got := (&alreadyRunningError{}).Error(); got != "sermod already running" {
		t.Fatalf("without pid: %q", got)
	}
}
