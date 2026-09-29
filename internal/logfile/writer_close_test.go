package logfile

import (
	"path/filepath"
	"sync"
	"testing"
)

// Write racing Close (sermod closes its logs on shutdown while workers still
// record) must neither race on the file handle nor report a spurious append
// error for a record that arrives after Close. Run with -race.
func TestWriterWriteConcurrentWithClose(t *testing.T) {
	w, err := Open(filepath.Join(t.TempDir(), "events.log"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for range 100 {
		wg.Go(func() {
			if err := w.Write(map[string]string{"event": "x"}); err != nil {
				errs <- err
			}
		})
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("write during close: %v", err)
	}
	if err := w.Write(map[string]string{"event": "late"}); err != nil {
		t.Fatalf("write after close = %v, want the record dropped silently", err)
	}
}

// Close must be safe on a nil *Writer and on a Writer with no open file: both
// short-circuit to nil. Either guard, if inverted, would nil-dereference.
func TestWriterCloseNilSafe(t *testing.T) {
	var w *Writer
	if err := w.Close(); err != nil {
		t.Errorf("(*Writer)(nil).Close() = %v, want nil", err)
	}

	empty := &Writer{} // f is nil
	if err := empty.Close(); err != nil {
		t.Errorf("Writer{nil file}.Close() = %v, want nil", err)
	}
}
