package locks

import (
	"path/filepath"
	"testing"
)

// TestNamedLockNoCollisionWithDottedService pins that a named lock and a bare
// lock for a dotted service name map to distinct files and are each attributed to
// the right service only. With a '.' separator, lock "backup" on service "web"
// and a bare lock for a service literally named "web.backup" both produced
// web.backup.lock — an O_EXCL false conflict on write and a double-attribution on
// scan.
func TestNamedLockNoCollisionWithDottedService(t *testing.T) {
	l := namedLocker(t.TempDir(), fakeProc{})

	named := l.path("web", "backup") // lock "backup" on service "web"
	bare := l.path("web.backup", "") // bare lock for service "web.backup"
	if named == bare {
		t.Fatalf("named %q and bare %q must not collide", named, bare)
	}

	namedFile, bareFile := filepath.Base(named), filepath.Base(bare)

	if name, ok := matchService(namedFile, "web"); !ok || name != "backup" {
		t.Fatalf("matchService(%q, web) = %q,%v; want backup,true", namedFile, name, ok)
	}
	if _, ok := matchService(namedFile, "web.backup"); ok {
		t.Fatalf("%q must not be attributed to service web.backup", namedFile)
	}
	if name, ok := matchService(bareFile, "web.backup"); !ok || name != "" {
		t.Fatalf("matchService(%q, web.backup) = %q,%v; want \"\",true", bareFile, name, ok)
	}
	if _, ok := matchService(bareFile, "web"); ok {
		t.Fatalf("%q must not be attributed to service web", bareFile)
	}
}
