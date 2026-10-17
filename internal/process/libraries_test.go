package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLibraryMaps(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		count       int
		fail        bool
	}{
		{"pam", "1000-2000 r-xp 0000 08:01 42 /usr/lib64/libpam.so.0 (deleted)\n", 1, false},
		{"plugin", "1000-2000 r-xp 0000 08:01 42 /usr/lib/security/pam_unix.so\n", 1, false},
		{"space", "1000-2000 r-xp 0000 08:01 42 /opt/my app/libx.so\n", 1, false},
		{"data only", "1000-2000 r--p 0000 08:01 42 /usr/lib/libpam.so.0\n", 0, false},
		{"anonymous", "1000-2000 r-xp 0000 00:00 0\n", 0, false},
		{"memfd", "1000-2000 r-xp 0000 00:01 42 /memfd:jit.so (deleted)\n", 0, false},
		{"vdso", "1000-2000 r-xp 0000 00:00 0 [vdso]\n", 0, false},
		{"invalid", "not a map\n", 0, true},
		{"device", "1000-2000 r-xp 0000 bad 42 /lib/libx.so\n", 0, true},
		{"inode", "1000-2000 r-xp 0000 08:01 x /lib/libx.so\n", 0, true},
		{"traversal", "1000-2000 r-xp 0000 08:01 42 /lib/../libx.so\n", 0, true},
		{"newline ambiguity", "1000-2000 r-xp 0000 08:01 42 /lib/lib\\012x.so\n", 0, true},
		{"deduplicate", strings.Repeat("1000-2000 r-xp 0000 08:01 42 /lib/libx.so\n", 2), 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLibraryMaps(tc.input)
			if (err != nil) != tc.fail || len(got) != tc.count {
				t.Fatalf("got %v, %v", got, err)
			}
			if tc.name == "space" && got[0].path != "/opt/my app/libx.so" {
				t.Fatal(got)
			}
		})
	}
}

func libraryFixture() (libraryReader, Process) {
	id := Identity{PID: 42, UID: 0, Exe: "/usr/sbin/sshd", ExeOK: true, StartTicks: 100, StartTicksOK: true}
	r := libraryReader{
		reader: fakeReader{ids: map[int]Identity{42: id}},
		readFile: func(string) ([]byte, error) {
			return []byte("1000-2000 r-xp 0000 08:01 42 /lib/libpam.so.0 (deleted)\n"), nil
		},
		readlink: func(string) (string, error) { return "mnt:[1]", nil },
		stat: func(path string) (libraryFile, error) {
			if path == "/" || path == PIDPath(42, ProcFileRoot) {
				return libraryFile{identity: ExecutableFile{Device: 1, Inode: 1}}, nil
			}
			if strings.HasSuffix(path, procDeletedSuffix) {
				return libraryFile{}, os.ErrNotExist
			}
			return libraryFile{identity: ExecutableFile{Device: 2049, Inode: 43}, regular: true}, nil
		},
	}
	return r, toProcess(id, RoleMain, SourceBackend)
}

func TestScanLibrariesIdentityAndFilesystem(t *testing.T) {
	for _, name := range []string{"replaced", "unchanged", "missing", "permission", "foreign namespace", "chroot", "PID reused", "UID changed", "exe changed", "timeout", "literal deleted suffix", "not regular"} {
		t.Run(name, func(t *testing.T) {
			r, p := libraryFixture()
			ctx := t.Context()
			wantErr := false
			wantCount := 1
			switch name {
			case "unchanged", "literal deleted suffix":
				old := r.stat
				r.stat = func(path string) (libraryFile, error) {
					f, err := old(path)
					if strings.Contains(path, "libpam") && (name == "literal deleted suffix" || !strings.HasSuffix(path, procDeletedSuffix)) {
						return libraryFile{identity: ExecutableFile{Device: 2049, Inode: 42}, regular: true}, nil
					}
					return f, err
				}
				wantCount = 0
			case "missing", "permission", "not regular":
				old := r.stat
				r.stat = func(path string) (libraryFile, error) {
					if !strings.Contains(path, "libpam") {
						return old(path)
					}
					if name == "missing" {
						return libraryFile{}, os.ErrNotExist
					}
					if name == "permission" {
						return libraryFile{}, os.ErrPermission
					}
					return libraryFile{}, nil
				}
				wantErr = name != "missing"
			case "foreign namespace":
				r.readlink = func(path string) (string, error) { return path, nil }
				wantErr = true
			case "chroot":
				old := r.stat
				r.stat = func(path string) (libraryFile, error) {
					f, err := old(path)
					if path == "/" {
						f.identity.Inode = 2
					}
					return f, err
				}
				wantErr = true
			case "PID reused", "UID changed", "exe changed":
				old := r.readFile
				r.readFile = func(path string) ([]byte, error) {
					ids := r.reader.(fakeReader).ids
					id := ids[42]
					switch name {
					case "PID reused":
						id.StartTicks++
					case "UID changed":
						id.UID++
					case "exe changed":
						id.Exe = "/usr/bin/other"
					}
					ids[42] = id
					return old(path)
				}
				wantErr = true
			case "timeout":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				wantErr = true
			}
			if wantErr {
				wantCount = 0
			}
			report, err := r.scan(ctx, []Process{p})
			if (err != nil) != wantErr || len(report.Libraries) != wantCount {
				t.Fatalf("report=%+v err=%v", report, err)
			}
			if name == "missing" && !report.Libraries[0].Missing {
				t.Fatal("missing replacement must stay advisory")
			}
		})
	}
}

func TestScanLibrariesScopeAndPartialFailure(t *testing.T) {
	r, p := libraryFixture()
	if report, err := r.scan(t.Context(), []Process{}); err != nil || report.Processes != 0 {
		t.Fatalf("empty service became host inventory: %+v %v", report, err)
	}
	if report, err := r.scan(t.Context(), nil); err != nil || len(report.Libraries) != 1 {
		t.Fatalf("host inventory: %+v %v", report, err)
	}
	bad := p
	bad.PID = 43
	procs := []Process{bad, p}
	report, err := r.scan(t.Context(), procs)
	if procs[0].PID != 43 {
		t.Fatal("inventory mutated caller's process list")
	}
	if err == nil || len(report.Libraries) != 1 {
		t.Fatalf("partial findings lost: %+v %v", report, err)
	}
	r.readFile = func(string) ([]byte, error) { return nil, os.ErrPermission }
	if _, err := r.scan(t.Context(), []Process{p}); !errors.Is(err, os.ErrPermission) {
		t.Fatal(err)
	}
}

func TestStatLibraryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "libpam.so.0")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := statLibraryFile(path)
	if err != nil || !f.regular || f.identity.Inode == 0 || f.modified == 0 || f.changed == 0 {
		t.Fatalf("stat: %+v %v", f, err)
	}
	if _, err := statLibraryFile(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := statLibraryFile("relative"); err == nil {
		t.Fatal("accepted relative path")
	}
}

type exitingLibraryReader struct {
	fakeReader
	exited bool
}

func (r *exitingLibraryReader) IdentityWithError(pid int) (Identity, bool, error) {
	if r.exited {
		return Identity{}, false, nil
	}
	id, ok := r.Identity(pid)
	return id, ok, nil
}

func TestScanLibrariesConfirmedExitIsNotFailure(t *testing.T) {
	for _, duringScan := range []bool{false, true} {
		r, p := libraryFixture()
		reader := &exitingLibraryReader{fakeReader: r.reader.(fakeReader), exited: !duringScan}
		r.reader = reader
		read := r.readFile
		r.readFile = func(path string) ([]byte, error) { reader.exited = true; return read(path) }
		report, err := r.scan(t.Context(), []Process{p})
		if err != nil || len(report.Libraries) != 0 {
			t.Fatalf("exited process caused a false alert: %+v %v", report, err)
		}
	}
}
