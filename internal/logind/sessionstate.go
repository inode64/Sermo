package logind

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
	sessionSeatKey      = "SEAT="
	sessionUserKey      = "USER="
	// maxRecordBytes bounds one session or user record; logind writes a few
	// hundred bytes of KEY=VALUE lines.
	maxRecordBytes = 64 << 10
	// sessionRefSuffix names the FIFO logind keeps beside each session record.
	sessionRefSuffix = ".ref"
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
	state, _ = recordField(data, sessionStateKey)
	return state, true
}

// recordField returns the value of the first KEY=VALUE line of a logind record
// whose prefix is key (which includes the "="), and whether it is present.
func recordField(data []byte, key string) (string, bool) {
	for line := range strings.SplitSeq(string(data), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), key); ok {
			return strings.TrimSpace(value), true
		}
	}
	return "", false
}

// SeatedSessions names, sorted, the logind sessions attached to a seat, each as
// "id (user on seat)".
//
// A session on a local seat holds device descriptors (DRM, input) that logind
// may release when it stops, so it is the one a logind stop or restart can
// disrupt; an SSH or background session has no seat. logind writes SEAT= only
// for a seated session. The records are read from disk, not asked over D-Bus,
// so the answer stays available while logind has lost the system bus. The
// .ref FIFO beside each record is skipped and every record is opened without
// blocking. An unreadable directory or record is an error, never "no seat".
// dir defaults to SessionsDir.
func SeatedSessions(dir string) ([]string, error) {
	dir = cmp.Or(dir, SessionsDir)
	entries, err := hostfs.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	var out []string
	for _, entry := range entries {
		id := entry.Name()
		// logind writes each record through a temporary dot file and renames it
		// into place.
		if strings.HasPrefix(id, ".") || strings.HasSuffix(id, sessionRefSuffix) {
			continue
		}
		data, err := hostfs.ReadRegularFile(filepath.Join(dir, id), maxRecordBytes)
		if errors.Is(err, fs.ErrNotExist) {
			continue // the session ended between the listing and the read
		}
		if err != nil {
			return nil, fmt.Errorf("session %s: %w", id, err)
		}
		seat, _ := recordField(data, sessionSeatKey)
		if seat == "" {
			continue
		}
		user, _ := recordField(data, sessionUserKey)
		out = append(out, fmt.Sprintf("%s (%s on %s)", id, cmp.Or(user, "?"), seat))
	}
	slices.Sort(out)
	return out, nil
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
