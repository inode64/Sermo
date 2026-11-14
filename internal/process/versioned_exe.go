package process

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"sermo/internal/strutil"
	"strings"
)

// Some packages install one versioned executable per release and point a
// stable name at it (Gentoo: /usr/bin/rspamd -> rspamd-4.2.3). An upgrade
// installs the new file, repoints the link and deletes the old one, so the
// running daemon keeps the previous version's path while a selector naming the
// link now resolves to the new one, or, when the configuration was loaded
// before the upgrade, still holds the old resolution. Either way the daemon
// and its replacement stop matching their own selector, and neither the
// replaced-binary evidence a stop relies on nor the identity a start verifies
// is found.

// selectorNamesExe reports whether exe is the executable the selector names
// now. The resolution cached when the configuration was loaded is exact; when
// it no longer matches, a versioned sibling counts only if the link resolves to
// it when read again, which is what a configuration reload would cache.
func selectorNamesExe(sel *Selector, exe string) bool {
	resolved := selectorExePath(sel)
	if exe == resolved {
		return true
	}
	return versionedSibling(sel, resolved, exe) && canonicalizePath(sel.Exe) == exe
}

// selectorNamedReplacedExe reports whether prev, the path of a deleted
// executable, is the selector's program: its exact resolution, or another
// version of the file its link points to. Callers apply it only to an
// executable the kernel reports as deleted, and it grants no signal on its own:
// cleanup still verifies the kernel-held file and the process ownership.
func selectorNamedReplacedExe(sel *Selector, prev string) bool {
	resolved := selectorExePath(sel)
	return prev == resolved || versionedSibling(sel, resolved, prev)
}

// versionedSibling reports whether path is another version of the file the
// selector's configured name links to: the configured name resolves to a file
// that is that name plus a version, and path is the same name plus a different
// version in the same directory. A configured name that is not a link, or a
// path elsewhere, is never a sibling.
func versionedSibling(sel *Selector, resolved, path string) bool {
	configured := filepath.Clean(sel.Exe)
	if resolved == configured || path == resolved || filepath.Dir(path) != filepath.Dir(resolved) {
		return false
	}
	name := filepath.Base(configured)
	return hasVersionSuffix(filepath.Base(resolved), name) && hasVersionSuffix(filepath.Base(path), name)
}

// hasVersionSuffix reports whether file is name followed by a version: an
// optional separator (-, _ or .) and then a digit, as in rspamd-4.2.3,
// screen-4.9.1 or php8.3. A companion tool such as rspamd_stats is not one.
func hasVersionSuffix(file, name string) bool {
	rest, ok := strings.CutPrefix(file, name)
	if !ok || rest == "" {
		return false
	}
	if strings.ContainsRune("-_.", rune(rest[0])) {
		rest = rest[1:]
	}
	return rest != "" && rest[0] >= '0' && rest[0] <= '9'
}

// versionedFileName splits a file name that ends in a version into the program
// name and that version: python3.13 into python and 3.13, rspamd-4.1.1 into
// rspamd and 4.1.1. The name must end in a character that is neither a digit
// nor a separator, so python3.14-config or rspamd_stats carry no version.
var versionedFileName = regexp.MustCompile(`^(.*[^0-9._-])[-_.]?(\d+(?:\.\d+)*)$`)

// NewerVersionBeside reports whether the directory that held path, read with
// readDir, holds the same program with a higher version. A deleted versioned
// executable with a newer one beside it was upgraded, not uninstalled: an
// interpreter upgrade removes python3.13 once python3.14 is installed, while
// the daemons it ran stay installed and start again on the new one. A lower or
// equal version is not an upgrade; a name without a version is never listed,
// and a listing that fails proves nothing.
func NewerVersionBeside(path string, readDir func(string) ([]fs.DirEntry, error)) bool {
	name, version, ok := splitVersionedFileName(filepath.Base(path))
	if !ok {
		return false
	}
	entries, err := readDir(filepath.Dir(path))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if other, otherVersion, ok := splitVersionedFileName(entry.Name()); ok && other == name && strutil.CompareVersions(otherVersion, version) > 0 {
			return true
		}
	}
	return false
}

func splitVersionedFileName(file string) (name, version string, ok bool) {
	m := versionedFileName.FindStringSubmatch(file)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}
