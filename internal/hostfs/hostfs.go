// Package hostfs is the door through which Sermo reads the host's files.
//
// ReadFile, ReadDir, Readlink, Stat, Lstat, Open and OpenFile read files Sermo
// locates by itself: procfs and sysfs tables, catalog and configuration files,
// pidfiles, runtime lock and log files. Every path must be absolute and clean.
// A relative or traversing path is rejected before the file system is touched.
// This is lexical validation, not confinement: any clean absolute path is
// accepted and symlinks are followed by the underlying os operations. Callers
// must authorize the target and validate untrusted components before joining
// them, since filepath.Join removes traversal components before check can see
// them. Operator-supplied paths (a `--config` argument) are resolved by the CLI
// before they reach this package.
//
// The *Target functions read paths Sermo does not determine itself: what the
// operator or the catalog names as an object to monitor or a candidate to probe
// (a check's file, a watched tree, a files_absent entry, a variable's candidate
// path) and what a verified process reports (a terminal session's socket).
// Those are read exactly as written, with plain os semantics, because their
// source owns their form; the name at the call site says which kind of path a
// read trusts.
package hostfs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrPath reports a path this package refuses to open.
var ErrPath = errors.New("hostfs: path must be absolute and clean")

// check reports whether path is absolute, clean and free of NUL bytes.
// It does not establish that path is trusted or contained in a directory.
func check(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
		return fmt.Errorf("%w: %q", ErrPath, path)
	}
	return nil
}

// checked runs op on path only once check accepts it, so every entry point
// refuses an unsafe path before the file system is touched. The os error is
// returned as is: it already names the operation and path, and callers add
// their own context.
func checked[T any](path string, op func(string) (T, error)) (T, error) { //nolint:ireturn // T is the concrete os result each entry point instantiates
	if err := check(path); err != nil {
		var zero T
		return zero, err
	}
	return op(path)
}

// The os functions below are hostfs's own door; everything else reads the host
// through it (G304: callers authorize host paths; check enforces their form).

// ReadFile reads the named host file.
func ReadFile(path string) ([]byte, error) {
	return checked(path, os.ReadFile) //nolint:forbidigo // hostfs is the checked door
}

// ReadDir reads the named host directory entries.
func ReadDir(path string) ([]os.DirEntry, error) {
	return checked(path, os.ReadDir) //nolint:forbidigo // hostfs is the checked door
}

// Readlink reads the target of one host symlink.
func Readlink(path string) (string, error) {
	return checked(path, os.Readlink) //nolint:forbidigo // hostfs is the checked door
}

// Stat describes the named host file, following a final symlink.
func Stat(path string) (os.FileInfo, error) {
	return checked(path, os.Stat) //nolint:forbidigo // hostfs is the checked door
}

// Lstat describes the named host file without following a final symlink.
func Lstat(path string) (os.FileInfo, error) {
	return checked(path, os.Lstat) //nolint:forbidigo // hostfs is the checked door
}

// Open opens the named host file or directory for reading.
func Open(path string) (*os.File, error) {
	return checked(path, os.Open) //nolint:forbidigo // hostfs is the checked door
}

// OpenFile opens the named host file with flag and perm, creating it when
// flag asks for it.
func OpenFile(path string, flag int, perm os.FileMode) (*os.File, error) {
	return checked(path, func(path string) (*os.File, error) {
		return os.OpenFile(path, flag, perm) //nolint:forbidigo,gosec // hostfs is the checked door; see checked
	})
}

// MaxPidfileBytes bounds a pidfile read. Only the first line is parsed;
// PostgreSQL's postmaster.pid, the longest common format, is a few hundred
// bytes.
const MaxPidfileBytes = 4096

// ReadRegularFile reads at most limit bytes of the named host file and refuses
// anything but a regular file. It opens without blocking and checks the opened
// descriptor, so a FIFO at a path another user controls, such as a daemon's
// pidfile, cannot stall the caller until a writer appears.
func ReadRegularFile(path string, limit int64) ([]byte, error) {
	fh, err := OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer func() { _ = fh.Close() }()
	info, err := fh.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(fh, limit))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return data, nil
}

// StatTarget describes an operator- or catalog-named path, read as written.
func StatTarget(path string) (os.FileInfo, error) {
	return os.Stat(path) //nolint:forbidigo,wrapcheck // the *Target door: the configuration owns this path's form
}

// LstatTarget describes an operator- or catalog-named path without following
// a final symlink, read as written.
func LstatTarget(path string) (os.FileInfo, error) {
	return os.Lstat(path) //nolint:forbidigo,wrapcheck // the *Target door: the configuration owns this path's form
}

// ReadFileTarget reads an operator- or catalog-named file, as written.
func ReadFileTarget(path string) ([]byte, error) {
	return os.ReadFile(path) //nolint:forbidigo,gosec,wrapcheck // the *Target door: G304, the configuration names this file
}

// ReadDirTarget reads an operator- or catalog-named directory, as written.
func ReadDirTarget(path string) ([]os.DirEntry, error) {
	return os.ReadDir(path) //nolint:forbidigo,wrapcheck // the *Target door: the configuration owns this path's form
}
