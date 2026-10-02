package process

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadExeDeletedDiagnostic(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, target, exe, previous string
		ok                          bool
	}{
		{name: "current", target: testExe, exe: testExe, ok: true},
		{name: "deleted", target: testExe + " (deleted)", previous: testExe},
		{name: "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "exe")
			if tc.target != "" {
				if err := os.Symlink(tc.target, path); err != nil {
					t.Fatal(err)
				}
			}
			exe, ok, previous := readExeAt(path)
			if exe != tc.exe || ok != tc.ok || previous != tc.previous {
				t.Fatalf("exe=%q ok=%v previous=%q", exe, ok, previous)
			}
		})
	}
}

func TestOpenDeletedExecutablePinsUnlinkedFile(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                                                string
		linked, noExec, otherNamespace, badLink, unreadable bool
		want                                                bool
	}{
		{name: "unlinked executable", want: true},
		{name: "literal suffix is not unlink", linked: true},
		{name: "non executable", noExec: true},
		{name: "other namespace", otherNamespace: true},
		{name: "changed proc link", badLink: true},
		{name: "unreadable", unreadable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "executable")
			mode := os.FileMode(0o700)
			if tc.noExec {
				mode = 0o600
			}
			if err := os.WriteFile(path, []byte("fixture"), mode); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.Close() })
			if !tc.linked {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			r := executableReader{
				open: func(string) (*os.File, error) {
					if tc.unreadable {
						return nil, os.ErrPermission
					}
					return f, nil
				},
				readlink: func(p string) (string, error) {
					if strings.HasSuffix(p, "ns/mnt") {
						if tc.otherNamespace && p != SelfPath("ns/mnt") {
							return "mnt:[2]", nil
						}
						return "mnt:[1]", nil
					}
					if tc.badLink {
						return "/other (deleted)", nil
					}
					return testExe + procDeletedSuffix, nil
				},
			}
			got, identity, err := r.openDeleted(100, testExe)
			if (err == nil) != tc.want {
				t.Fatalf("file=%v identity=%+v err=%v", got, identity, err)
			}
			if tc.want && (got != f || identity.Inode == 0) {
				t.Fatalf("unpinned identity: %+v", identity)
			}
			if !tc.want && !tc.otherNamespace && !tc.unreadable {
				if _, err := f.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("failed verification left file open: %v", err)
				}
			}
		})
	}
}
