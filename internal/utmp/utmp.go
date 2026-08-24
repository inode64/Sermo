// Package utmp reads the system login-accounting database (utmp) to enumerate
// active terminal sessions and logged-in users, using native Go (no `who`/`w`
// process). It is shared by the tty/wall notifiers and the `users` check so the
// binary record parsing lives in one place.
package utmp

import "time"

// DevRoot is the canonical Linux device root used to resolve terminal lines.
const DevRoot = "/dev"

// Session is one active login session: its leader PID, user, terminal line (for
// example "pts/0" or "tty1") and remote host recorded by login accounting.
// Host is empty for local sessions.
type Session struct {
	PID  int
	User string
	Line string
	Host string
}

// Terminal is the kernel identity and last input time of a login terminal.
type Terminal struct {
	Device     uint64
	AccessedAt time.Time
}

// DistinctUsers counts the unique, non-empty user names across sessions. It is
// platform-independent (the slice already comes from Sessions/SessionsFrom).
func DistinctUsers(sessions []Session) int {
	users := make(map[string]struct{}, len(sessions))
	for _, s := range sessions {
		if s.User != "" {
			users[s.User] = struct{}{}
		}
	}
	return len(users)
}

// DistinctUserCount reports the number of distinct users with an active login
// session in the system utmp database.
func DistinctUserCount() (int, error) {
	sessions, err := Sessions()
	if err != nil {
		return 0, err
	}
	return DistinctUsers(sessions), nil
}

// Sessions returns the active login sessions from the default utmp locations.
func Sessions() ([]Session, error) {
	return SessionsFrom(nil)
}

// DefaultPaths returns the usual utmp locations in lookup order. The returned
// slice is a copy; off Linux it is nil.
func DefaultPaths() []string {
	return append([]string(nil), defaultPaths...)
}
