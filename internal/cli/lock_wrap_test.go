package cli

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not appear", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A SIGTERM aimed at the wrapper must not release the lock while COMMAND is
// still running: sermoctl forwards it and holds the lock until COMMAND exits.
func TestLockWrapForwardsSIGTERMAndHoldsLockUntilCommandExits(t *testing.T) {
	root := t.TempDir()
	global, locksDir := writeLocksConfig(t, root)
	lockPath := filepath.Join(locksDir, "mysql.lock")
	ready := filepath.Join(root, "ready")
	cleaning := filepath.Join(root, "cleaning")
	script := "trap ': > " + cleaning + "; sleep 0.5; exit 0' TERM; : > " + ready + "; while :; do sleep 0.05; done"

	done := make(chan int, 1)
	go func() {
		code, _, _ := runLockCLI(t, "--config", global, "lock", "mysql", "--reason", "backup", "--ttl", "1h",
			"--", "sh", "-c", script)
		done <- code
	}()
	waitForFile(t, ready)
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, cleaning)
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lock released while the command was still cleaning up: %v", err)
	}
	select {
	case code := <-done:
		if code != exitSuccess {
			t.Fatalf("wrap exit = %d, want the command's 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wrapper did not return after the command exited")
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("lock should be released after the command exited: %v", err)
	}
}
