package logfile

import "testing"

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
