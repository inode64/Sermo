package notify

import "sermo/internal/utmp"

// ActiveUserCount reports the number of distinct users with an active login
// session, read from utmp. It returns 0 when utmp is unreadable or empty (and on
// non-Linux platforms), so it is safe to call as a best-effort header stat.
func ActiveUserCount() int {
	count, err := utmp.DistinctUserCount()
	if err != nil {
		return 0
	}
	return count
}
