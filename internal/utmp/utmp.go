// Package utmp reads the system login-accounting database (utmp) to enumerate
// active terminal sessions and logged-in users, using native Go (no `who`/`w`
// process). It is shared by the tty/wall notifiers and the `users` check so the
// binary record parsing lives in one place.
package utmp

// Session is one active login session: the user, terminal line (for example
// "pts/0" or "tty1") and the remote host recorded by login accounting. Host
// is empty for local sessions.
type Session struct {
	User string
	Line string
	Host string
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
