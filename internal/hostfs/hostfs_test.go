package hostfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckRejectsUnsafePaths(t *testing.T) {
	tests := []struct {
		name string
		path string
		ok   bool
	}{
		{name: "absolute clean", path: "/proc/1/stat", ok: true},
		{name: "root", path: "/", ok: true},
		{name: "empty", path: ""},
		{name: "relative", path: "etc/fstab"},
		{name: "traversal", path: "/proc/1/../2/stat"},
		{name: "unclean", path: "/proc//1/stat"},
		{name: "trailing slash", path: "/run/sermo/"},
		{name: "nul byte", path: "/proc/1\x00/stat"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Check(tc.path)
			if tc.ok && err != nil {
				t.Fatalf("Check(%q) = %v, want nil", tc.path, err)
			}
			if !tc.ok && !errors.Is(err, ErrPath) {
				t.Fatalf("Check(%q) = %v, want ErrPath", tc.path, err)
			}
		})
	}
}

func TestReadOpenAndOpenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := ReadFile(path)
	if err != nil || string(data) != "data" {
		t.Fatalf("ReadFile = %q, %v", data, err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = f.Close()
	created := filepath.Join(dir, "created")
	f, err = OpenFile(created, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	_ = f.Close()
	if _, err := os.Stat(created); err != nil {
		t.Fatalf("OpenFile did not create the file: %v", err)
	}
	if _, err := ReadFile("relative/file"); !errors.Is(err, ErrPath) {
		t.Fatalf("relative ReadFile = %v, want ErrPath", err)
	}
	if _, err := Open(dir + "/../x"); !errors.Is(err, ErrPath) {
		t.Fatalf("traversing Open = %v, want ErrPath", err)
	}
	if _, err := OpenFile("", os.O_RDONLY, 0); !errors.Is(err, ErrPath) {
		t.Fatalf("empty OpenFile = %v, want ErrPath", err)
	}
	if _, err := ReadFile(filepath.Join(dir, "missing")); err == nil || errors.Is(err, ErrPath) {
		t.Fatalf("missing file = %v, want the file system error", err)
	}
}
