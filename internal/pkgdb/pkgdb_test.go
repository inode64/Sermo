package pkgdb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGentooBackend(t *testing.T) {
	root := t.TempDir()
	gentooFixture(t, root)
	x := refreshed(t, root)
	if got := x.Backend(); got != BackendGentoo {
		t.Fatalf("Backend = %q, want %q", got, BackendGentoo)
	}
	expectOwnership(t, x, map[string]Ownership{
		"/usr/bin/python3.12":             Owned,    // obj entry
		"/usr/bin/vi":                     Owned,    // sym entry
		"/usr/bin/python":                 Owned,    // real symlink to an owned file
		"/bin/bash":                       Owned,    // indexed as /bin/bash
		"/usr/bin/bash":                   Owned,    // merged-usr alias of /bin/bash
		"/lib64/ld-linux-x86-64.so.2":     Owned,    // indexed as /lib64
		"/usr/lib64/ld-linux-x86-64.so.2": Owned,    // merged-usr alias of /lib64
		"/opt/My App/bin/run":             Owned,    // path with a space
		"/usr/bin":                        NotOwned, // dir entries are not indexed
		"/opt/rogue":                      NotOwned, // exists on disk, no owner
		"/usr/sbin/nothing":               NotOwned, // absent
		"usr/bin/python3.12":              Unknown,  // not absolute
	})
}

func TestDpkgBackend(t *testing.T) {
	root := t.TempDir()
	dpkgFixture(t, root)
	x := refreshed(t, root)
	if got := x.Backend(); got != BackendDpkg {
		t.Fatalf("Backend = %q, want %q", got, BackendDpkg)
	}
	expectOwnership(t, x, map[string]Ownership{
		"/bin/bash":           Owned,    // listed
		"/usr/bin/bash":       Owned,    // merged-usr alias
		"/usr/bin/python3.12": Owned,    // second .list
		"/usr/bin/python3":    Owned,    // real symlink to an owned file
		"/":                   NotOwned, // "/." is skipped
		"/bin/bash.md5sums":   NotOwned, // only .list files are read
		"/usr/bin/rogue":      NotOwned,
	})
}

func TestPacmanBackend(t *testing.T) {
	root := t.TempDir()
	pacmanFixture(t, root)
	x := refreshed(t, root)
	if got := x.Backend(); got != BackendPacman {
		t.Fatalf("Backend = %q, want %q", got, BackendPacman)
	}
	expectOwnership(t, x, map[string]Ownership{
		"/usr/bin/bash":       Owned,    // %FILES% entry
		"/bin/bash":           Owned,    // resolved through the /bin -> usr/bin link
		"/usr/bin/sh":         Owned,    // pacman lists the symlink itself
		"/usr/bin/python3.12": Owned,    // second package
		"/usr/bin/python":     Owned,    // real symlink to an owned file
		"/usr/bin":            NotOwned, // directory entry "usr/bin/" skipped
		"/etc/bash.bashrc":    NotOwned, // %BACKUP% section is not files
		"/usr/bin/rogue":      NotOwned,
	})
}

func TestApkBackend(t *testing.T) {
	root := t.TempDir()
	apkFixture(t, root)
	x := refreshed(t, root)
	if got := x.Backend(); got != BackendApk {
		t.Fatalf("Backend = %q, want %q", got, BackendApk)
	}
	expectOwnership(t, x, map[string]Ownership{
		"/bin/busybox":        Owned,    // F:bin R:busybox
		"/usr/bin/busybox":    Owned,    // merged-usr alias
		"/bin/sh":             Owned,    // apk lists the symlink itself
		"/etc/securetty":      Owned,    // directory switch inside a record
		"/usr/bin/python3.12": Owned,    // second record
		"/usr/bin/python3":    Owned,    // real symlink to an owned file
		"/bin":                NotOwned, // directories are not files
		"/usr/bin/rogue":      NotOwned,
	})
}

func TestNoBackendAnswersUnknown(t *testing.T) {
	x := refreshed(t, t.TempDir())
	if got := x.Backend(); got != BackendNone {
		t.Fatalf("Backend = %q, want %q", got, BackendNone)
	}
	expectOwnership(t, x, map[string]Ownership{
		"/bin/bash":     Unknown,
		"/usr/bin/bash": Unknown,
	})
	fresh := New(Options{Root: t.TempDir()})
	if got := fresh.Backend(); got != BackendNone {
		t.Fatalf("Backend before Refresh = %q, want %q", got, BackendNone)
	}
	if got := fresh.Owned(t.Context(), "/bin/bash"); got != Unknown {
		t.Fatalf("Owned before Refresh = %v, want Unknown", got)
	}
}

func TestDetectionOrderPrefersGentoo(t *testing.T) {
	root := t.TempDir()
	dpkgFixture(t, root)
	gentooFixture(t, root)
	pacmanFixture(t, root)
	apkFixture(t, root)
	mkdir(t, root, rpmDBDir)
	x := refreshed(t, root)
	if got := x.Backend(); got != BackendGentoo {
		t.Fatalf("Backend = %q, want %q", got, BackendGentoo)
	}
	// Only the Gentoo database was indexed: the dpkg-only path is unowned.
	expectOwnership(t, x, map[string]Ownership{
		"/usr/share/man/man1/bash.1.gz": NotOwned,
		"/usr/bin/vi":                   Owned,
	})
}

func TestRefreshSkipsUnchangedMarker(t *testing.T) {
	root := t.TempDir()
	gentooFixture(t, root)
	x := refreshed(t, root)
	catDir := filepath.Join(root, gentooDBDir, "dev-lang")
	before := mtime(t, catDir)

	// A package added without touching the category mtime is invisible.
	writeFile(t, root, "var/db/pkg/dev-lang/perl-5.40.0/CONTENTS", "obj /usr/bin/perl 0 1700000000\n")
	chtimes(t, catDir, before)
	if err := x.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := x.Owned(t.Context(), "/usr/bin/perl"); got != NotOwned {
		t.Fatalf("Owned after unchanged marker = %v, want NotOwned (no rebuild)", got)
	}

	// Bumping the category mtime is noticed on the next Refresh.
	chtimes(t, catDir, before.Add(2*time.Second))
	if err := x.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := x.Owned(t.Context(), "/usr/bin/perl"); got != Owned {
		t.Fatalf("Owned after marker change = %v, want Owned", got)
	}
}

func TestRefreshNoticesNewCategory(t *testing.T) {
	root := t.TempDir()
	gentooFixture(t, root)
	x := refreshed(t, root)
	// A new category changes the /var/db/pkg mtime by itself.
	writeFile(t, root, "var/db/pkg/app-shells/zsh-5.9/CONTENTS", "obj /bin/zsh 0 1700000000\n")
	if err := x.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := x.Owned(t.Context(), "/usr/bin/zsh"); got != Owned {
		t.Fatalf("Owned(/usr/bin/zsh) = %v, want Owned", got)
	}
}

func TestRefreshKeepsPreviousIndexOnUnreadableDatabase(t *testing.T) {
	t.Run("gentoo CONTENTS is a directory", func(t *testing.T) {
		root := t.TempDir()
		gentooFixture(t, root)
		x := refreshed(t, root)
		mkdir(t, root, "var/db/pkg/app-shells/zsh-5.9/CONTENTS")
		err := x.Refresh(t.Context())
		if err == nil {
			t.Fatal("Refresh = nil, want error")
		}
		if got := x.Backend(); got != BackendGentoo {
			t.Fatalf("Backend after failed refresh = %q, want %q", got, BackendGentoo)
		}
		expectOwnership(t, x, map[string]Ownership{"/usr/bin/vi": Owned, "/bin/zsh": NotOwned})
		// The failure is not remembered as a successful build: it repeats.
		if err := x.Refresh(t.Context()); err == nil {
			t.Fatal("second Refresh = nil, want error again")
		}
	})
	t.Run("dpkg info directory is a file", func(t *testing.T) {
		root := t.TempDir()
		dpkgFixture(t, root)
		x := refreshed(t, root)
		if err := os.RemoveAll(filepath.Join(root, dpkgInfoDir)); err != nil {
			t.Fatal(err)
		}
		writeFile(t, root, dpkgInfoDir, "not a directory")
		chtimes(t, filepath.Join(root, dpkgStatusFile), time.Now().Add(time.Minute))
		if err := x.Refresh(t.Context()); err == nil {
			t.Fatal("Refresh = nil, want error")
		}
		if got := x.Backend(); got != BackendDpkg {
			t.Fatalf("Backend = %q, want %q", got, BackendDpkg)
		}
		expectOwnership(t, x, map[string]Ownership{"/bin/bash": Owned})
	})
	t.Run("first refresh fails leaves none", func(t *testing.T) {
		root := t.TempDir()
		mkdir(t, root, "var/db/pkg/app-shells/zsh-5.9/CONTENTS")
		x := New(Options{Root: root})
		if err := x.Refresh(t.Context()); err == nil {
			t.Fatal("Refresh = nil, want error")
		}
		if got := x.Backend(); got != BackendNone {
			t.Fatalf("Backend = %q, want %q", got, BackendNone)
		}
		if got := x.Owned(t.Context(), "/bin/zsh"); got != Unknown {
			t.Fatalf("Owned = %v, want Unknown", got)
		}
	})
}

func TestRefreshHonoursContext(t *testing.T) {
	root := t.TempDir()
	gentooFixture(t, root)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	x := New(Options{Root: root})
	err := x.Refresh(ctx)
	if !errors.Is(err, ErrCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Refresh = %v, want ErrCanceled wrapping context.Canceled", err)
	}
	if got := x.Backend(); got != BackendNone {
		t.Fatalf("Backend = %q, want %q", got, BackendNone)
	}
}

func TestBackendSwitchRebuilds(t *testing.T) {
	root := t.TempDir()
	dpkgFixture(t, root)
	x := refreshed(t, root)
	expectOwnership(t, x, map[string]Ownership{"/bin/bash": Owned})
	gentooFixture(t, root)
	if err := x.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := x.Backend(); got != BackendGentoo {
		t.Fatalf("Backend = %q, want %q", got, BackendGentoo)
	}
	expectOwnership(t, x, map[string]Ownership{
		"/usr/bin/vi":                   Owned,
		"/usr/share/man/man1/bash.1.gz": NotOwned,
	})
	// Removing every database drops to BackendNone and Unknown.
	if err := os.RemoveAll(filepath.Join(root, "var")); err != nil {
		t.Fatal(err)
	}
	if err := x.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := x.Backend(); got != BackendNone {
		t.Fatalf("Backend = %q, want %q", got, BackendNone)
	}
	expectOwnership(t, x, map[string]Ownership{"/usr/bin/vi": Unknown})
}

func TestSymlinkOutsideRootIsNotFound(t *testing.T) {
	root := t.TempDir()
	gentooFixture(t, root)
	outside := t.TempDir()
	target := filepath.Join(outside, "python3.12")
	if err := os.WriteFile(target, []byte("#!bin"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink(t, root, "usr/bin/escaped", target)
	x := refreshed(t, root)
	if got := x.Owned(t.Context(), "/usr/bin/escaped"); got != NotOwned {
		t.Fatalf("Owned(escaping symlink) = %v, want NotOwned", got)
	}
}

func TestOwnedConcurrently(t *testing.T) {
	root := t.TempDir()
	gentooFixture(t, root)
	x := refreshed(t, root)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			for range 50 {
				if got := x.Owned(t.Context(), "/usr/bin/vi"); got != Owned {
					t.Errorf("goroutine %d: Owned = %v, want Owned", i, got)
					return
				}
				_ = x.OwnedAll(t.Context(), []string{"/bin/bash", "/opt/rogue"})
				if i%4 == 0 {
					if err := x.Refresh(t.Context()); err != nil {
						t.Errorf("Refresh: %v", err)
						return
					}
				}
			}
		})
	}
	wg.Wait()
}

func TestOwnershipString(t *testing.T) {
	tests := map[Ownership]string{Unknown: "unknown", Owned: "owned", NotOwned: "not_owned", Ownership(42): "unknown"}
	for own, want := range tests {
		if got := own.String(); got != want {
			t.Errorf("Ownership(%d).String() = %q, want %q", int(own), got, want)
		}
	}
}

func TestGentooEntryPath(t *testing.T) {
	tests := []struct {
		line string
		want string
		ok   bool
	}{
		{line: "obj /usr/bin/python3.12 0123 1700000000", want: "/usr/bin/python3.12", ok: true},
		{line: "obj /opt/My App/bin/run 0123 1700000000", want: "/opt/My App/bin/run", ok: true},
		{line: "sym /usr/bin/vi -> vim 1700000000", want: "/usr/bin/vi", ok: true},
		{line: "sym /usr/bin/a -> b -> c 1700000000", want: "/usr/bin/a", ok: true},
		{line: "dir /usr/bin"},
		{line: "obj /usr/bin/short 0123"},
		{line: "obj  0123 1700000000"},
		{line: "sym /usr/bin/vi"},
		{line: ""},
		{line: "garbage"},
	}
	for _, tc := range tests {
		got, ok := gentooEntryPath(tc.line)
		if ok != tc.ok || got != tc.want {
			t.Errorf("gentooEntryPath(%q) = %q, %v; want %q, %v", tc.line, got, ok, tc.want, tc.ok)
		}
	}
}

// TestMergedSbinAliasFollowsHostSymlinks pins the Gentoo merged-usr layout
// where /usr/sbin and /sbin are symlinks to /usr/bin: the database records
// /usr/sbin/sshd while /proc/<pid>/exe resolves /usr/bin/sshd.
func TestMergedSbinAliasFollowsHostSymlinks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "var/db/pkg/net-misc/openssh-9.8/CONTENTS", strings.Join([]string{
		"obj /usr/sbin/sshd 45ca21826f38aa7c2a7cfc94f0516c63 1788244133",
		"sym /usr/sbin/glusterd -> glusterfsd 1786613751",
		"obj /sbin/mdadm 81fb3bf016cca5e66a9a3e5f27240eaf 1778480566",
		"",
	}, "\n"))
	writeFile(t, root, "usr/bin/sshd", "#!bin")
	symlink(t, root, "usr/sbin", "bin")
	symlink(t, root, "sbin", "usr/bin")
	x := refreshed(t, root)
	expectOwnership(t, x, map[string]Ownership{
		"/usr/bin/sshd":     Owned,    // /usr/sbin → bin on this host
		"/usr/sbin/sshd":    Owned,    // as recorded
		"/usr/bin/glusterd": Owned,    // sym entry under the linked directory
		"/usr/bin/mdadm":    Owned,    // /sbin → usr/bin on this host
		"/usr/bin/rogue":    NotOwned, // the alias never invents owners
	})

	// Without the symlinks the recorded spelling alone is owned; the kernel
	// would then never report the other one.
	plain := t.TempDir()
	writeFile(t, plain, "var/db/pkg/net-misc/openssh-9.8/CONTENTS", "obj /usr/sbin/sshd 45ca21826f38aa7c2a7cfc94f0516c63 1788244133\n")
	y := refreshed(t, plain)
	expectOwnership(t, y, map[string]Ownership{"/usr/sbin/sshd": Owned, "/usr/bin/sshd": NotOwned})
}

// TestGentooSkipsMergeInProgress pins that a package directory Portage is
// still merging (no CONTENTS yet) does not fail the refresh: the installed
// packages keep answering while the emerge runs.
func TestGentooSkipsMergeInProgress(t *testing.T) {
	root := t.TempDir()
	gentooFixture(t, root)
	writeFile(t, root, "var/db/pkg/dev-lang/-MERGING-python-3.13.0/environment.bz2", "")
	x := refreshed(t, root)
	expectOwnership(t, x, map[string]Ownership{"/usr/bin/python3.12": Owned, "/opt/rogue": NotOwned})
}
