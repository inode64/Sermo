package process

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/sys/unix"
)

type outputFileInfo struct{ device, inode uint64 }

func (outputFileInfo) Name() string       { return "11" }
func (outputFileInfo) Size() int64        { return 0 }
func (outputFileInfo) Mode() os.FileMode  { return os.ModeDevice | os.ModeCharDevice | 0o600 }
func (outputFileInfo) ModTime() time.Time { return time.Time{} }
func (outputFileInfo) IsDir() bool        { return false }
func (f outputFileInfo) Sys() any         { return &syscall.Stat_t{Dev: f.device, Ino: f.inode, Rdev: 11} }

func outputFixture() (outputReader, []Selector, map[string][]byte) {
	ids := map[int]Identity{
		42: {PID: 42, PPID: 1, StartTicks: 100, StartTicksOK: true, Exe: "/usr/bin/driver", ExeOK: true, UID: 0, State: "S"},
		43: {PID: 43, PPID: 42, StartTicks: 101, StartTicksOK: true, Exe: "/usr/bin/worker", ExeOK: true, UID: 0, State: "S"},
		44: {PID: 44, PPID: 43, StartTicks: 102, StartTicksOK: true, Exe: "/usr/bin/writer", ExeOK: true, UID: 0, State: "S"},
	}
	data := map[string][]byte{
		"/proc/42/wchan":    []byte("do_wait\n"),
		"/proc/42/syscall":  fmt.Appendf(nil, "%d 0xffffffffffffffff 0x0", unix.SYS_WAIT4),
		"/proc/42/fdinfo/9": []byte("tty-index:\t11\n"),
		"/proc/42/io":       []byte("rchar: 3\n"),
		"/proc/43/syscall":  fmt.Appendf(nil, "%d 0xffffffffffffffff 0x0", unix.SYS_WAIT4),
		"/proc/44/syscall":  fmt.Appendf(nil, "%d 0x2 0x123 0xa7", unix.SYS_WRITE),
		"/proc/44/stack":    []byte("n_tty_write+0x1/0x2\n"),
		"/proc/44/io":       []byte("wchar: 7\n"),
	}
	dir := func(name string) []os.DirEntry {
		entries, _ := fstest.MapFS{name: &fstest.MapFile{}}.ReadDir(".")
		return entries
	}
	r := outputReader{
		reader: fakeReader{ids: ids}, resolve: fakeUsers(map[string]uint32{"root": 0}),
		read: func(path string) ([]byte, error) {
			b, ok := data[path]
			if !ok {
				return nil, fs.ErrNotExist
			}
			return b, nil
		},
		dirs: func(path string) ([]os.DirEntry, error) {
			if path == "/proc/42/task" {
				return dir("42"), nil
			}
			if path == "/proc/42/fd" {
				return dir("9"), nil
			}
			return nil, nil
		},
		link: func(string) (string, error) { return "/dev/pts/ptmx", nil },
		stat: func(string) (os.FileInfo, error) { return outputFileInfo{device: 1, inode: 12}, nil },
	}
	return r, []Selector{{Name: RoleMain, Type: SelectorCommandMatch, Exe: "/usr/bin/driver", User: "root"}}, data
}

func TestTerminalOutputWaitCycle(t *testing.T) {
	for _, name := range []string{"verified", "different owner", "wrong user", "delegated", "parent reading", "unrelated child", "multiple possible children", "not writing", "non-terminal write", "different terminal", "unreadable stack", "unreadable descriptors", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			r, selectors, data := outputFixture()
			ids := r.reader.(fakeReader).ids
			switch name {
			case "different owner":
				selectors[0].Exe = "/usr/bin/other"
			case "wrong user":
				id := ids[42]
				id.UID = 1
				ids[42] = id
			case "delegated":
				selectors[0].Delegated = true
			case "parent reading":
				data["/proc/42/wchan"] = []byte("do_epoll_wait")
			case "unrelated child":
				data["/proc/42/syscall"] = fmt.Appendf(nil, "%d 99", unix.SYS_WAIT4)
			case "multiple possible children":
				ids[45] = Identity{PID: 45, PPID: 42, StartTicks: 103, StartTicksOK: true}
			case "not writing":
				data["/proc/44/syscall"] = []byte("running")
			case "non-terminal write":
				data["/proc/44/stack"] = []byte("pipe_write+0x1/0x2")
			case "different terminal":
				r.stat = func(path string) (os.FileInfo, error) {
					inode := uint64(12)
					if strings.HasSuffix(path, "/fd/2") {
						inode++
					}
					return outputFileInfo{device: 1, inode: inode}, nil
				}
			case "unreadable stack":
				r.read = func(string) ([]byte, error) { return nil, fs.ErrPermission }
			case "unreadable descriptors":
				r.dirs = func(string) ([]os.DirEntry, error) { return nil, fs.ErrPermission }
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "cancelled" {
				cancel()
			}
			got, err := r.scan(ctx, selectors)
			wantErr := name == "delegated" || name == "unreadable stack" || name == "unreadable descriptors" || name == "cancelled"
			if (err != nil) != wantErr {
				t.Fatalf("scan error = %v", err)
			}
			if name == "verified" {
				if len(got) != 1 || got[0].Owner.PID != 42 || got[0].Writer.PID != 44 || got[0].MasterFD != 9 || got[0].WriterFD != 2 || got[0].OwnerReadChars != 3 || got[0].WriterWriteChars != 7 {
					t.Fatalf("targets = %+v", got)
				}
			} else if len(got) != 0 {
				t.Fatalf("unproven target accepted: %+v", got)
			}
		})
	}
}

func TestTerminalOutputSharedMasterAndConcurrentParentAreNotRecoverable(t *testing.T) {
	for _, name := range []string{"shared master", "multiple threads"} {
		t.Run(name, func(t *testing.T) {
			r, selectors, data := outputFixture()
			original := r.dirs
			r.dirs = func(path string) ([]os.DirEntry, error) {
				if name == "shared master" && path == "/proc/43/fd" {
					data["/proc/43/fdinfo/9"] = data["/proc/42/fdinfo/9"]
					return original("/proc/42/fd")
				}
				if name == "multiple threads" && path == "/proc/42/task" {
					entries, err := original(path)
					return append(entries, entries...), err
				}
				return original(path)
			}
			got, err := r.scan(t.Context(), selectors)
			if err != nil || len(got) != 0 {
				t.Fatalf("ambiguous reader accepted: %+v err=%v", got, err)
			}
		})
	}
}

func TestTerminalOutputProgressingParentAvoidsHostDescriptorScan(t *testing.T) {
	r, selectors, data := outputFixture()
	data["/proc/42/wchan"] = []byte("do_epoll_wait")
	r.link = func(string) (string, error) {
		t.Fatal("progressing parent caused global descriptor scan")
		return "", nil
	}
	if got, err := r.scan(t.Context(), selectors); err != nil || len(got) != 0 {
		t.Fatalf("progressing parent: %+v err=%v", got, err)
	}
}
