//go:build linux

package utmp

import (
	"os"
	"path/filepath"
	"testing"
)

func record(typ uint16, line, user string) []byte {
	rec := make([]byte, recordSize)
	nativeEndian.PutUint16(rec[:2], typ)
	copy(rec[lineOffset:lineOffset+lineSize], line)
	copy(rec[userOffset:userOffset+userSize], user)
	return rec
}

func TestParseKeepsOnlyUserProcesses(t *testing.T) {
	data := append(record(userProcess, "pts/0", "root"), record(2, "tty1", "login")...)
	data = append(data, record(userProcess, "pts/1", "fran")...)

	got := parse(data)
	if len(got) != 2 {
		t.Fatalf("parse returned %d sessions: %+v", len(got), got)
	}
	if got[0] != (Session{User: "root", Line: "pts/0"}) || got[1] != (Session{User: "fran", Line: "pts/1"}) {
		t.Fatalf("parse = %+v", got)
	}
}

func TestSessionsFromFallsBackAndReads(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent")
	present := filepath.Join(dir, "utmp")
	data := append(record(userProcess, "pts/0", "fran"), record(userProcess, "pts/1", "fran")...)
	if err := os.WriteFile(present, data, 0o644); err != nil {
		t.Fatal(err)
	}

	sessions, err := SessionsFrom([]string{missing, present})
	if err != nil {
		t.Fatalf("SessionsFrom: %v", err)
	}
	if DistinctUsers(sessions) != 1 {
		t.Fatalf("sessions = %+v, want one distinct user", sessions)
	}

	if _, err := SessionsFrom([]string{missing}); err == nil {
		t.Fatal("SessionsFrom with no readable file must error")
	}
}
