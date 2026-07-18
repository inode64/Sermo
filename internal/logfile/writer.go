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
	"sync"
	"time"
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
	now  func() time.Time
}

// Open creates parent directories as needed and opens path for append.
func Open(path string) (*Writer, error) {
	if path == "" {
		return nil, errors.New("log path is empty")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("log path %q must be absolute", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), logDirMode); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, logFileMode)
	if err != nil {
		return nil, fmt.Errorf("open log %q: %w", path, err)
	}
	return &Writer{path: path, f: f, now: time.Now}, nil
}

// Write marshals v as one JSON line and appends it to the log.
func (w *Writer) Write(v any) error {
	if w == nil || w.f == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal log record: %w", err)
	}
	data = append(data, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()
	_, err = w.f.Write(data)
	if err != nil {
		return fmt.Errorf("append log %q: %w", w.path, err)
	}
	return nil
}

// Close closes the underlying file.
func (w *Writer) Close() error {
	if w == nil || w.f == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	err := w.f.Close()
	w.f = nil
	if err != nil {
		return fmt.Errorf("close log %q: %w", w.path, err)
	}
	return nil
}
