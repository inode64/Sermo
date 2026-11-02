package app

import (
	"io/fs"
	"os"
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
	}, stat)
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
