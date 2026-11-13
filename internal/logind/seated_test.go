package logind

import (
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

func writeSessions(t *testing.T, records map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for id, body := range records {
		if err := os.WriteFile(filepath.Join(dir, id), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSeatedSessionsNamesOnlySeatedRecords(t *testing.T) {
	t.Parallel()
	dir := writeSessions(t, map[string]string{
		"c1":      "# This is private data. Do not parse.\nUID=0\nUSER=root\nREMOTE=1\nTTY=pts/0\nSTATE=active\n",
		"c2":      "UID=0\nUSER=root\nSTATE=closing\nSEAT=\n",
		"3":       "UID=1000\nUSER=alice\nSEAT=seat0\nVTNR=1\nSTATE=active\n",
		"7":       "UID=1001\nSEAT=seat1\n",
		".#c4XyZ": "USER=ghost\nSEAT=seat0\n",
	})
	// logind keeps a FIFO beside each record; opening one for reading would
	// block until a writer appears.
	if err := syscall.Mkfifo(filepath.Join(dir, "c1.ref"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := SeatedSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"3 (alice on seat0)", "7 (? on seat1)"}; !slices.Equal(got, want) {
		t.Fatalf("SeatedSessions = %v, want %v", got, want)
	}
	empty, err := SeatedSessions(writeSessions(t, map[string]string{"c1": "USER=root\nREMOTE=1\n"}))
	if err != nil || len(empty) != 0 {
		t.Fatalf("seatless sessions = %v, %v; want none", empty, err)
	}
}

// What cannot be read is an error, so a guard on it denies instead of calling
// the host seatless; a FIFO under a record's name is refused without blocking.
func TestSeatedSessionsRejectsWhatItCannotRead(t *testing.T) {
	t.Parallel()
	t.Run("missing directory", func(t *testing.T) {
		t.Parallel()
		if got, err := SeatedSessions(filepath.Join(t.TempDir(), "absent")); err == nil {
			t.Fatalf("SeatedSessions = %v, want an error", got)
		}
	})
	t.Run("FIFO as a record", func(t *testing.T) {
		t.Parallel()
		dir := writeSessions(t, nil)
		if err := syscall.Mkfifo(filepath.Join(dir, "c9"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := SeatedSessions(dir); err == nil {
			t.Fatalf("SeatedSessions = %v, want an error", got)
		}
	})
	t.Run("directory as a record", func(t *testing.T) {
		t.Parallel()
		dir := writeSessions(t, nil)
		if err := os.Mkdir(filepath.Join(dir, "c9"), 0o755); err != nil {
			t.Fatal(err)
		}
		if got, err := SeatedSessions(dir); err == nil {
			t.Fatalf("SeatedSessions = %v, want an error", got)
		}
	})
}
