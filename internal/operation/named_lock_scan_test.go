package operation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/locks"
	"sermo/internal/process"
)

func TestOperationsRefuseUnverifiableNamedLock(t *testing.T) {
	for _, contents := range []struct {
		name       string
		data       string
		brokenLink bool
	}{
		{name: "empty"},
		{name: "partial", data: `{"owner_pid":`},
		{name: "malformed", data: `not JSON`},
		{name: "unreadable target", brokenLink: true},
	} {
		t.Run(contents.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "svc\\backup.lock")
			if contents.brokenLink {
				if err := os.Symlink(filepath.Join(dir, "missing"), path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte(contents.data), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, action := range []string{"start", "stop", "restart", "reload", "resume", "repair"} {
				t.Run(action, func(t *testing.T) {
					mgr := &fakeManager{}
					var events []Result
					engine := New(Config{Service: "svc", Unit: "svc", Backend: "systemd", Manager: mgr, Scanner: locks.NewScanner(dir), Emit: func(result Result) { events = append(events, result) }})
					released := false
					engine.AcquireLock = func(_ time.Duration) (func() error, error) {
						return func() error { released = true; return nil }, nil
					}
					engine.Discover = func() ([]process.Process, error) { return nil, nil }
					result := engine.Do(context.Background(), action)
					if result.Status != ResultFailed || !strings.Contains(result.Message, "runtime locks") {
						t.Fatalf("unverifiable named lock permitted action: %+v", result)
					}
					if len(mgr.calls) != 0 || !released || len(events) != 1 || events[0].Status != ResultFailed {
						t.Fatalf("calls=%v released=%v events=%+v", mgr.calls, released, events)
					}
				})
			}
		})
	}
}

func TestNamedLockWarningsAreScopedToService(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "other\\backup.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	engine := New(Config{Service: "svc", Backend: "systemd", Manager: &fakeManager{}, Scanner: locks.NewScanner(dir)})
	found, err := engine.NamedLocks()
	if err != nil || len(found) != 0 {
		t.Fatalf("unrelated lock affected service: %v, %v", found, err)
	}
}
