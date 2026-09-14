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

func TestReadHostFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	data, err := ReadFile(path)
	if err != nil || string(data) != "data" {
		t.Fatalf("ReadFile = %q, %v", data, err)
	}
	entries, err := ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("ReadDir = %v entries, %v", len(entries), err)
	}
	if target, err := Readlink(link); err != nil || target != path {
		t.Fatalf("Readlink = %q, %v", target, err)
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
	if _, err := ReadDir("relative"); !errors.Is(err, ErrPath) {
		t.Fatalf("relative ReadDir = %v, want ErrPath", err)
	}
	if _, err := Readlink("relative/link"); !errors.Is(err, ErrPath) {
		t.Fatalf("relative Readlink = %v, want ErrPath", err)
	}
	if _, err := ReadFile(filepath.Join(dir, "missing")); err == nil || errors.Is(err, ErrPath) {
		t.Fatalf("missing file = %v, want the file system error", err)
	}
}

// Every entry point must reject unsafe syntax before accessing the filesystem.
// The target exists so a missing-file error cannot hide a missing Check call.
func TestHostOperationsRejectUnsafePaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	operations := []struct {
		name string
		run  func(string) error
	}{
		{name: "ReadFile", run: func(path string) error { _, err := ReadFile(path); return err }},
		{name: "ReadDir", run: func(path string) error { _, err := ReadDir(path); return err }},
		{name: "Readlink", run: func(path string) error { _, err := Readlink(path); return err }},
		{name: "Open", run: func(path string) error {
			f, err := Open(path)
			if f != nil {
				_ = f.Close()
			}
			return err
		}},
		{name: "OpenFile", run: func(path string) error {
			f, err := OpenFile(path, os.O_RDONLY, 0)
			if f != nil {
				_ = f.Close()
			}
			return err
		}},
	}
	paths := []struct {
		name string
		path string
	}{
		{name: "empty", path: ""},
		{name: "relative", path: "file"},
		{name: "parent traversal", path: dir + "/../" + filepath.Base(dir) + "/file"},
		{name: "dot component", path: dir + "/./file"},
		{name: "duplicate separator", path: dir + "//file"},
		{name: "NUL", path: path + "\x00"},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			for _, tc := range paths {
				t.Run(tc.name, func(t *testing.T) {
					if err := operation.run(tc.path); !errors.Is(err, ErrPath) {
						t.Fatalf("%s(%q) = %v, want ErrPath", operation.name, tc.path, err)
					}
				})
			}
		})
	}
}

func TestOpenFollowsOperatorSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	f, err := Open(link)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	got, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(got, want) {
		t.Fatal("Open did not follow the operator symlink to its target")
	}
}
