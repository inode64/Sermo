package process

import (
	"sermo/internal/cfgval"
	"time"
)

// KillSelector is a stop_policy.kill_only_if selector. A process is killable
// only if its real UID matches one of Users AND its resolved exe exactly matches
// one of ExeAny (section 21/34).
type KillSelector struct {
	Users  []string
	ExeAny []string
}

// KillPolicy is the resolved stop_policy governing signal escalation (section 22).
type KillPolicy struct {
	GracefulTimeout time.Duration
	TermTimeout     time.Duration
	KillTimeout     time.Duration
	ForceKill       bool
	KillOnlyIf      KillSelector
}

// Killable reports whether p may be signalled. It requires a resolved exe that
// exactly matches an exe_any entry AND a real UID matching a users entry. A
// process with an unresolvable exe is never killable, and an empty selector
// (no users or no exe) matches nothing — both fail-safe (section 21/34).
func (s KillSelector) Killable(p Process, resolve UserResolver) bool {
	if !p.ExeOK {
		return false
	}
	return s.exeMatches(p.Exe) && s.userMatches(p.UID, resolve)
}

func (s KillSelector) exeMatches(exe string) bool {
	for _, candidate := range s.ExeAny {
		if canonicalizePath(candidate) == exe {
			return true
		}
	}
	return false
}

func (s KillSelector) userMatches(uid uint32, resolve UserResolver) bool {
	for _, u := range s.Users {
		if got, ok := resolve(u); ok && got == uid {
			return true
		}
	}
	return false
}

// ParseStopPolicy extracts the resolved stop_policy section into a KillPolicy,
// reporting malformed durations as warnings.
func ParseStopPolicy(tree map[string]any) (KillPolicy, []string) {
	policy := KillPolicy{}
	sp, ok := tree["stop_policy"].(map[string]any)
	if !ok {
		return policy, nil
	}

	var warnings []string
	policy.GracefulTimeout = parseDuration(sp["graceful_timeout"], "stop_policy.graceful_timeout", &warnings)
	policy.TermTimeout = parseDuration(sp["term_timeout"], "stop_policy.term_timeout", &warnings)
	policy.KillTimeout = parseDuration(sp["kill_timeout"], "stop_policy.kill_timeout", &warnings)
	if b, ok := sp["force_kill"].(bool); ok {
		policy.ForceKill = b
	}
	if koi, ok := sp["kill_only_if"].(map[string]any); ok {
		policy.KillOnlyIf.Users = cfgval.StringList(koi["users"])
		policy.KillOnlyIf.ExeAny = cfgval.StringList(koi["exe_any"])
	}
	return policy, warnings
}

func parseDuration(v any, field string, warnings *[]string) time.Duration {
	s := cfgval.AsString(v)
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		*warnings = append(*warnings, field+": invalid duration "+s)
		return 0
	}
	return d
}
