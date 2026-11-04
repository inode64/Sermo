package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"sermo/internal/hostfs"
)

func TestLibraryRootAncestor(t *testing.T) {
	for _, name := range []string{"ancestor", "intermediate inode", "foreign root", "cancelled", "closed", "depth bound"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			identity, err := statLibraryFile("/")
			if err != nil {
				t.Fatal(err)
			}
			if name == "intermediate inode" {
				identity, err = statLibraryFile(root)
				if err != nil {
					t.Fatal(err)
				}
			}
			depth := 3
			if name == "depth bound" {
				depth = libraryRootAncestorLimit + 1
			}
			startPath := filepath.Join(root, strings.Repeat("child/", depth))
			if err := os.MkdirAll(startPath, 0o700); err != nil {
				t.Fatal(err)
			}
			start, err := hostfs.OpenFile(startPath, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			if name == "cancelled" {
				cancel()
			}
			if name == "closed" {
				_ = start.Close()
			}
			if name == "foreign root" {
				identity.identity = ExecutableFile{}
			}
			found, err := libraryRootAncestor(ctx, start, identity.identity)
			if name != "ancestor" {
				if err == nil || found != nil {
					t.Fatalf("unsafe root = %v, %v", found, err)
				}
				if name == "cancelled" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation = %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if err := verifyLibraryRootIdentity(found, identity.identity); err != nil {
					t.Fatal(err)
				}
				_ = found.Close()
			}
			if _, err := start.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("consumed descriptor is still open: %v", err)
			}
		})
	}
}

// procfs integration reads this test's own descriptors and namespace only.
func TestPinnedNamespaceRootOfOwnProcess(t *testing.T) {
	pid := os.Getpid()
	root, err := statLibraryFile(PIDPath(pid, ProcFileRoot))
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := statLibraryFile(PIDPath(pid, ProcFileMountNamespace))
	if err != nil {
		t.Fatal(err)
	}
	mountID, err := libraryDirectoryMountID(PIDPath(pid, ProcFileRoot))
	if err != nil {
		t.Fatal(err)
	}
	expected := libraryRootIdentity{process: root.identity, processMount: mountID, namespace: namespace.identity, host: root.identity}
	for _, name := range []string{"pinned", "root changed", "root mount changed", "namespace changed", "foreign root", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			want := expected
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			switch name {
			case "root changed":
				want.process.Inode++
			case "root mount changed":
				want.processMount++
			case "namespace changed":
				want.namespace.Inode++
			case "foreign root":
				want.host = ExecutableFile{}
			case "cancelled":
				cancel()
			}
			prefix, closeRoot, err := pinnedNamespaceRoot(ctx, pid, want)
			if name != "pinned" {
				if err == nil || prefix != "" || closeRoot != nil {
					t.Fatalf("unsafe pinned root = %q, %v", prefix, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			current, err := statLibraryFile(prefix)
			if err != nil || current.identity != root.identity {
				t.Fatalf("pinned root = %+v, %v", current, err)
			}
			closeRoot()
		})
	}
}

func TestScanLibrariesPinsNamespaceRoots(t *testing.T) {
	for _, name := range []string{"verified", "foreign root", "advisory fallback", "mount unreadable", "advisory mount unreadable", "namespace changed", "process root changed", "process root mount changed", "maps unreadable", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			r, proc := libraryFixture()
			statFile, readFile := r.stat, r.readFile
			const pinned = "/proc/self/fd/77"
			hostRoot := ExecutableFile{Device: 1, Inode: 1}
			processRoot := ExecutableFile{Device: 1, Inode: 9}
			namespace := ExecutableFile{Device: 4, Inode: 70}
			changed := false
			r.mountID = func(string) (uint64, error) {
				if name == "mount unreadable" || name == "advisory mount unreadable" {
					return 0, os.ErrPermission
				}
				if changed && name == "process root mount changed" {
					return 78, nil
				}
				return 77, nil
			}
			r.stat = func(path string) (libraryFile, error) {
				switch path {
				case PIDPath(proc.PID, ProcFileRoot):
					id := processRoot
					if changed && name == "process root changed" {
						id.Inode++
					}
					return libraryFile{identity: id}, nil
				case PIDPath(proc.PID, ProcFileMountNamespace):
					id := namespace
					if changed && name == "namespace changed" {
						id.Inode++
					}
					return libraryFile{identity: id}, nil
				case procSelfMountNamespace:
					if strings.HasPrefix(name, "advisory") {
						return libraryFile{identity: namespace}, nil
					}
					return libraryFile{identity: ExecutableFile{Device: 4, Inode: 1}}, nil
				case pinned:
					return libraryFile{identity: hostRoot}, nil
				default:
					if strings.HasSuffix(path, "/lib/libpam.so.0") && !strings.HasPrefix(name, "advisory") && !strings.HasPrefix(path, pinned+"/") {
						t.Fatalf("library lookup escaped its pinned namespace root: %s", path)
					}
					return statFile(path)
				}
			}
			opened, closed := 0, 0
			r.namespaceRoot = func(_ context.Context, pid int, expected libraryRootIdentity) (string, func(), error) {
				if pid != proc.PID || expected.process != processRoot || expected.host != hostRoot || expected.processMount != 77 {
					t.Fatalf("unverified root request: %d, %+v", pid, expected)
				}
				if name == "foreign root" || name == "advisory fallback" {
					return "", nil, os.ErrPermission
				}
				opened++
				return pinned, func() { closed++ }, nil
			}
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			r.readFile = func(path string) ([]byte, error) {
				if name == "namespace changed" || name == "process root changed" || name == "process root mount changed" {
					changed = true
				}
				if name == "cancelled" {
					cancel()
				}
				if name == "maps unreadable" {
					return nil, os.ErrPermission
				}
				return readFile(path)
			}
			report, err := r.scan(ctx, []Process{proc})
			wantErr := name != "verified" && !strings.HasPrefix(name, "advisory")
			if (err != nil) != wantErr {
				t.Fatalf("scan = %+v, %v", report, err)
			}
			if opened != closed {
				t.Fatalf("pinned roots leaked: opened %d, closed %d", opened, closed)
			}
			if !wantErr {
				if report.Processes != 1 || len(report.Libraries) != 1 {
					t.Fatalf("incomplete chroot inventory: %+v", report)
				}
				finding := report.Libraries[0]
				if !finding.ChrootedProcess || finding.NamespaceRootVerified != (name == "verified") {
					t.Fatalf("wrong root evidence: %+v", finding)
				}
			}
			if name == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
		})
	}
}

func TestScanLibrariesPinsMainIndependentlyOfSibling(t *testing.T) {
	for _, principal := range []int{0, 42} {
		t.Run(strconv.Itoa(principal), func(t *testing.T) {
			r, main := libraryFixture()
			r.principal = principal
			child := main
			child.PID, child.Role, child.Source = 41, RoleChild, SourceChild
			r.reader = fakeReader{ids: map[int]Identity{
				child.PID: {PID: child.PID, UID: child.UID, Exe: child.Exe, ExeOK: true, StartTicks: child.StartTicks, StartTicksOK: true},
				main.PID:  {PID: main.PID, UID: main.UID, Exe: main.Exe, ExeOK: true, StartTicks: main.StartTicks, StartTicksOK: true},
			}}
			statFile := r.stat
			hostRoot := ExecutableFile{Device: 1, Inode: 1}
			namespace := ExecutableFile{Device: 4, Inode: 70}
			r.stat = func(path string) (libraryFile, error) {
				for _, pid := range []int{child.PID, main.PID} {
					switch path {
					case PIDPath(pid, ProcFileRoot):
						return libraryFile{identity: ExecutableFile{Device: 1, Inode: uint64(pid)}}, nil
					case PIDPath(pid, ProcFileMountNamespace):
						return libraryFile{identity: namespace}, nil
					case "/proc/self/fd/" + strconv.Itoa(pid):
						return libraryFile{identity: hostRoot}, nil
					}
				}
				return statFile(path)
			}
			r.mountID = func(path string) (uint64, error) {
				if path == PIDPath(child.PID, ProcFileRoot) {
					return 77, nil
				}
				return 78, nil
			}
			opened, closed := 0, 0
			r.namespaceRoot = func(_ context.Context, pid int, expected libraryRootIdentity) (string, func(), error) {
				if expected.process.Inode != uint64(pid) || expected.namespace != namespace || expected.host != hostRoot {
					t.Fatalf("borrowed another process's root: %d, %+v", pid, expected)
				}
				opened++
				return "/proc/self/fd/" + strconv.Itoa(pid), func() { closed++ }, nil
			}
			report, err := r.scan(t.Context(), []Process{main, child})
			if err != nil || len(report.Libraries) != 2 {
				t.Fatalf("scan = %+v, %v", report, err)
			}
			wantPins := 1
			if principal == main.PID {
				wantPins = 2
			}
			if opened != wantPins || closed != opened {
				t.Fatalf("root pins = %d opened, %d closed; want %d", opened, closed, wantPins)
			}
			for _, finding := range report.Libraries {
				if finding.PID == main.PID && finding.NamespaceRootVerified != (principal == main.PID) {
					t.Fatalf("main borrowed sibling's root proof: %+v", finding)
				}
			}
		})
	}
}
