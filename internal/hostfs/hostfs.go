// Package hostfs is the single door through which Sermo reads files it locates
// on the host by itself: procfs and sysfs tables, catalog and configuration
// files, pidfiles, runtime lock and log files.
//
// Every path must be absolute and clean. A relative or traversing path is
// rejected before the file system is touched, so a path assembled from a
// device name, a PID or a configured directory can never escape the location
// the caller reasoned about. Operator-supplied paths (a `--config` argument)
// are resolved by the CLI before they reach this package.
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
	return os.ReadFile(path) //nolint:gosec,wrapcheck // G304: every host file read passes Check above; this is the single audited read path.
	// wrapcheck: the os error already names the operation and path; callers add their own context.
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
