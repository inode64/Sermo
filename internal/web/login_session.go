package web

import (
	"crypto/sha256"
	"sync"
	"time"

	"sermo/internal/webcred"
)

const (
	// sessionCookiePrefix names the cookie that carries a dashboard login: an
	// opaque random token the server maps to the role the password granted.
	// sessionCookieName appends the host identity, because cookies are scoped
	// by host name but not by port: dashboards reached as localhost:9797 and
	// localhost:9798 through SSH tunnels must not overwrite each other's login.
	sessionCookiePrefix = "sermo_session"
	// defaultSessionTTL is how long a /login session lasts without
	// web.session_ttl.
	defaultSessionTTL = 12 * time.Hour
	// maxSessions bounds the in-memory store. A full store drops the session
	// closest to expiry among those of the new login's role, so logging in
	// again and again with the guest password cannot evict admin sessions.
	maxSessions = 1024

	// loginMaxFailures password attempts from one client within
	// loginFailureWindow that did not succeed lock it out until the window ends.
	loginMaxFailures   = 5
	loginFailureWindow = 15 * time.Minute
	// maxLoginClients bounds the failure table; a full table forgets the
	// client whose window started first.
	maxLoginClients = 4096
)

type session struct {
	role    string
	expires time.Time
}

// sessionStore keeps dashboard logins in memory, keyed by the sha256 of the
// cookie token so a dump of the map holds nothing a browser could replay. A
// daemon restart forgets every session: operators log in again.
type sessionStore struct {
	mu       sync.Mutex
	ttl      time.Duration
	sessions map[[sha256.Size]byte]session
}

func newSessionStore(ttl time.Duration) *sessionStore {
	return &sessionStore{ttl: ttl, sessions: map[[sha256.Size]byte]session{}}
}

// create starts a session for role at now and returns the cookie token.
func (s *sessionStore) create(role string, now time.Time) (string, error) {
	token, err := webcred.GenerateSecret()
	if err != nil {
		return "", err //nolint:wrapcheck // GenerateSecret already names what failed.
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, sess := range s.sessions {
		if !now.Before(sess.expires) {
			delete(s.sessions, key)
		}
	}
	if len(s.sessions) >= maxSessions {
		// Every session shares one TTL, so the earliest to expire is the oldest.
		sameRole := func(_ [sha256.Size]byte, sess session) bool { return sess.role == role }
		if !dropEarliest(s.sessions, func(sess session) time.Time { return sess.expires }, sameRole) {
			dropEarliest(s.sessions, func(sess session) time.Time { return sess.expires }, nil)
		}
	}
	s.sessions[sha256.Sum256([]byte(token))] = session{role: role, expires: now.Add(s.ttl)}
	return token, nil
}

// role returns the role of a session live at now, or "" for an unknown or
// expired one.
func (s *sessionStore) role(token string, now time.Time) string {
	key := sha256.Sum256([]byte(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[key]
	if !ok {
		return ""
	}
	if !now.Before(sess.expires) {
		delete(s.sessions, key)
		return ""
	}
	return sess.role
}

func (s *sessionStore) delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sha256.Sum256([]byte(token)))
}

// loginLimiter throttles password guessing per client: each attempt counts
// against the client as soon as it starts — before the slow password check, so
// a burst of concurrent guesses cannot all slip in under the limit — and only a
// success clears the count. After loginMaxFailures attempts inside
// loginFailureWindow the client is refused until that window, counted from its
// first attempt, ends.
type loginLimiter struct {
	mu      sync.Mutex
	clients map[string]loginFailures
}

type loginFailures struct {
	count int
	since time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{clients: map[string]loginFailures{}}
}

// reserve counts one attempt by client at now. It reports false, and how long
// to wait, when the client has used up its attempts for the window.
func (l *loginLimiter) reserve(client string, now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.clients[client]
	if ok && !now.Before(f.since.Add(loginFailureWindow)) {
		ok = false
	}
	if !ok {
		if len(l.clients) >= maxLoginClients {
			dropEarliest(l.clients, func(f loginFailures) time.Time { return f.since }, nil)
		}
		f = loginFailures{since: now}
	}
	if f.count >= loginMaxFailures {
		return f.since.Add(loginFailureWindow).Sub(now), false
	}
	f.count++
	l.clients[client] = f
	return 0, true
}

func (l *loginLimiter) succeed(client string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.clients, client)
}

// dropEarliest deletes the entry of m with the earliest time among those keep
// accepts (all of them when keep is nil), bounding a size-capped table without
// an ordering structure beside it. It reports whether it deleted one.
func dropEarliest[K comparable, V any](m map[K]V, at func(V) time.Time, keep func(K, V) bool) bool {
	var earliestKey K
	var earliest time.Time
	found := false
	for key, v := range m {
		if keep != nil && !keep(key, v) {
			continue
		}
		if t := at(v); !found || t.Before(earliest) {
			earliestKey, earliest, found = key, t, true
		}
	}
	if found {
		delete(m, earliestKey)
	}
	return found
}
