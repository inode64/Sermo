package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// alreadyRunningError is returned when another sermod instance holds the
// runtime singleton lock.
type alreadyRunningError struct {
	PID int
}

func (e *alreadyRunningError) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("sermod already running (pid %d)", e.PID)
	}
	return "sermod already running"
}

// acquireInstanceLock takes an exclusive non-blocking flock on
// <runtime>/sermod.lock. The returned file must stay open for the life of the
// process; closing it releases the lock.
func acquireInstanceLock(runtimeDir string) (*os.File, error) {
	if runtimeDir == "" {
		runtimeDir = defaultRuntimeDir
	}
	path := filepath.Join(runtimeDir, instanceLockFilename)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open instance lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, &alreadyRunningError{PID: readDaemonPID(runtimeDir)}
	}
	return f, nil
}

// readDaemonPID returns the PID from <runtime>/sermod.pid when present and
// parseable. It is best-effort context for an already-running warning.
func readDaemonPID(runtimeDir string) int {
	data, err := os.ReadFile(filepath.Join(runtimeDir, daemonPIDFilename))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
