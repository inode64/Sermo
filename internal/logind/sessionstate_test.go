package logind

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"testing"
)

func TestSessionStateReadsLogindRecord(t *testing.T) {
	t.Parallel()
	records := map[string]string{
		"/run/systemd/sessions/3": "# This is private data. Do not parse.\nUID=0\nSTATE=active\nSCOPE=session-3.scope\n",
		"/run/systemd/sessions/7": "UID=0\nSTATE=closing\n",
		"/run/systemd/sessions/9": "UID=0\n",
	}
	readFile := func(path string) ([]byte, error) {
		if data, ok := records[path]; ok {
			return []byte(data), nil
		}
		return nil, fs.ErrNotExist
	}
	for _, tc := range []struct {
		id, state string
		found     bool
		closed    bool
	}{
		{"3", "active", true, false},
		{"7", "closing", true, true},
		{"9", "", true, false},
		{"42", "", false, true},
		{"", "", false, true},
		{"../etc/passwd", "", false, true},
	} {
		state, found := sessionState(readFile, "", tc.id)
		if state != tc.state || found != tc.found {
			t.Fatalf("sessionState(%q) = %q,%v want %q,%v", tc.id, state, found, tc.state, tc.found)
		}
		if closed := SessionClosed(readFile, "", tc.id); closed != tc.closed {
			t.Fatalf("SessionClosed(%q) = %v want %v", tc.id, closed, tc.closed)
		}
	}
}

func TestSessionsReadable(t *testing.T) {
	t.Parallel()
	present := func(string) ([]os.DirEntry, error) { return nil, nil }
	missing := func(string) ([]os.DirEntry, error) { return nil, fs.ErrNotExist }
	if !SessionsReadable(present, "") || SessionsReadable(missing, "/custom") {
		t.Fatal("SessionsReadable must follow the directory listing")
	}
}

func TestSessionStateUnreadableRecordIsNotClosed(t *testing.T) {
	t.Parallel()
	for name, err := range map[string]error{
		"permission": errors.New("permission denied"),
		"wrapped io": fmt.Errorf("read record: %w", errors.New("input/output error")),
	} {
		readFile := func(string) ([]byte, error) { return nil, err }
		if state, found := sessionState(readFile, "/custom", "3"); state != "" || !found {
			t.Fatalf("%s: sessionState = %q,%v want \"\",true", name, state, found)
		}
		if SessionClosed(readFile, "/custom", "3") {
			t.Fatalf("%s: a record the reader cannot read must never be called closed", name)
		}
	}
	readFileMissing := func(string) ([]byte, error) { return nil, fmt.Errorf("open: %w", fs.ErrNotExist) }
	if !SessionClosed(readFileMissing, "/custom", "3") {
		t.Fatal("a missing record (even wrapped) is a session logind forgot")
	}
	readFileEmpty := func(string) ([]byte, error) { return []byte(""), nil }
	if SessionClosed(readFileEmpty, "/custom", "3") {
		t.Fatal("an empty but present record is not a closed session")
	}
}
