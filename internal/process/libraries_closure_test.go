package process

import (
	"context"
	"debug/elf"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const (
	gccOldRuntime = "/usr/lib/gcc/x86_64-pc-linux-gnu/15/libstdc++.so.6.0.34"
	gccNewRuntime = "/usr/lib/gcc/x86_64-pc-linux-gnu/16/libstdc++.so.6"
	gccSONAME     = "libstdc++.so.6"
	gccExe        = "/usr/bin/exim"
)

// gccSlotFixture is a host whose compiler slot changed: every process maps the
// old compiler's libstdc++, whose directory is gone, and the new compiler
// provides the same SONAME in its own directory.
type gccSlotFixture struct {
	reader   libraryReader
	procs    []Process
	mapped   ExecutableFile
	newFile  libraryFile
	current  libraryELF
	resolved map[string]string
	missing  []string
	err      error
	calls    []string
}

func newGCCSlotFixture(pids ...int) *gccSlotFixture {
	f := &gccSlotFixture{
		mapped:   ExecutableFile{Device: unix.Mkdev(8, 1), Inode: 77},
		newFile:  libraryFile{identity: ExecutableFile{Device: 2049, Inode: 90}, regular: true, modified: 5, changed: 6},
		resolved: map[string]string{gccSONAME: gccNewRuntime},
	}
	f.current = libraryELF{file: f.newFile, soname: gccSONAME, class: elf.ELFCLASS64, machine: elf.EM_X86_64}
	ids := map[int]Identity{}
	for _, pid := range pids {
		id := Identity{PID: pid, UID: 0, Exe: gccExe, ExeOK: true, StartTicks: uint64(100 + pid), StartTicksOK: true}
		ids[pid] = id
		f.procs = append(f.procs, toProcess(id, RoleMain, SourceBackend))
	}
	f.reader = libraryReader{
		reader: fakeReader{ids: ids},
		readFile: func(string) ([]byte, error) {
			return []byte("1000-2000 r-xp 0000 08:01 77 " + gccOldRuntime + " (deleted)\n"), nil
		},
		stat: func(path string) (libraryFile, error) {
			switch {
			case path == "/", strings.HasSuffix(path, "/"+ProcFileRoot):
				return libraryFile{identity: ExecutableFile{Device: 1, Inode: 1}}, nil
			case strings.HasSuffix(path, gccNewRuntime):
				return f.newFile, nil
			}
			return libraryFile{}, os.ErrNotExist
		},
		elf: func(path string, expected ExecutableFile) (libraryELF, error) {
			if strings.HasSuffix(path, "/"+procFileMapFiles+"/1000-2000") {
				if expected != f.mapped {
					return libraryELF{}, errors.New("old mapped identity lost")
				}
				return libraryELF{soname: gccSONAME, class: elf.ELFCLASS64, machine: elf.EM_X86_64}, nil
			}
			if strings.HasSuffix(path, gccNewRuntime) && expected == f.newFile.identity {
				return f.current, nil
			}
			return libraryELF{}, os.ErrNotExist
		},
	}
	f.reader.dependencies = func(_ context.Context, root, exe string) (ExecutableDependencies, error) {
		f.calls = append(f.calls, root+" "+exe)
		return ExecutableDependencies{Resolved: f.resolved, Missing: f.missing}, f.err
	}
	return f
}

func TestScanLibrariesReplacesARemovedLibraryTheExecutableLinks(t *testing.T) {
	f := newGCCSlotFixture(42, 43)
	report, err := f.reader.scan(t.Context(), f.procs)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Libraries) != 2 {
		t.Fatalf("findings = %+v, want one per process", report.Libraries)
	}
	for _, lib := range report.Libraries {
		if lib.Missing || lib.ReplacementPath != gccNewRuntime || lib.Installed != f.newFile.identity || lib.Path != gccOldRuntime {
			t.Fatalf("finding = %+v, want the new compiler's runtime as its replacement", lib)
		}
	}
	// Both workers run the same executable under the same root: one closure.
	if want := []string{PIDPath(42, ProcFileRoot) + " " + gccExe}; !slices.Equal(f.calls, want) {
		t.Fatalf("resolver calls = %v, want %v", f.calls, want)
	}
}

func TestScanLibrariesKeepsAnUnprovenRemovedLibraryMissing(t *testing.T) {
	for _, name := range []string{"not linked", "resolver error", "no resolver", "relative resolution", "different SONAME", "different class", "different machine", "changed file", "same inode", "not regular"} {
		t.Run(name, func(t *testing.T) {
			f := newGCCSlotFixture(42)
			switch name {
			case "not linked":
				// A dlopen plugin: the executable's closure does not name it.
				f.resolved = map[string]string{"libother.so.1": "/usr/lib64/libother.so.1"}
			case "resolver error":
				f.err = errors.New("read dependencies of /usr/bin/exim: truncated")
			case "no resolver":
				f.reader.dependencies = nil
			case "relative resolution":
				f.resolved = map[string]string{gccSONAME: "usr/lib/gcc/x86_64-pc-linux-gnu/16/libstdc++.so.6"}
			case "different SONAME":
				f.current.soname = "libstdc++.so.7"
			case "different class":
				f.current.class = elf.ELFCLASS32
			case "different machine":
				f.current.machine = elf.EM_AARCH64
			case "changed file":
				f.current.file.changed++
			case "same inode":
				f.newFile.identity = f.mapped
				f.current.file = f.newFile
			case "not regular":
				f.newFile.regular = false
			}
			report, err := f.reader.scan(t.Context(), f.procs)
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Libraries) != 1 || !report.Libraries[0].Missing || report.Libraries[0].ReplacementPath != "" {
				t.Fatalf("findings = %+v, want the removed library still missing", report.Libraries)
			}
		})
	}
}

func TestScanLibrariesWithholdsAPrincipalThatWouldNotStart(t *testing.T) {
	for _, name := range []string{"resolves", "missing SONAME", "unreadable executable", "advisory inventory", "worker only", "no stale library"} {
		t.Run(name, func(t *testing.T) {
			f := newGCCSlotFixture(42, 43)
			f.reader.principal = 42
			wantUnresolved, wantError := []string(nil), ""
			switch name {
			case "missing SONAME":
				f.missing = []string{"libgone.so.1"}
				wantUnresolved = f.missing
			case "unreadable executable":
				f.err = errors.New("inspect /usr/bin/exim: no such file or directory")
				wantError = "resolve /usr/bin/exim dependencies: inspect /usr/bin/exim: no such file or directory"
			case "advisory inventory":
				f.missing = []string{"libgone.so.1"}
				f.reader.principal = 0
			case "worker only":
				f.missing = []string{"libgone.so.1"}
				f.reader.principal = 99
			case "no stale library":
				f.missing = []string{"libgone.so.1"}
				f.reader.stat = func(path string) (libraryFile, error) {
					if strings.HasSuffix(path, gccOldRuntime+procDeletedSuffix) {
						return libraryFile{identity: f.mapped, regular: true}, nil
					}
					return libraryFile{identity: ExecutableFile{Device: 1, Inode: 1}}, nil
				}
			}
			report, err := f.reader.scan(t.Context(), f.procs)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(report.ExecutableUnresolved, wantUnresolved) || report.ExecutableError != wantError {
				t.Fatalf("unresolved = %v, error = %q; want %v, %q", report.ExecutableUnresolved, report.ExecutableError, wantUnresolved, wantError)
			}
			if name == "no stale library" && len(f.calls) != 0 {
				t.Fatalf("resolver ran %d time(s) with nothing stale", len(f.calls))
			}
		})
	}
}

// A cancelled resolution proves nothing about the executable, so the scan does
// not keep it for the processes that follow.
func TestLibraryClosureIsNotCachedWhenCancelled(t *testing.T) {
	f := newGCCSlotFixture(42)
	roots := &libraryRoots{reader: f.reader}
	source := libraryRoot{prefix: PIDPath(42, ProcFileRoot)}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if c := roots.closure(ctx, source, 42, gccExe); !c.known {
		t.Fatal("a configured resolver must report its result")
	}
	if len(roots.closures) != 0 {
		t.Fatalf("cached %d cancelled closure(s)", len(roots.closures))
	}
	if c := roots.closure(t.Context(), source, 42, ""); c.known || len(f.calls) != 1 {
		t.Fatalf("closure without an executable = %+v after %d call(s), want unknown and no resolver call", c, len(f.calls))
	}
}
