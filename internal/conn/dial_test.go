package conn

import (
	"errors"
	"io"
	"strings"
	"testing"
)

type failingReadWriter struct {
	err error
}

func (f failingReadWriter) Read([]byte) (int, error)    { return 0, f.err }
func (f failingReadWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestReadTextGreetingWrapsReadError(t *testing.T) {
	wantErr := errors.New("greeting unavailable")
	_, _, _, err := readTextGreeting(failingReadWriter{err: wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("readTextGreeting() error = %v, want wrapped %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "read text greeting") {
		t.Fatalf("readTextGreeting() error = %q, want greeting context", err)
	}
}

var _ io.ReadWriter = failingReadWriter{}
