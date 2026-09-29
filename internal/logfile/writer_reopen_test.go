package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// logrotate's default rename+create leaves the daemon writing into the
// renamed file unless the writer reopens its configured path.
func TestWriterReopenFollowsRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if err := w.Write(map[string]string{"n": "before"}); err != nil {
		t.Fatal(err)
	}
	rotated := path + ".1"
	if err := os.Rename(path, rotated); err != nil {
		t.Fatal(err)
	}
	if err := w.Reopen(); err != nil {
		t.Fatalf("Reopen() = %v", err)
	}
	if err := w.Write(map[string]string{"n": "after"}); err != nil {
		t.Fatal(err)
	}
	assertLog(t, rotated, `{"n":"before"}`+"\n")
	assertLog(t, path, `{"n":"after"}`+"\n")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != logFileMode {
		t.Fatalf("reopened mode = %o, want %o", got, logFileMode)
	}
}

// A failed reopen must not lose records: the writer keeps its previous handle.
func TestWriterReopenFailureKeepsHandle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "access.log")
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	rotated := filepath.Join(dir, "access.log.1")
	if err := os.Rename(path, rotated); err != nil {
		t.Fatal(err)
	}
	// A regular file where the parent directory should be makes reopen fail.
	if err := os.RemoveAll(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Dir(path), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.Reopen(); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("Reopen() = %v, want error naming %s", err, path)
	}
	if err := w.Write(map[string]int{"n": 1}); err != nil {
		t.Fatalf("Write after failed reopen = %v", err)
	}
	assertLog(t, rotated, `{"n":1}`+"\n")
}

func TestWriterReopenNilAndClosed(t *testing.T) {
	var w *Writer
	if err := w.Reopen(); err != nil {
		t.Fatalf("(*Writer)(nil).Reopen() = %v", err)
	}
	path := filepath.Join(t.TempDir(), "diag.log")
	closed, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// A closed writer stays closed: reopening must not recreate its file.
	if err := closed.Reopen(); err != nil {
		t.Fatalf("closed Reopen() = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("closed writer recreated %s: %v", path, err)
	}
}

func assertLog(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}
