//go:build linux

package utmp

import (
	"os"
	"path/filepath"
	"testing"
)

func record(typ uint16, pid int32, line, user, host string) []byte {
	rec := make([]byte, recordSize)
	nativeEndian.PutUint16(rec[:2], typ)
	nativeEndian.PutUint32(rec[pidOffset:pidOffset+pidSize], uint32(pid))
	copy(rec[lineOffset:lineOffset+lineSize], line)
	copy(rec[userOffset:userOffset+userSize], user)
	copy(rec[hostOffset:hostOffset+hostSize], host)
	return rec
}

func TestParseKeepsOnlyUserProcesses(t *testing.T) {
	data := append(record(userProcess, 4242, "pts/0", "root", "192.0.2.10"), record(2, 1, "tty1", "login", "")...)
	data = append(data, record(userProcess, 4343, "pts/1", "fran", "")...)

	got := parse(data)
	if len(got) != 2 {
		t.Fatalf("parse returned %d sessions: %+v", len(got), got)
	}
	if got[0] != (Session{PID: 4242, User: "root", Line: "pts/0", Host: "192.0.2.10"}) || got[1] != (Session{PID: 4343, User: "fran", Line: "pts/1"}) {
		t.Fatalf("parse = %+v", got)
	}
}

func TestDefaultPaths(t *testing.T) {
	got := DefaultPaths()
	if len(got) != 2 || got[0] != utmpRunPath || got[1] != utmpLegacyRunPath {
		t.Fatalf("DefaultPaths() = %v, want [%s %s]", got, utmpRunPath, utmpLegacyRunPath)
	}
	got[0] = "mutated"
	if DefaultPaths()[0] != utmpRunPath {
		t.Fatal("DefaultPaths returned a shared backing array")
	}
}

func TestSessionsFromFallsBackAndReads(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent")
	present := filepath.Join(dir, "utmp")
	data := append(record(userProcess, 42, "pts/0", "fran", "192.0.2.10"), record(userProcess, 43, "pts/1", "fran", "192.0.2.11")...)
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

func TestSessionsReadsDefaultPaths(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent")
	present := filepath.Join(dir, "utmp")
	data := record(userProcess, 42, "pts/0", "fran", "192.0.2.10")
	if err := os.WriteFile(present, data, 0o644); err != nil {
		t.Fatal(err)
	}

	orig := defaultPaths
	defaultPaths = []string{missing, present}
	t.Cleanup(func() { defaultPaths = orig })

	got, err := SessionsFrom(nil)
	if err != nil {
		t.Fatalf("SessionsFrom(nil): %v", err)
	}
	if DistinctUsers(got) != 1 {
		t.Fatalf("SessionsFrom(nil) = %+v, want one distinct user", got)
	}
}
