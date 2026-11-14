package process

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// versionedInstall lays out a package that installs one executable per release
// behind a stable link (/usr/bin/rspamd -> rspamd-4.1.1) and returns the
// directory and a function that upgrades it the way the package manager does:
// install the new file, repoint the link, delete the old one.
func versionedInstall(t *testing.T) (string, func()) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/true\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("rspamd-4.1.1")
	write("rspamd_stats")
	if err := os.Symlink("rspamd-4.1.1", filepath.Join(dir, "rspamd")); err != nil {
		t.Fatal(err)
	}
	upgrade := func() {
		write("rspamd-4.2.3")
		link := filepath.Join(dir, "rspamd")
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("rspamd-4.2.3", link); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(dir, "rspamd-4.1.1")); err != nil {
			t.Fatal(err)
		}
	}
	return dir, upgrade
}

func parsedSelector(t *testing.T, exe, user string) Selector {
	t.Helper()
	sel := staleSelector(RoleMain, exe, user)
	sel.exePath = canonicalizePath(sel.Exe)
	return sel
}

// After an upgrade the old daemon runs the deleted previous version and its
// replacement the new one. Both must read as the service's program whether
// the selector was resolved before the upgrade (a daemon that was not reloaded)
// or after it (a reload), so a restart can stop the old daemon through the
// backend and verify the new one.
func TestVersionedLinkFollowsAnUpgrade(t *testing.T) {
	t.Parallel()
	dir, upgrade := versionedInstall(t)
	link := filepath.Join(dir, "rspamd")
	before := parsedSelector(t, link, "root")
	upgrade()
	after := parsedSelector(t, link, "root")

	d := Discoverer{
		Reader: fakeReader{ids: map[int]Identity{
			10: {PID: 10, UID: 0, PPID: 1, ExePrev: filepath.Join(dir, "rspamd-4.1.1")},
			11: {PID: 11, UID: 0, PPID: 1, Exe: filepath.Join(dir, "rspamd-4.2.3"), ExeOK: true},
		}},
		ResolveUser: fakeUsers(map[string]uint32{"root": 0}),
	}
	resolve := d.resolveUser()
	for name, sel := range map[string]Selector{"loaded before the upgrade": before, "reloaded after it": after} {
		if !d.matchesDeletedExe(&sel, fakeReaderIdentity(d, 10), resolve) {
			t.Fatalf("%s: the deleted previous version is not the service's replaced binary", name)
		}
		if d.matches(&sel, fakeReaderIdentity(d, 10), resolve) {
			t.Fatalf("%s: a deleted executable must never match", name)
		}
		if !d.matches(&sel, fakeReaderIdentity(d, 11), resolve) {
			t.Fatalf("%s: the new version the link points to does not match", name)
		}
	}

	// The operation's judgement: the old daemon alone is replaced, not unverified.
	only := Discoverer{Reader: fakeReader{ids: map[int]Identity{
		10: {PID: 10, UID: 0, PPID: 1, ExePrev: filepath.Join(dir, "rspamd-4.1.1")},
	}}, ResolveUser: d.ResolveUser, BackendPIDs: func() []int { return []int{10} }}
	out, err := only.Observe([]Selector{after})
	if err != nil {
		t.Fatal(err)
	}
	if p := out.Principal(); !p.ReplacedExecutable || p.Trusted || p.Unverified() {
		t.Fatalf("principal = %+v, want replaced and not unverified", p)
	}
}

// Naming evidence only extends a selector whose name is a link to a versioned
// file, only to its own directory and to the same name plus a version, and
// never to a live file the link does not point to or to another user.
func TestVersionedSiblingStaysNarrow(t *testing.T) {
	t.Parallel()
	dir, upgrade := versionedInstall(t)
	upgrade()
	if err := os.WriteFile(filepath.Join(dir, "rspamd-4.1.0"), []byte("#!/bin/true\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "rspamd")
	other := t.TempDir()
	d := Discoverer{
		Reader: fakeReader{ids: map[int]Identity{
			20: {PID: 20, UID: 0, ExePrev: filepath.Join(other, "rspamd-4.1.1")},
			21: {PID: 21, UID: 0, ExePrev: filepath.Join(dir, "rspamd_stats")},
			22: {PID: 22, UID: 0, ExePrev: filepath.Join(dir, "rspamdx-4.1.1")},
			23: {PID: 23, UID: 0, Exe: filepath.Join(dir, "rspamd-4.1.0"), ExeOK: true},
			24: {PID: 24, UID: 33, ExePrev: filepath.Join(dir, "rspamd-4.1.1")},
			25: {PID: 25, UID: 0, ExePrev: filepath.Join(dir, "rspamd-4.1.1")},
		}},
		ResolveUser: fakeUsers(map[string]uint32{"root": 0, "www": 33}),
	}
	resolve := d.resolveUser()
	sel := parsedSelector(t, link, "root")
	for _, pid := range []int{20, 21, 22, 24} {
		if d.matchesDeletedExe(&sel, fakeReaderIdentity(d, pid), resolve) {
			t.Fatalf("pid %d: %+v must not read as the service's replaced binary", pid, fakeReaderIdentity(d, pid))
		}
	}
	if d.matches(&sel, fakeReaderIdentity(d, 23), resolve) {
		t.Fatal("an installed version the link does not point to is not the service's program")
	}
	// The selector names the versioned file itself, not a link: exact only.
	direct := parsedSelector(t, filepath.Join(dir, "rspamd-4.2.3"), "root")
	if d.matchesDeletedExe(&direct, fakeReaderIdentity(d, 25), resolve) {
		t.Fatal("a selector without a link must keep its exact path")
	}
}

func TestHasVersionSuffix(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		file, name string
		want       bool
	}{
		{"rspamd-4.2.3", "rspamd", true},
		{"screen-4.9.1", "screen", true},
		{"php8.3", "php", true},
		{"db_5.3", "db", true},
		{"python3.13", "python3", true},
		{"rspamd", "rspamd", false},
		{"rspamd_stats", "rspamd", false},
		{"rspamd-", "rspamd", false},
		{"rspamdx-4.1", "rspamd", false},
		{"rspamd-beta", "rspamd", false},
	} {
		if got := hasVersionSuffix(tc.file, tc.name); got != tc.want {
			t.Errorf("hasVersionSuffix(%q, %q) = %v, want %v", tc.file, tc.name, got, tc.want)
		}
	}
}

// An interpreter upgrade deletes the old versioned file once the new one is
// installed; only a higher version of the same program counts as that upgrade.
func TestNewerVersionBeside(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"python3", "python3.14", "python3.14-config", "python3.12", "rspamd-4.2.3", "rspamd_stats", "apache2ctl"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	listings := 0
	readDir := func(path string) ([]fs.DirEntry, error) {
		listings++
		return os.ReadDir(path)
	}
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/usr/bin/python3.13", true},
		{"/usr/bin/python3.14", false},
		{"/usr/bin/python3.15", false},
		{"/usr/bin/rspamd-4.1.1", true},
		{"/usr/bin/rspamd-4.2.3", false},
		{"/usr/bin/apache2", false},
		{"/usr/bin/python3.9", true},
		{"/usr/bin/rspamd", false},
	} {
		path := filepath.Join(dir, filepath.Base(tc.path))
		if got := NewerVersionBeside(path, readDir); got != tc.want {
			t.Errorf("NewerVersionBeside(%q) = %v, want %v", path, got, tc.want)
		}
	}
	// apache2 reads as version 2 of apache, so it is listed too; only rspamd,
	// which carries no version, is not.
	if listings != 7 {
		t.Errorf("directory listings = %d, want one per versioned name and none for rspamd", listings)
	}
	companion := t.TempDir()
	if err := os.WriteFile(filepath.Join(companion, "python3.10-config"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if NewerVersionBeside(filepath.Join(companion, "python3.9"), os.ReadDir) {
		t.Error("a companion tool with a version inside its name counted as the upgrade")
	}
	if NewerVersionBeside(filepath.Join(dir, "python3.13"), func(string) ([]fs.DirEntry, error) { return nil, os.ErrPermission }) {
		t.Error("a directory that cannot be listed counted as proof of an upgrade")
	}
}
