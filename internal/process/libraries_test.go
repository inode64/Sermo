package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
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
		stat: func(path string) (libraryFile, error) {
			if path == "/" || path == PIDPath(42, ProcFileRoot) {
				return libraryFile{identity: ExecutableFile{Device: 1, Inode: 1}}, nil
			}
			if path == procSelfMountNamespace || path == PIDPath(42, ProcFileMountNamespace) {
				return libraryFile{identity: ExecutableFile{Device: 4, Inode: 4026531841}}, nil
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
	for _, name := range []string{"replaced", "unchanged", "missing", "permission", "own mount namespace", "chroot", "container", "chroot during scan", "PID reused", "UID changed", "exe changed", "timeout", "literal deleted suffix", "not regular"} {
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
			case "own mount namespace":
				// A sandboxed unit sees files Sermo's namespace may not: the
				// installed library is looked up through /proc/PID/root, so the
				// file only the process can see is the one compared.
				old := r.stat
				r.stat = func(path string) (libraryFile, error) {
					if strings.HasPrefix(path, "/lib/") {
						return libraryFile{}, os.ErrNotExist
					}
					if path == PIDPath(42, ProcFileRoot)+"/lib/libpam.so.0" {
						return libraryFile{identity: ExecutableFile{Device: 2049, Inode: 42}, regular: true}, nil
					}
					return old(path)
				}
				wantCount = 0
			case "chroot":
				// dovecot's imap-login: an empty root holds none of the libraries
				// it mapped before chrooting, so they are looked up from Sermo's.
				old := r.stat
				r.stat = func(path string) (libraryFile, error) {
					if strings.HasPrefix(path, PIDPath(42, ProcFileRoot)+"/") {
						return libraryFile{}, os.ErrNotExist
					}
					f, err := old(path)
					if path == "/" {
						f.identity.Inode = 2
					}
					return f, err
				}
			case "container":
				old := r.stat
				r.stat = func(path string) (libraryFile, error) {
					f, err := old(path)
					if path == "/" || path == PIDPath(42, ProcFileMountNamespace) {
						f.identity.Inode = 2
					}
					return f, err
				}
				wantErr = true
			case "chroot during scan":
				// The root is compared again after the maps are read: a chroot
				// in between must fail the process, not pass on the first look.
				mapped := false
				oldRead, oldStat := r.readFile, r.stat
				r.readFile = func(path string) ([]byte, error) { mapped = true; return oldRead(path) }
				r.stat = func(path string) (libraryFile, error) {
					f, err := oldStat(path)
					if mapped && path == PIDPath(42, ProcFileRoot) {
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
			if name == "chroot" && (report.Libraries[0].Missing || !report.Libraries[0].ChrootedProcess) {
				t.Fatalf("a chrooted process's library must be compared with the installed file and marked: %+v", report.Libraries[0])
			}
			if name == "replaced" && report.Libraries[0].ChrootedProcess {
				t.Fatal("a process on Sermo's own root is not chrooted")
			}
		})
	}
}

// A sandboxed unit's chrooted worker shares neither Sermo's root nor its mount
// namespace: only the unit's own daemon can see the libraries it maps.
func TestScanLibrariesReadsSandboxedChrootThroughItsNamespacePeer(t *testing.T) {
	const master, worker, unrelated = 41, 42, 43
	sandbox := ExecutableFile{Device: 4, Inode: 7}
	identity := func(pid int) Identity {
		return Identity{PID: pid, UID: 0, Exe: "/usr/sbin/dovecot", ExeOK: true, StartTicks: 100, StartTicksOK: true}
	}
	realRoot := libraryFile{identity: ExecutableFile{Device: 1, Inode: 1}}
	emptyRoot := libraryFile{identity: ExecutableFile{Device: 1, Inode: 9}}
	installed := libraryFile{identity: ExecutableFile{Device: 2049, Inode: 43}, regular: true}
	stats := map[string]libraryFile{
		"/":                                     realRoot,
		procSelfMountNamespace:                  {identity: ExecutableFile{Device: 4, Inode: 1}},
		PIDPath(master, ProcFileRoot):           realRoot,
		PIDPath(master, ProcFileMountNamespace): {identity: sandbox},
		PIDPath(master, ProcFileRoot) + "/lib/libz.so": installed,
		PIDPath(worker, ProcFileRoot):                  emptyRoot,
		PIDPath(worker, ProcFileMountNamespace):        {identity: sandbox},
		PIDPath(unrelated, ProcFileRoot):               emptyRoot,
		PIDPath(unrelated, ProcFileMountNamespace):     {identity: ExecutableFile{Device: 4, Inode: 8}},
	}
	statCalls := 0
	r := libraryReader{
		reader: fakeReader{ids: map[int]Identity{master: identity(master), worker: identity(worker), unrelated: identity(unrelated)}},
		readFile: func(path string) ([]byte, error) {
			if path == PIDPath(master, procFileMaps) {
				return nil, nil
			}
			return []byte("1000-2000 r-xp 0000 08:01 42 /lib/libz.so\n"), nil
		},
		stat: func(path string) (libraryFile, error) {
			statCalls++
			if f, ok := stats[path]; ok {
				return f, nil
			}
			return libraryFile{}, os.ErrNotExist
		},
	}

	report, err := r.scan(t.Context(), []Process{toProcess(identity(master), RoleMain, SourceBackend), toProcess(identity(worker), RoleChild, SourceChild)})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(report.Libraries) != 1 || report.Libraries[0].Missing || !report.Libraries[0].ChrootedProcess {
		t.Fatalf("libraries = %+v, want the worker's replaced library, marked chrooted", report.Libraries)
	}

	if _, err := r.scan(t.Context(), []Process{toProcess(identity(master), RoleMain, SourceBackend), toProcess(identity(unrelated), RoleChild, SourceChild)}); err == nil {
		t.Fatal("a chroot in a namespace no peer can read must leave the inventory incomplete")
	}

	// The namespace index must not tax the common scan that meets no chroot.
	statCalls = 0
	if _, err := r.scan(t.Context(), []Process{toProcess(identity(master), RoleMain, SourceBackend)}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if statCalls > 6 {
		t.Fatalf("stat calls = %d, a scan without a chroot must not build the namespace index", statCalls)
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

func TestLibraryInventoryIncludesCurrentAndLateModules(t *testing.T) {
	r, p := libraryFixture()
	for _, paths := range [][]string{{"/lib/libone.so"}, {"/lib/libone.so", "/lib/plugin.so"}} {
		r.readFile = func(string) ([]byte, error) {
			var maps strings.Builder
			for _, path := range paths {
				maps.WriteString("1000-2000 r-xp 0000 08:01 43 " + path + "\n")
			}
			return []byte(maps.String()), nil
		}
		report, err := r.scan(t.Context(), []Process{p})
		if err != nil || len(report.Libraries) != 0 || !slices.Equal(report.Dependencies, paths) {
			t.Fatalf("current modules: %+v %v", report, err)
		}
	}
	// A later process cannot inherit the previous service's dependency list.
	r.readFile = func(string) ([]byte, error) { return nil, os.ErrPermission }
	if report, err := r.scan(t.Context(), []Process{p}); err == nil || report.Processes != 0 || len(report.Dependencies) != 0 {
		t.Fatalf("unreadable process reported complete: %+v %v", report, err)
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
