package logind

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeUsers lays out a logind users directory and a linger directory under a
// fresh temporary root; a nil linger slice leaves the linger directory absent.
func writeUsers(t *testing.T, records map[string]string, linger []string) (usersDir, lingerDir string) {
	t.Helper()
	root := t.TempDir()
	usersDir = filepath.Join(root, "users")
	lingerDir = filepath.Join(root, "linger")
	if err := os.Mkdir(usersDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range records {
		if err := os.WriteFile(filepath.Join(usersDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if linger != nil {
		if err := os.Mkdir(lingerDir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range linger {
			if err := os.WriteFile(filepath.Join(lingerDir, name), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return usersDir, lingerDir
}

func TestUsersWithoutLingerNamesTrackedUsersWithoutLinger(t *testing.T) {
	t.Parallel()
	records := map[string]string{
		"0":       "# This is private data. Do not parse.\nNAME=root\nSTATE=active\n",
		"1000":    "NAME=alice\nSTATE=closing\n",
		"1001":    "NAME=bob\nSTATE=lingering\n",
		".#0Ab1C": "NAME=ghost\n",
		"README":  "NAME=nobody\n",
	}
	for _, tc := range []struct {
		name   string
		linger []string
		want   []string
	}{
		{"no linger directory", nil, []string{"alice", "bob", "root"}},
		{"empty linger directory", []string{}, []string{"alice", "bob", "root"}},
		{"some linger", []string{"bob"}, []string{"alice", "root"}},
		{"all linger", []string{"alice", "bob", "root"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			usersDir, lingerDir := writeUsers(t, records, tc.linger)
			got, err := UsersWithoutLinger(usersDir, lingerDir)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("UsersWithoutLinger = %v, want %v", got, tc.want)
			}
		})
	}
}

// logind follows symlinks when it tests the linger file: a dangling link is no
// linger, while a link to an existing file is one.
func TestUsersWithoutLingerFollowsLingerLinks(t *testing.T) {
	t.Parallel()
	usersDir, lingerDir := writeUsers(t, map[string]string{"0": "NAME=root\n", "1000": "NAME=alice\n"}, []string{"target"})
	if err := os.Symlink(filepath.Join(lingerDir, "target"), filepath.Join(lingerDir, "alice")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(lingerDir, "missing"), filepath.Join(lingerDir, "root")); err != nil {
		t.Fatal(err)
	}
	got, err := UsersWithoutLinger(usersDir, lingerDir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"root"}) {
		t.Fatalf("UsersWithoutLinger = %v, want [root]", got)
	}
}

// A record or directory the function cannot judge is an error, so a guard on it
// denies instead of reading an unknown user as lingering.
func TestUsersWithoutLingerRejectsWhatItCannotJudge(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		records map[string]string
		want    string
	}{
		{"record without NAME", map[string]string{"0": "STATE=active\n"}, "no NAME"},
		{"empty NAME", map[string]string{"0": "NAME=\n"}, "names user"},
		{"NAME with a path", map[string]string{"0": "NAME=../etc\n"}, "names user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			usersDir, lingerDir := writeUsers(t, tc.records, []string{})
			got, err := UsersWithoutLinger(usersDir, lingerDir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("UsersWithoutLinger = %v, %v; want error containing %q", got, err, tc.want)
			}
		})
	}
	t.Run("missing users directory", func(t *testing.T) {
		t.Parallel()
		if got, err := UsersWithoutLinger(filepath.Join(t.TempDir(), "absent"), t.TempDir()); err == nil {
			t.Fatalf("UsersWithoutLinger = %v, want an error", got)
		}
	})
	t.Run("record that is not a regular file", func(t *testing.T) {
		t.Parallel()
		usersDir, lingerDir := writeUsers(t, nil, []string{})
		if err := os.Mkdir(filepath.Join(usersDir, "0"), 0o755); err != nil {
			t.Fatal(err)
		}
		if got, err := UsersWithoutLinger(usersDir, lingerDir); err == nil {
			t.Fatalf("UsersWithoutLinger = %v, want an error", got)
		}
	})
}
