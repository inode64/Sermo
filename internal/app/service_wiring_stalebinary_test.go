package app

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sermo/internal/process"
)

type fakeFileInfo struct{ name string }

func (f fakeFileInfo) Name() string     { return f.name }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() fs.FileMode  { return 0o755 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() any           { return nil }

// An upgrade leaves a file at the path, an uninstall leaves none; a stat that
// fails for another reason keeps the replaced reading. Each path is read once.
func TestMarkRemovedBinariesStatsEachPathOnce(t *testing.T) {
	stats := map[string]int{}
	stat := func(path string) (fs.FileInfo, error) {
		stats[path]++
		switch path {
		case "/usr/bin/apache2":
			return nil, os.ErrNotExist
		case "/usr/sbin/hung":
			return nil, os.ErrDeadlineExceeded
		}
		return fakeFileInfo{name: path}, nil
	}
	got := markRemovedBinaries([]process.StaleBinary{
		{PID: 1, Path: "/usr/bin/apache2"}, {PID: 2, Path: "/usr/bin/apache2"},
		{PID: 3, Path: "/usr/sbin/sshd"}, {PID: 4, Path: "/usr/sbin/hung"},
	}, stat, func(string) ([]fs.DirEntry, error) { return nil, nil })
	want := []bool{true, true, false, false}
	for i, s := range got {
		if s.Removed != want[i] {
			t.Fatalf("pid %d removed = %v, want %v", s.PID, s.Removed, want[i])
		}
	}
	if stats["/usr/bin/apache2"] != 1 {
		t.Fatalf("stat calls = %v, want one per path", stats)
	}
}

// An interpreter upgrade removes python3.13 once python3.14 is beside it: the
// daemons it ran are upgraded, not uninstalled, so a restart may renew them.
// Without a newer version the path still reads as removed.
func TestMarkRemovedBinariesKnowsAnUpgradedInterpreter(t *testing.T) {
	gone := func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist }
	bin := t.TempDir()
	for _, name := range []string{"python3.14", "perl5.40"} {
		if err := os.WriteFile(filepath.Join(bin, name), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	listings := 0
	readDir := func(dir string) ([]fs.DirEntry, error) {
		listings++
		return os.ReadDir(dir)
	}
	got := markRemovedBinaries([]process.StaleBinary{
		{PID: 1, Path: filepath.Join(bin, "python3.13")}, {PID: 2, Path: filepath.Join(bin, "python3.13")},
		{PID: 3, Path: filepath.Join(bin, "perl5.42")}, {PID: 4, Path: filepath.Join(bin, "nginx")},
	}, gone, readDir)
	want := []bool{false, false, true, true}
	for i, s := range got {
		if s.Removed != want[i] {
			t.Fatalf("pid %d (%s) removed = %v, want %v", s.PID, s.Path, s.Removed, want[i])
		}
	}
	if listings != 2 {
		t.Fatalf("directory listings = %d, want one per gone versioned path", listings)
	}
}
