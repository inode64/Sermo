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

const (
	getentDatabaseGroup  = "group"
	getentDatabasePasswd = "passwd"
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

type nameResolver func(uint32) (string, bool)

// negativeCacheTTL bounds how long a failed (ok=false) lookup is cached. Positive
// results are cached for the lookup's lifetime, but caching a miss forever means
// a user created after the first probe — e.g. one named in kill_only_if or a
// process selector — would never be recognized until the daemon restarts,
// silently weakening a force_kill safety decision.
const negativeCacheTTL = 30 * time.Second

const (
	passwdGroupFieldSeparator = ":"
	passwdGroupNameIndex      = 0
	passwdGroupIDIndex        = 2
	passwdGroupMinFields      = passwdGroupIDIndex + 1
)

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
	if l == nil {
		return OSUserResolver(name)
	}
	return l.resolveID(name, l.users, nativeUserID, l.getentUserID)
}

// ResolveGroup resolves a group name or numeric GID to a GID.
func (l *UserLookup) ResolveGroup(name string) (uint32, bool) {
	if l == nil {
		return OSGroupResolver(name)
	}
	return l.resolveID(name, l.groups, nativeGroupID, l.getentGroupID)
}

// Username returns a display name for uid, or an empty string when unknown.
func (l *UserLookup) Username(uid uint32) string {
	if l == nil {
		name, _ := nativeUserName(uid)
		return name
	}
	return l.resolveName(uid, l.userNames, nativeUserName, l.getentUserName)
}

// GroupName returns a display name for gid, or an empty string when unknown.
func (l *UserLookup) GroupName(gid uint32) string {
	if l == nil {
		name, _ := nativeGroupName(gid)
		return name
	}
	return l.resolveName(gid, l.groupNames, nativeGroupName, l.getentGroupName)
}

func (l *UserLookup) resolveID(name string, cache map[string]idLookupResult, native, getent UserResolver) (uint32, bool) {
	if id, ok := parseUint32(name); ok {
		return id, true
	}
	if got, cached := cachedID(l, cache, name); cached {
		return got.id, got.ok
	}
	id, ok := l.lookupID(name, native, getent)
	storeID(l, cache, name, idLookupResult{id: id, ok: ok})
	return id, ok
}

func (l *UserLookup) resolveName(id uint32, cache map[uint32]nameLookupResult, native, getent nameResolver) string {
	if got, cached := cachedName(l, cache, id); cached {
		if got.ok {
			return got.name
		}
		return ""
	}
	name, ok := l.lookupName(id, native, getent)
	storeName(l, cache, id, nameLookupResult{name: name, ok: ok})
	return name
}

func (l *UserLookup) lookupID(name string, native, getent UserResolver) (uint32, bool) {
	switch l.mode {
	case UserLookupNumeric:
		return 0, false
	case UserLookupNative:
		return native(name)
	case UserLookupGetent:
		if id, ok := getent(name); ok {
			return id, true
		}
		return native(name)
	default: // auto
		if id, ok := native(name); ok {
			return id, true
		}
		if !cgoEnabled {
			return getent(name)
		}
		return 0, false
	}
}

func (l *UserLookup) lookupName(id uint32, native, getent nameResolver) (string, bool) {
	switch l.mode {
	case UserLookupNumeric:
		return "", false
	case UserLookupNative:
		return native(id)
	case UserLookupGetent:
		if name, ok := getent(id); ok {
			return name, true
		}
		return native(id)
	default: // auto
		if name, ok := native(id); ok {
			return name, true
		}
		if !cgoEnabled {
			return getent(id)
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
	line, ok := l.getent(getentDatabasePasswd, name)
	if !ok {
		return 0, false
	}
	_, uid, ok := parseUnixDatabaseLine(line)
	return uid, ok
}

func (l *UserLookup) getentGroupID(name string) (uint32, bool) {
	line, ok := l.getent(getentDatabaseGroup, name)
	if !ok {
		return 0, false
	}
	_, gid, ok := parseUnixDatabaseLine(line)
	return gid, ok
}

func (l *UserLookup) getentUserName(uid uint32) (string, bool) {
	line, ok := l.getent(getentDatabasePasswd, strconv.FormatUint(uint64(uid), numericIDBase))
	if !ok {
		return "", false
	}
	name, _, ok := parseUnixDatabaseLine(line)
	return name, ok
}

func (l *UserLookup) getentGroupName(gid uint32) (string, bool) {
	line, ok := l.getent(getentDatabaseGroup, strconv.FormatUint(uint64(gid), numericIDBase))
	if !ok {
		return "", false
	}
	name, _, ok := parseUnixDatabaseLine(line)
	return name, ok
}

func (l *UserLookup) getent(database, query string) (string, bool) {
	res, err := execx.Run(context.Background(), l.runner, l.timeout, UserLookupGetent, database, query)
	if err != nil || res.ExitCode != execx.ExitCodeSuccess {
		return "", false
	}
	for line := range strings.SplitSeq(res.Stdout, procLineSeparator) {
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
	u, err := user.LookupId(strconv.FormatUint(uint64(uid), numericIDBase))
	if err != nil || u.Username == "" {
		return "", false
	}
	return u.Username, true
}

func nativeGroupName(gid uint32) (string, bool) {
	g, err := user.LookupGroupId(strconv.FormatUint(uint64(gid), numericIDBase))
	if err != nil || g.Name == "" {
		return "", false
	}
	return g.Name, true
}

// parseUnixDatabaseLine extracts the name and numeric ID from the shared
// colon-separated passwd/group record layout.
func parseUnixDatabaseLine(line string) (string, uint32, bool) {
	fields := strings.Split(line, passwdGroupFieldSeparator)
	if len(fields) < passwdGroupMinFields || fields[passwdGroupNameIndex] == "" {
		return "", 0, false
	}
	id, ok := parseUint32(fields[passwdGroupIDIndex])
	return fields[passwdGroupNameIndex], id, ok
}

func parseUint32(s string) (uint32, bool) {
	n, err := strconv.ParseUint(strings.TrimSpace(s), numericIDBase, numericIDBits)
	if err != nil {
		return 0, false
	}
	return uint32(n), true
}
