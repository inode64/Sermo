package locks

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLockPathRejectsDirectoryComponents(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name     string
		filename string
		wantErr  bool
	}{
		{name: "operation", filename: "mysql.lock"},
		{name: "named", filename: "mysql\\backup.lock"},
		{name: "dotted service", filename: "web.example.lock"},
		{name: "embedded dots", filename: "web..example.lock"},
		{name: "empty", filename: "", wantErr: true},
		{name: "dot", filename: ".", wantErr: true},
		{name: "parent", filename: "..", wantErr: true},
		{name: "absolute", filename: filepath.Join(dir, "mysql.lock"), wantErr: true},
		{name: "traversal", filename: "../mysql.lock", wantErr: true},
		{name: "nested", filename: "child/mysql.lock", wantErr: true},
		{name: "cleaned traversal", filename: "child/../mysql.lock", wantErr: true},
		{name: "trailing separator", filename: "mysql.lock/", wantErr: true},
		{name: "nul", filename: "mysql\x00.lock", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, err := lockPath(dir, tc.filename)
			if tc.wantErr {
				if err == nil || path != "" {
					t.Fatalf("lockPath(%q) = %q, %v; want no path and an error", tc.filename, path, err)
				}
				return
			}
			if err != nil || filepath.Dir(path) != dir || filepath.Base(path) != tc.filename {
				t.Fatalf("lockPath(%q) = %q, %v; want unchanged filename directly under %q", tc.filename, path, err, dir)
			}
		})
	}
}

func TestLockersRejectNULIdentifiers(t *testing.T) {
	dir := t.TempDir()
	operation := NewOperationLocker(dir)
	if _, err := operation.Acquire("mysql\x00", time.Hour); err == nil {
		t.Fatal("operation lock accepted a NUL identifier")
	}
	named := namedLocker(dir, fakeProc{})
	for _, tc := range []struct {
		service string
		name    string
	}{
		{service: "mysql\x00", name: "backup"},
		{service: "mysql", name: "backup\x00"},
	} {
		t.Run(tc.service+"/"+tc.name, func(t *testing.T) {
			if _, err := named.Pin(tc.service, tc.name, "", time.Hour); err == nil {
				t.Fatal("named lock accepted a NUL identifier")
			}
			if err := named.Release(tc.service, tc.name); err == nil {
				t.Fatal("explicit release accepted a NUL identifier")
			}
			if _, err := named.ReleaseInactive(tc.service, tc.name); err == nil {
				t.Fatal("inactive release accepted a NUL identifier")
			}
		})
	}
}
