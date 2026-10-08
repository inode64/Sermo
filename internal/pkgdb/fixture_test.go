package pkgdb

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// writeFile creates rel (relative to root) with content, making parents.
func writeFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mkdir(t *testing.T, root, rel string) string {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
	return path
}

// symlink creates rel (relative to root) pointing at target, which is kept as
// given so a relative target resolves inside the fixture root. An existing
// link is left alone so fixtures can be stacked.
func symlink(t *testing.T, root, rel, target string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil && !errors.Is(err, fs.ErrExist) {
		t.Fatal(err)
	}
}

func chtimes(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func mtime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime()
}

// gentooFixture builds a /var/db/pkg tree with one category and one package
// owning a plain file, a symlink entry, a merged-usr file, a file whose path
// has a space, and a real symlink to an owned file.
func gentooFixture(t *testing.T, root string) {
	t.Helper()
	contents := strings.Join([]string{
		"dir /usr/bin",
		"obj /usr/bin/python3.12 0123456789abcdef0123456789abcdef 1700000000",
		"sym /usr/bin/vi -> vim 1700000000",
		"obj /bin/bash fedcba9876543210fedcba9876543210 1700000000",
		"obj /opt/My App/bin/run 00000000000000000000000000000000 1700000000",
		"obj /lib64/ld-linux-x86-64.so.2 11111111111111111111111111111111 1700000000",
		"",
	}, "\n")
	writeFile(t, root, "var/db/pkg/dev-lang/python-3.12.4/CONTENTS", contents)
	writeFile(t, root, "usr/bin/python3.12", "#!bin")
	symlink(t, root, "usr/bin/python", "python3.12")
	writeFile(t, root, "opt/rogue", "#!bin")
	mergedUsr(t, root)
}

// mergedUsr lays out a merged-usr root: /bin, /sbin and /lib64 are symlinks
// into /usr, as on every current Gentoo, Debian, Arch or Fedora host.
func mergedUsr(t *testing.T, root string) {
	t.Helper()
	for _, dir := range []string{"usr/bin", "usr/lib64"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	symlink(t, root, "bin", "usr/bin")
	symlink(t, root, "sbin", "usr/bin")
	symlink(t, root, "lib64", "usr/lib64")
}

func dpkgFixture(t *testing.T, root string) {
	t.Helper()
	writeFile(t, root, "var/lib/dpkg/status", "Package: bash\nStatus: install ok installed\n")
	writeFile(t, root, "var/lib/dpkg/info/bash.list", strings.Join([]string{
		"/.",
		"/bin",
		"/bin/bash",
		"/usr/share/man/man1/bash.1.gz",
		"",
	}, "\n"))
	writeFile(t, root, "var/lib/dpkg/info/python3.list", "/usr/bin/python3.12\n")
	writeFile(t, root, "var/lib/dpkg/info/bash.md5sums", "deadbeef  bin/bash\n")
	writeFile(t, root, "usr/bin/python3.12", "#!bin")
	symlink(t, root, "usr/bin/python3", "python3.12")
	mergedUsr(t, root)
}

func pacmanFixture(t *testing.T, root string) {
	t.Helper()
	writeFile(t, root, "var/lib/pacman/local/ALPM_DB_VERSION", "9\n")
	writeFile(t, root, "var/lib/pacman/local/bash-5.2.026-2/desc", "%NAME%\nbash\n\n")
	writeFile(t, root, "var/lib/pacman/local/bash-5.2.026-2/files", strings.Join([]string{
		"%FILES%",
		"usr/",
		"usr/bin/",
		"usr/bin/bash",
		"usr/bin/sh",
		"",
		"%BACKUP%",
		"etc/bash.bashrc\tdeadbeef",
		"",
	}, "\n"))
	writeFile(t, root, "var/lib/pacman/local/python-3.12.4-1/files", "%FILES%\nusr/bin/python3.12\n")
	writeFile(t, root, "usr/bin/python3.12", "#!bin")
	writeFile(t, root, "usr/bin/bash", "#!bin")
	symlink(t, root, "usr/bin/python", "python3.12")
	mergedUsr(t, root)
}

func apkFixture(t *testing.T, root string) {
	t.Helper()
	writeFile(t, root, "lib/apk/db/installed", strings.Join([]string{
		"C:Q1abc",
		"P:busybox",
		"V:1.36.1-r29",
		"F:bin",
		"R:busybox",
		"Z:Q1def",
		"R:sh",
		"a:0:0:777",
		"F:etc",
		"R:securetty",
		"",
		"P:python3",
		"F:usr/bin",
		"R:python3.12",
		"",
	}, "\n"))
	writeFile(t, root, "usr/bin/python3.12", "#!bin")
	symlink(t, root, "usr/bin/python3", "python3.12")
	mergedUsr(t, root)
}

func refreshed(t *testing.T, root string) *Index {
	t.Helper()
	x := New(Options{Root: root})
	if err := x.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return x
}

func expectOwnership(t *testing.T, x *Index, want map[string]Ownership) {
	t.Helper()
	for exe, own := range want {
		if got := x.Owned(t.Context(), exe); got != own {
			t.Errorf("Owned(%q) = %v, want %v", exe, got, own)
		}
	}
	got := x.OwnedAll(t.Context(), keys(want))
	if len(got) != len(want) {
		t.Fatalf("OwnedAll returned %d entries, want %d", len(got), len(want))
	}
	for exe, own := range want {
		if got[exe] != own {
			t.Errorf("OwnedAll[%q] = %v, want %v", exe, got[exe], own)
		}
	}
}

func keys(m map[string]Ownership) []string {
	return slices.Collect(maps.Keys(m))
}
