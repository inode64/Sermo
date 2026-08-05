package process

import (
	"errors"
	"fmt"
)

// IdentityFilter narrows a read-only process observation by exact executable,
// real user and/or real primary group. Unlike Selector it intentionally permits
// an owner-only filter: callers must already scope the candidate process set,
// for example to one terminal session. It never authorizes signalling.
type IdentityFilter struct {
	Exe   string
	User  string
	Group string

	exePath string
}

// IdentityMatch reports whether a filter definitively matches a process, does
// not match it, or cannot decide because an exact executable was unreadable.
type IdentityMatch uint8

const (
	// IdentityNoMatch means the process does not satisfy the filter.
	IdentityNoMatch IdentityMatch = iota
	// IdentityMatched means the process satisfies every filter field.
	IdentityMatched
	// IdentityUnknown means a required executable identity was unreadable.
	IdentityUnknown
)

// NewIdentityFilter validates and prepares a terminal-scoped process filter.
func NewIdentityFilter(exe, user, group string) (IdentityFilter, error) {
	if exe == "" && user == "" && group == "" {
		return IdentityFilter{}, errors.New("identity filter requires exe, user or group")
	}
	filter := IdentityFilter{Exe: exe, User: user, Group: group}
	if exe != "" {
		filter.exePath = canonicalizePath(exe)
	}
	return filter, nil
}

// Match applies every configured field to id. Resolving a configured owner is
// part of the decision: an unknown user/group is an error so operation guards
// can block instead of silently weakening a protected-session policy.
func (f IdentityFilter) Match(id Identity, resolveUser, resolveGroup UserResolver) (IdentityMatch, error) {
	if f.User != "" {
		if resolveUser == nil {
			return IdentityNoMatch, fmt.Errorf("resolve user %q: resolver unavailable", f.User)
		}
		uid, ok := resolveUser(f.User)
		if !ok {
			return IdentityNoMatch, fmt.Errorf("resolve user %q", f.User)
		}
		if uid != id.UID {
			return IdentityNoMatch, nil
		}
	}
	if f.Group != "" {
		if resolveGroup == nil {
			return IdentityNoMatch, fmt.Errorf("resolve group %q: resolver unavailable", f.Group)
		}
		gid, ok := resolveGroup(f.Group)
		if !ok {
			return IdentityNoMatch, fmt.Errorf("resolve group %q", f.Group)
		}
		if gid != id.GID {
			return IdentityNoMatch, nil
		}
	}
	if f.Exe == "" {
		return IdentityMatched, nil
	}
	if !id.ExeOK {
		return IdentityUnknown, nil
	}
	if f.exePath == id.Exe {
		return IdentityMatched, nil
	}
	return IdentityNoMatch, nil
}
