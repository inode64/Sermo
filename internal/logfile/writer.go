// Package logfile provides append-only JSON Lines writers for Sermo audit and
// export logs configured under engine.access, engine.events and
// engine.diagnostics.
package logfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sermo/internal/hostfs"
	"sync"
)

const (
	logDirMode  = 0o750
	logFileMode = 0o640
)

// Writer appends one JSON-encoded record per line to a log file.
type Writer struct {
	path string
	f    *os.File
	mu   sync.Mutex
}

// Open creates parent directories as needed and opens path for append.
func Open(path string) (*Writer, error) {
	if path == "" {
		return nil, errors.New("log path is empty")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("log path %q must be absolute", path)
	}
	f, err := openAppend(path)
	if err != nil {
		return nil, err
	}
	return &Writer{path: path, f: f}, nil
}

func openAppend(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), logDirMode); err != nil {
		return nil, fmt.Errorf("create log directory for %q: %w", path, err)
	}
	f, err := hostfs.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, logFileMode)
	if err != nil {
		return nil, fmt.Errorf("open log %q: %w", path, err)
	}
	return f, nil
}

// Reopen replaces the open file with a fresh open of the configured path, so a
// log rotated by rename (logrotate's default create mode) is followed instead
// of writing into the renamed or deleted inode forever. The new file is opened
// before the old one is closed: on failure the writer keeps appending to its
// previous handle rather than dropping records. A nil or closed writer stays
// as it is.
func (w *Writer) Reopen() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	f, err := openAppend(w.path)
	if err != nil {
		return fmt.Errorf("reopen: %w", err)
	}
	old := w.f
	w.f = f
	if err := old.Close(); err != nil {
		return fmt.Errorf("close rotated log %q: %w", w.path, err)
	}
	return nil
}

// Write marshals v as one JSON line and appends it to the log.
func (w *Writer) Write(v any) error {
	if w == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal log record: %w", err)
	}
	data = append(data, '\n')

	// The file is checked under the lock: Close may run concurrently (daemon
	// shutdown), and a record arriving after it is dropped like one written to
	// a writer that never opened.
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	_, err = w.f.Write(data)
	if err != nil {
		return fmt.Errorf("append log %q: %w", w.path, err)
	}
	return nil
}

// Close closes the underlying file.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	if err != nil {
		return fmt.Errorf("close log %q: %w", w.path, err)
	}
	return nil
}
