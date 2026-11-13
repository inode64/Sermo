package logind

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"sermo/internal/hostfs"
)

const (
	// UsersDir is where logind publishes one record per user it tracks, named
	// by UID.
	UsersDir = "/run/systemd/users"
	// LingerDir holds one file per user whose manager outlives its sessions.
	LingerDir   = "/var/lib/systemd/linger"
	userNameKey = "NAME="
)

// UsersWithoutLinger names, sorted, the users logind tracks that have no
// linger file.
//
// A restarted logind may not restore the sessions it tracked; a user left with
// none and no linger file is then garbage-collected, and logind stops that
// user's manager and every process under it (tmux panes, user services). The
// set comes from logind's own records, the users it would collect, and the
// name each record carries is the one logind looks up in the linger directory.
// Any record or linger file it cannot judge is an error, never a lingering
// user. usersDir and lingerDir default to UsersDir and LingerDir.
func UsersWithoutLinger(usersDir, lingerDir string) ([]string, error) {
	usersDir = cmp.Or(usersDir, UsersDir)
	lingerDir = cmp.Or(lingerDir, LingerDir)
	entries, err := hostfs.ReadDir(usersDir)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	var out []string
	for _, entry := range entries {
		uid := entry.Name()
		// logind writes each record through a temporary dot file and renames
		// it into place, so only bare UIDs are records.
		if !isUID(uid) {
			continue
		}
		name, found, err := userName(filepath.Join(usersDir, uid))
		if err != nil {
			return nil, fmt.Errorf("user %s: %w", uid, err)
		}
		if !found {
			continue
		}
		lingers, err := lingerFileExists(filepath.Join(lingerDir, name))
		if err != nil {
			return nil, fmt.Errorf("user %s linger: %w", name, err)
		}
		if !lingers {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out, nil
}

// userName reads the NAME= of one user record. found is false when the record
// vanished after the listing: logind dropped the user in between.
func userName(path string) (name string, found bool, err error) {
	data, err := hostfs.ReadRegularFile(path, maxRecordBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read record: %w", err)
	}
	value, present := recordField(data, userNameKey)
	if !present {
		return "", false, errors.New("record has no NAME")
	}
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\") {
		return "", false, fmt.Errorf("record names user %q", value)
	}
	return value, true, nil
}

// lingerFileExists reports whether logind would find a linger file at path.
// logind tests it with access(2), which follows symlinks: any entry counts,
// a dangling link does not.
func lingerFileExists(path string) (bool, error) {
	if _, err := hostfs.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat: %w", err)
	}
	return true, nil
}

func isUID(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
