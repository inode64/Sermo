package logind

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"sermo/internal/hostfs"
)

const (
	// SessionsDir is where logind publishes one record per login session.
	SessionsDir = "/run/systemd/sessions"
	// SessionStateClosing is the record state of a session whose leader has
	// gone while processes of its scope still run.
	SessionStateClosing = "closing"
	sessionStateKey     = "STATE="
)

// sessionState reads the state logind recorded for one session id. found is
// false when logind has no record for it: the session is over and whatever
// still runs in its scope outlived its login. A record it cannot read reports
// found with an empty state, so an unreadable session is never called closed.
// readFile defaults to hostfs.ReadFile; dir defaults to SessionsDir.
func sessionState(readFile func(string) ([]byte, error), dir, id string) (state string, found bool) {
	if id == "" || strings.ContainsAny(id, "/\\") || id == "." || id == ".." {
		return "", false
	}
	if readFile == nil {
		readFile = hostfs.ReadFile
	}
	if dir == "" {
		dir = SessionsDir
	}
	data, err := readFile(filepath.Join(dir, id))
	if err != nil {
		// Only a missing record means logind forgot the session. Any other
		// failure (permission, I/O, a record mid-rewrite) says nothing about it.
		return "", !errors.Is(err, fs.ErrNotExist)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), sessionStateKey); ok {
			return strings.TrimSpace(value), true
		}
	}
	return "", true
}

// SessionsReadable reports whether logind's session directory can be listed.
// Without it a missing record proves nothing — the directory itself is gone or
// hidden — so a caller must not call any session closed. readDir defaults to
// hostfs.ReadDir; dir defaults to SessionsDir.
func SessionsReadable(readDir func(string) ([]os.DirEntry, error), dir string) bool {
	if readDir == nil {
		readDir = hostfs.ReadDir
	}
	if dir == "" {
		dir = SessionsDir
	}
	_, err := readDir(dir)
	return err == nil
}

// SessionClosed reports whether a session id names a login that is over: no
// logind record, or a record in the closing state.
func SessionClosed(readFile func(string) ([]byte, error), dir, id string) bool {
	state, found := sessionState(readFile, dir, id)
	return !found || state == SessionStateClosing
}
