// Package hostfs is the single door through which Sermo reads files it locates
// on the host by itself: procfs and sysfs tables, catalog and configuration
// files, pidfiles, runtime lock and log files.
//
// Every path must be absolute and clean. A relative or traversing path is
// rejected before the file system is touched. This is lexical validation,
// not confinement: any clean absolute path is accepted and symlinks are
// followed by the underlying os operations. Callers must authorize the target
// and validate untrusted components before joining them, since filepath.Join
// removes traversal components before Check can see them. Operator-supplied
// paths (a `--config` argument) are resolved by the CLI before they reach this
// package.
package hostfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrPath reports a path this package refuses to open.
var ErrPath = errors.New("hostfs: path must be absolute and clean")

// Check reports whether path is absolute, clean and free of NUL bytes.
// It does not establish that path is trusted or contained in a directory.
func Check(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
		return fmt.Errorf("%w: %q", ErrPath, path)
	}
	return nil
}

// ReadFile reads the named host file.
func ReadFile(path string) ([]byte, error) {
	if err := Check(path); err != nil {
		return nil, err
	}
	return os.ReadFile(path) //nolint:gosec,wrapcheck // G304: callers authorize host paths; Check enforces their lexical form.
	// wrapcheck: the os error already names the operation and path; callers add their own context.
}

// ReadDir reads the named host directory entries.
func ReadDir(path string) ([]os.DirEntry, error) {
	if err := Check(path); err != nil {
		return nil, err
	}
	return os.ReadDir(path) //nolint:wrapcheck // see ReadFile.
}

// Readlink reads the target of one host symlink.
func Readlink(path string) (string, error) {
	if err := Check(path); err != nil {
		return "", err
	}
	return os.Readlink(path) //nolint:wrapcheck // see ReadFile.
}

// Open opens the named host file or directory for reading.
func Open(path string) (*os.File, error) {
	if err := Check(path); err != nil {
		return nil, err
	}
	return os.Open(path) //nolint:gosec,wrapcheck // G304 and wrapcheck: see ReadFile.
}

// OpenFile opens the named host file with flag and perm, creating it when
// flag asks for it.
func OpenFile(path string, flag int, perm os.FileMode) (*os.File, error) {
	if err := Check(path); err != nil {
		return nil, err
	}
	return os.OpenFile(path, flag, perm) //nolint:gosec,wrapcheck // G304 and wrapcheck: see ReadFile.
}
