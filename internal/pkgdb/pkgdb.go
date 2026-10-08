// Package pkgdb indexes the files owned by installed packages so a host watch
// can tell whether a running process's executable belongs to any package.
//
// Native parsers cover the Gentoo (/var/db/pkg), dpkg, pacman and apk
// databases. The rpm database has no stable on-disk format Sermo parses, so
// that backend asks `rpm -qf` through the execx runner and caches the answers.
//
// Absence of a database is never a violation: without one every lookup answers
// Unknown, never NotOwned.
package pkgdb

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"sermo/internal/execx"
	"sermo/internal/hostfs"
)

// Backend names the package database an Index reads.
type Backend string

// Package databases Sermo recognises, in detection order.
const (
	BackendNone   Backend = "none"
	BackendGentoo Backend = "gentoo"
	BackendDpkg   Backend = "dpkg"
	BackendPacman Backend = "pacman"
	BackendApk    Backend = "apk"
	BackendRPM    Backend = "rpm"
)

// Ownership is the answer to "does an installed package own this file?".
type Ownership int

// Ownership answers. Unknown is the zero value: no database, a lookup that
// could not run, or a path the index cannot resolve.
const (
	Unknown Ownership = iota
	Owned
	NotOwned
)

// String returns the operator-facing name of the answer.
func (o Ownership) String() string {
	names := map[Ownership]string{Owned: "owned", NotOwned: "not_owned"}
	if name, ok := names[o]; ok {
		return name
	}
	return "unknown"
}

// Options configures an Index.
type Options struct {
	// Root is the filesystem root the databases live under; "" means "/".
	// Tests pass a t.TempDir().
	Root string
	// Runner executes `rpm -qf` for the rpm backend; nil makes rpm lookups
	// answer Unknown. The native backends never run a command.
	Runner execx.Runner
	// Timeout bounds one rpm invocation; 0 means defaultRPMTimeout.
	Timeout time.Duration
	// Now is the clock the rpm answer cache ages against; nil means time.Now.
	Now func() time.Time
}

const (
	defaultRoot       = "/"
	defaultRPMTimeout = 10 * time.Second

	gentooDBDir      = "var/db/pkg"
	gentooContents   = "CONTENTS"
	dpkgStatusFile   = "var/lib/dpkg/status"
	dpkgInfoDir      = "var/lib/dpkg/info"
	dpkgListSuffix   = ".list"
	pacmanLocalDir   = "var/lib/pacman/local"
	pacmanFilesName  = "files"
	apkInstalledFile = "lib/apk/db/installed"
	rpmDBDir         = "var/lib/rpm"
)

// ErrCanceled wraps the context error that interrupted a Refresh.
var ErrCanceled = errors.New("pkgdb: refresh interrupted")

// Index answers file-ownership questions from one detected package database.
// The zero value is not usable; call New.
type Index struct {
	root    string
	runner  execx.Runner
	timeout time.Duration
	now     func() time.Time

	// refreshMu serialises Refresh so concurrent callers never build twice.
	refreshMu sync.Mutex

	// mu guards the published state below. paths is replaced as a whole on
	// every rebuild and never mutated afterwards, so a reader may keep using
	// the map it took under the lock.
	mu       sync.RWMutex
	backend  Backend
	marker   string
	gen      uint64
	paths    pathSet
	rpmCache map[string]rpmEntry
}

// New returns an Index that has detected nothing yet; call Refresh first.
func New(opts Options) *Index {
	root := opts.Root
	if root == "" {
		root = defaultRoot
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultRPMTimeout
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Index{
		root:     filepath.Clean(root),
		runner:   opts.Runner,
		timeout:  timeout,
		now:      now,
		backend:  BackendNone,
		rpmCache: map[string]rpmEntry{},
	}
}

// Backend reports the database detected by the last Refresh, BackendNone
// before any.
func (x *Index) Backend() Backend {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.backend
}

// Refresh detects the package database and rebuilds the in-memory index when
// the database changed since the last build or the backend changed. It is
// cheap when nothing changed: only the marker paths are stat'ed. It returns
// an error only when a present database could not be read; the index then
// keeps its previous content and Backend, and the next Refresh retries.
func (x *Index) Refresh(ctx context.Context) error {
	x.refreshMu.Lock()
	defer x.refreshMu.Unlock()

	backend, marker, err := x.detect()
	if err != nil {
		return err
	}
	x.mu.RLock()
	unchanged := backend == x.backend && marker == x.marker
	x.mu.RUnlock()
	if unchanged {
		return nil
	}
	paths, err := x.build(ctx, backend)
	if err != nil {
		return err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.backend = backend
	x.marker = marker
	x.paths = paths
	x.rpmCache = map[string]rpmEntry{}
	x.gen++
	return nil
}

// Owned reports whether exe, a clean absolute path as /proc/<pid>/exe resolves
// it, belongs to an installed package. Safe for concurrent use.
func (x *Index) Owned(ctx context.Context, exe string) Ownership {
	return x.OwnedAll(ctx, []string{exe})[exe]
}

// OwnedAll answers for several paths at once. The map has one entry per input
// path. For the rpm backend it runs one `rpm -qf` for every path that is not
// already cached; the native backends never run a command.
func (x *Index) OwnedAll(ctx context.Context, exes []string) map[string]Ownership {
	out := make(map[string]Ownership, len(exes))
	for _, exe := range exes {
		out[exe] = Unknown
	}
	x.mu.RLock()
	backend, paths := x.backend, x.paths
	x.mu.RUnlock()
	switch backend {
	case BackendGentoo, BackendDpkg, BackendPacman, BackendApk:
		for _, exe := range exes {
			out[exe] = x.lookupNative(paths, exe)
		}
	case BackendRPM:
		x.ownedRPM(ctx, exes, out)
	case BackendNone:
	}
	return out
}

// lookupNative tests the cleaned path, then the symlink-resolved path under
// Root: a package may own the symlink or its target. Any resolution error
// treats that variant as not found.
func (x *Index) lookupNative(paths pathSet, exe string) Ownership {
	p := filepath.Clean(exe)
	if !filepath.IsAbs(p) {
		return Unknown
	}
	if paths.has(p) {
		return Owned
	}
	if target, ok := x.resolve(p); ok && target != p && paths.has(target) {
		return Owned
	}
	return NotOwned
}

// resolve evaluates the symlinks of p under Root and returns the result as a
// path relative to Root again. A target outside Root is not found.
func (x *Index) resolve(p string) (string, bool) {
	resolved, err := filepath.EvalSymlinks(filepath.Join(x.root, p))
	if err != nil {
		return "", false
	}
	if x.root == defaultRoot {
		return resolved, true
	}
	rel, ok := strings.CutPrefix(resolved, x.root+"/")
	if !ok {
		return "", false
	}
	return "/" + rel, true
}

// join returns the absolute, clean host path of a database file under Root.
func (x *Index) join(rel string) string {
	return filepath.Join(x.root, rel)
}

// detect finds the first database marker present under Root and returns the
// backend with its change fingerprint. A marker that exists but cannot be
// inspected is an error; a missing one is skipped.
func (x *Index) detect() (Backend, string, error) {
	markers := []struct {
		backend Backend
		path    string
		dir     bool
	}{
		{backend: BackendGentoo, path: gentooDBDir, dir: true},
		{backend: BackendDpkg, path: dpkgStatusFile},
		{backend: BackendPacman, path: pacmanLocalDir, dir: true},
		{backend: BackendApk, path: apkInstalledFile},
		{backend: BackendRPM, path: rpmDBDir, dir: true},
	}
	for _, m := range markers {
		path := x.join(m.path)
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
				continue
			}
			return BackendNone, "", fmt.Errorf("pkgdb: %s database marker %s: %w", m.backend, path, err)
		}
		if info.IsDir() != m.dir {
			continue
		}
		marker := mtimeStamp(info)
		if m.backend == BackendGentoo {
			marker, err = gentooFingerprint(path, info)
			if err != nil {
				return BackendNone, "", err
			}
		}
		return m.backend, marker, nil
	}
	return BackendNone, "", nil
}

// gentooFingerprint folds the mtime of /var/db/pkg (categories added or
// removed) with the mtime of every category directory (a package of an
// existing category rebuilt, installed or removed).
func gentooFingerprint(dbDir string, info fs.FileInfo) (string, error) {
	entries, err := hostfs.ReadDir(dbDir)
	if err != nil {
		return "", fmt.Errorf("pkgdb: gentoo: %w", err)
	}
	var b strings.Builder
	b.WriteString(mtimeStamp(info))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		catInfo, err := e.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return "", fmt.Errorf("pkgdb: gentoo: category %s: %w", e.Name(), err)
		}
		b.WriteByte(' ')
		b.WriteString(e.Name())
		b.WriteByte('=')
		b.WriteString(mtimeStamp(catInfo))
	}
	return b.String(), nil
}

func mtimeStamp(info fs.FileInfo) string {
	return strconv.FormatInt(info.ModTime().UnixNano(), 10)
}

// build parses the database of a native backend into a fresh set. The rpm
// backend and BackendNone have nothing to parse and get an empty set.
func (x *Index) build(ctx context.Context, backend Backend) (pathSet, error) {
	paths := newPathSet(x.dirAliases())
	var err error
	switch backend {
	case BackendGentoo:
		err = x.buildGentoo(ctx, paths)
	case BackendDpkg:
		err = x.buildDpkg(ctx, paths)
	case BackendPacman:
		err = x.buildPacman(ctx, paths)
	case BackendApk:
		err = x.buildApk(paths)
	case BackendNone, BackendRPM:
	}
	if err != nil {
		return pathSet{}, err
	}
	return paths, nil
}

// dirAliases resolves which merged directories are symlinks on this host, so
// a database entry such as /usr/sbin/sshd also answers for /usr/bin/sshd, the
// path /proc/<pid>/exe reports when /usr/sbin links to bin.
func (x *Index) dirAliases() []dirAlias {
	var aliases []dirAlias
	for _, dir := range mergedDirs {
		if target, ok := x.resolve(dir); ok && target != dir {
			aliases = append(aliases, dirAlias{from: dir, to: target})
		}
	}
	return aliases
}

// interrupted wraps a context error so a caller can tell a cancelled refresh
// from an unreadable database.
func interrupted(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrCanceled, err)
	}
	return nil
}
