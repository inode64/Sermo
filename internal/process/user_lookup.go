package process

import (
	"context"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"time"

	"sermo/internal/execx"
)

const (
	// UserLookupAuto uses native os/user lookups. When the binary was built with
	// CGO disabled, native lookups only cover local passwd/group files, so auto
	// falls back to `getent` for NSS-backed users and groups.
	UserLookupAuto = "auto"
	// UserLookupNative uses only Go's os/user package. With CGO enabled this
	// normally goes through libc/NSS; with CGO disabled it is local-file based.
	UserLookupNative = "native"
	// UserLookupGetent prefers `getent passwd|group`, then falls back to native
	// lookups if getent is missing or returns no entry.
	UserLookupGetent = "getent"
	// UserLookupNumeric disables name lookups. Numeric UID/GID selectors still
	// work, but names fail closed and displayed owners remain numeric/blank.
	UserLookupNumeric = "numeric"

	// DefaultUserLookupTimeout bounds each getent lookup.
	DefaultUserLookupTimeout = 250 * time.Millisecond
)

type idLookupResult struct {
	id uint32
	ok bool
	at time.Time // when resolved; negative results expire after negativeCacheTTL
}

type nameLookupResult struct {
	name string
	ok   bool
	at   time.Time
}

// negativeCacheTTL bounds how long a failed (ok=false) lookup is cached. Positive
// results are cached for the lookup's lifetime, but caching a miss forever means
// a user created after the first probe — e.g. one named in kill_only_if or a
// process selector — would never be recognized until the daemon restarts,
// silently weakening a force_kill safety decision.
const negativeCacheTTL = 30 * time.Second

// UserLookupConfig configures user/group lookup behavior.
type UserLookupConfig struct {
	Mode    string
	Timeout time.Duration
	Runner  execx.Runner
}

// UserLookup resolves users and groups with per-process caches.
type UserLookup struct {
	mode    string
	timeout time.Duration
	runner  execx.Runner

	now    func() time.Time // injectable clock (defaults to time.Now)
	negTTL time.Duration    // negative-result TTL (defaults to negativeCacheTTL)

	mu         sync.Mutex
	users      map[string]idLookupResult
	groups     map[string]idLookupResult
	userNames  map[uint32]nameLookupResult
	groupNames map[uint32]nameLookupResult
}

// ValidUserLookupMode reports whether mode is accepted by NewUserLookup.
func ValidUserLookupMode(mode string) bool {
	switch NormalizeUserLookupMode(mode) {
	case UserLookupAuto, UserLookupNative, UserLookupGetent, UserLookupNumeric:
		return true
	default:
		return false
	}
}

// NormalizeUserLookupMode returns the canonical lookup mode. Empty means auto.
func NormalizeUserLookupMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return UserLookupAuto
	}
	return mode
}

// NewUserLookup returns a cached user/group lookup service.
func NewUserLookup(cfg UserLookupConfig) *UserLookup {
	mode := NormalizeUserLookupMode(cfg.Mode)
	if !ValidUserLookupMode(mode) {
		mode = UserLookupAuto
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultUserLookupTimeout
	}
	runner := cfg.Runner
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	return &UserLookup{
		mode:       mode,
		timeout:    timeout,
		runner:     runner,
		now:        time.Now,
		negTTL:     negativeCacheTTL,
		users:      map[string]idLookupResult{},
		groups:     map[string]idLookupResult{},
		userNames:  map[uint32]nameLookupResult{},
		groupNames: map[uint32]nameLookupResult{},
	}
}

// DefaultUserLookup is the production default used when no config is available.
func DefaultUserLookup() *UserLookup {
	return NewUserLookup(UserLookupConfig{Mode: UserLookupAuto})
}

// ResolveUser resolves a user name or numeric UID to a UID.
func (l *UserLookup) ResolveUser(name string) (uint32, bool) {
	if uid, ok := parseUint32(name); ok {
		return uid, true
	}
	if l == nil {
		return OSUserResolver(name)
	}
	if got, cached := cachedID(l, l.users, name); cached {
		return got.id, got.ok
	}
	uid, ok := l.lookupUserID(name)
	storeID(l, l.users, name, idLookupResult{id: uid, ok: ok})
	return uid, ok
}

// ResolveGroup resolves a group name or numeric GID to a GID.
func (l *UserLookup) ResolveGroup(name string) (uint32, bool) {
	if gid, ok := parseUint32(name); ok {
		return gid, true
	}
	if l == nil {
		return OSGroupResolver(name)
	}
	if got, cached := cachedID(l, l.groups, name); cached {
		return got.id, got.ok
	}
	gid, ok := l.lookupGroupID(name)
	storeID(l, l.groups, name, idLookupResult{id: gid, ok: ok})
	return gid, ok
}

// Username returns a display name for uid, or an empty string when unknown.
func (l *UserLookup) Username(uid uint32) string {
	if l == nil {
		if name, ok := nativeUserName(uid); ok {
			return name
		}
		return ""
	}
	if got, cached := cachedName(l, l.userNames, uid); cached {
		if got.ok {
			return got.name
		}
		return ""
	}
	name, ok := l.lookupUserName(uid)
	storeName(l, l.userNames, uid, nameLookupResult{name: name, ok: ok})
	return name
}

// GroupName returns a display name for gid, or an empty string when unknown.
func (l *UserLookup) GroupName(gid uint32) string {
	if l == nil {
		if name, ok := nativeGroupName(gid); ok {
			return name
		}
		return ""
	}
	if got, cached := cachedName(l, l.groupNames, gid); cached {
		if got.ok {
			return got.name
		}
		return ""
	}
	name, ok := l.lookupGroupName(gid)
	storeName(l, l.groupNames, gid, nameLookupResult{name: name, ok: ok})
	return name
}

func (l *UserLookup) lookupUserID(name string) (uint32, bool) {
	switch l.mode {
	case UserLookupNumeric:
		return 0, false
	case UserLookupNative:
		return nativeUserID(name)
	case UserLookupGetent:
		if uid, ok := l.getentUserID(name); ok {
			return uid, true
		}
		return nativeUserID(name)
	default: // auto
		if uid, ok := nativeUserID(name); ok {
			return uid, true
		}
		if !cgoEnabled {
			return l.getentUserID(name)
		}
		return 0, false
	}
}

func (l *UserLookup) lookupGroupID(name string) (uint32, bool) {
	switch l.mode {
	case UserLookupNumeric:
		return 0, false
	case UserLookupNative:
		return nativeGroupID(name)
	case UserLookupGetent:
		if gid, ok := l.getentGroupID(name); ok {
			return gid, true
		}
		return nativeGroupID(name)
	default: // auto
		if gid, ok := nativeGroupID(name); ok {
			return gid, true
		}
		if !cgoEnabled {
			return l.getentGroupID(name)
		}
		return 0, false
	}
}

func (l *UserLookup) lookupUserName(uid uint32) (string, bool) {
	switch l.mode {
	case UserLookupNumeric:
		return "", false
	case UserLookupNative:
		return nativeUserName(uid)
	case UserLookupGetent:
		if name, ok := l.getentUserName(uid); ok {
			return name, true
		}
		return nativeUserName(uid)
	default: // auto
		if name, ok := nativeUserName(uid); ok {
			return name, true
		}
		if !cgoEnabled {
			return l.getentUserName(uid)
		}
		return "", false
	}
}

func (l *UserLookup) lookupGroupName(gid uint32) (string, bool) {
	switch l.mode {
	case UserLookupNumeric:
		return "", false
	case UserLookupNative:
		return nativeGroupName(gid)
	case UserLookupGetent:
		if name, ok := l.getentGroupName(gid); ok {
			return name, true
		}
		return nativeGroupName(gid)
	default: // auto
		if name, ok := nativeGroupName(gid); ok {
			return name, true
		}
		if !cgoEnabled {
			return l.getentGroupName(gid)
		}
		return "", false
	}
}

func cachedID(l *UserLookup, cache map[string]idLookupResult, key string) (idLookupResult, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	got, ok := cache[key]
	if !ok || l.negativeExpired(got.ok, got.at) {
		return idLookupResult{}, false
	}
	return got, true
}

func storeID(l *UserLookup, cache map[string]idLookupResult, key string, value idLookupResult) {
	l.mu.Lock()
	value.at = l.clock()
	cache[key] = value
	l.mu.Unlock()
}

func cachedName(l *UserLookup, cache map[uint32]nameLookupResult, key uint32) (nameLookupResult, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	got, ok := cache[key]
	if !ok || l.negativeExpired(got.ok, got.at) {
		return nameLookupResult{}, false
	}
	return got, true
}

func storeName(l *UserLookup, cache map[uint32]nameLookupResult, key uint32, value nameLookupResult) {
	l.mu.Lock()
	value.at = l.clock()
	cache[key] = value
	l.mu.Unlock()
}

// clock returns the current time via the injectable hook, defaulting to time.Now.
func (l *UserLookup) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// negativeExpired reports whether a cached miss (ok=false) has outlived negTTL
// and must be re-resolved. Positive results never expire.
func (l *UserLookup) negativeExpired(ok bool, at time.Time) bool {
	if ok {
		return false
	}
	ttl := l.negTTL
	if ttl <= 0 {
		ttl = negativeCacheTTL
	}
	return l.clock().Sub(at) >= ttl
}

func (l *UserLookup) getentUserID(name string) (uint32, bool) {
	line, ok := l.getent("passwd", name)
	if !ok {
		return 0, false
	}
	_, uid, ok := parsePasswdLine(line)
	return uid, ok
}

func (l *UserLookup) getentGroupID(name string) (uint32, bool) {
	line, ok := l.getent("group", name)
	if !ok {
		return 0, false
	}
	_, gid, ok := parseGroupLine(line)
	return gid, ok
}

func (l *UserLookup) getentUserName(uid uint32) (string, bool) {
	line, ok := l.getent("passwd", strconv.FormatUint(uint64(uid), 10))
	if !ok {
		return "", false
	}
	name, _, ok := parsePasswdLine(line)
	return name, ok
}

func (l *UserLookup) getentGroupName(gid uint32) (string, bool) {
	line, ok := l.getent("group", strconv.FormatUint(uint64(gid), 10))
	if !ok {
		return "", false
	}
	name, _, ok := parseGroupLine(line)
	return name, ok
}

func (l *UserLookup) getent(database, query string) (string, bool) {
	res, err := execx.Run(context.Background(), l.runner, l.timeout, "getent", database, query)
	if err != nil || res.ExitCode != 0 {
		return "", false
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line, true
		}
	}
	return "", false
}

func nativeUserID(name string) (uint32, bool) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, false
	}
	return parseUint32(u.Uid)
}

func nativeGroupID(name string) (uint32, bool) {
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, false
	}
	return parseUint32(g.Gid)
}

func nativeUserName(uid uint32) (string, bool) {
	u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil || u.Username == "" {
		return "", false
	}
	return u.Username, true
}

func nativeGroupName(gid uint32) (string, bool) {
	g, err := user.LookupGroupId(strconv.FormatUint(uint64(gid), 10))
	if err != nil || g.Name == "" {
		return "", false
	}
	return g.Name, true
}

func parsePasswdLine(line string) (string, uint32, bool) {
	fields := strings.Split(line, ":")
	if len(fields) < 3 || fields[0] == "" {
		return "", 0, false
	}
	uid, ok := parseUint32(fields[2])
	return fields[0], uid, ok
}

func parseGroupLine(line string) (string, uint32, bool) {
	fields := strings.Split(line, ":")
	if len(fields) < 3 || fields[0] == "" {
		return "", 0, false
	}
	gid, ok := parseUint32(fields[2])
	return fields[0], gid, ok
}

func parseUint32(s string) (uint32, bool) {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(n), true
}
