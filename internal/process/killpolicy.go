package process

import (
	"sermo/internal/cfgval"
	"sort"
	"time"
)

// KillSelector is a stop_policy.kill_only_if selector. A process is killable
// only if its real UID matches one of Users AND its resolved exe exactly matches
// one of ExeAny.
type KillSelector struct {
	Users  []string
	ExeAny []string
	pairs  []killIdentity
}

// killIdentity keeps an automatic residual-reaping identity paired. Keeping
// user and exe together avoids authorizing the cross-product that a pair of
// independent users/exe_any lists would create for multi-role services.
type killIdentity struct {
	user string
	exe  string
}

// Configured reports whether the selector has the minimum fields required to
// authorize signalling. Empty or partial selectors intentionally match nothing.
func (s KillSelector) Configured() bool {
	return len(s.pairs) > 0 || len(s.Users) > 0 && len(s.ExeAny) > 0
}

// KillPolicy is the resolved stop_policy governing signal escalation.
type KillPolicy struct {
	GracefulTimeout time.Duration
	TermTimeout     time.Duration
	KillTimeout     time.Duration
	ForceKill       bool
	Automatic       bool
	KillOnlyIf      KillSelector
}

// EnableAutomaticReaping resolves force_kill: auto into a signal policy. An
// explicit kill_only_if remains authoritative; otherwise every named process
// selector that declares an exact executable and real user supplies one paired
// identity. Services without such an identity remain safely non-killable.
func EnableAutomaticReaping(policy KillPolicy, selectors []Selector) KillPolicy {
	if !policy.Automatic {
		return policy
	}
	if policy.KillOnlyIf.Configured() {
		policy.ForceKill = true
		return policy
	}

	pairs := make([]killIdentity, 0, len(selectors))
	seen := map[killIdentity]bool{}
	for _, selector := range selectors {
		if selector.Type != SelectorCommandMatch || selector.Exe == "" || selector.User == "" {
			continue
		}
		pair := killIdentity{user: selector.User, exe: selectorExePath(&selector)}
		if seen[pair] {
			continue
		}
		seen[pair] = true
		pairs = append(pairs, pair)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].user != pairs[j].user {
			return pairs[i].user < pairs[j].user
		}
		return pairs[i].exe < pairs[j].exe
	})
	policy.KillOnlyIf.pairs = pairs
	policy.ForceKill = len(pairs) > 0
	return policy
}

// Killable reports whether p may be signalled. It requires a resolved exe that
// exactly matches an exe_any entry AND a real UID matching a users entry. A
// process with an unresolvable exe is never killable, and an empty selector
// (no users or no exe) matches nothing — both fail-safe.
func (s KillSelector) Killable(p Process, resolve UserResolver) bool {
	if !s.Configured() {
		return false
	}
	if protectedKillProcess(p) {
		return false
	}
	if !p.ExeOK {
		return false
	}
	return s.explicitMatches(p, resolve) || s.pairMatches(p, resolve)
}

func (s KillSelector) explicitMatches(p Process, resolve UserResolver) bool {
	return s.exeMatches(p.Exe) && s.userMatches(p.UID, resolve)
}

func (s KillSelector) pairMatches(p Process, resolve UserResolver) bool {
	for _, pair := range s.pairs {
		uid, ok := resolve(pair.user)
		if ok && uid == p.UID && pair.exe == p.Exe {
			return true
		}
	}
	return false
}

func protectedKillProcess(p Process) bool {
	return p.PID <= 1 || protectedKernelProcess(p.PID, p.PPID, p.ExeOK, p.Cmdline)
}

func protectedKernelProcess(pid, ppid int, exeOK bool, cmdline []string) bool {
	return (pid == 2 || ppid == 2) && !exeOK && len(cmdline) == 0
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
	sp, ok := tree[SectionStopPolicy].(map[string]any)
	if !ok {
		return policy, nil
	}

	var warnings []string
	policy.GracefulTimeout = parseDuration(sp[StopPolicyKeyGracefulTimeout], SectionStopPolicy+"."+StopPolicyKeyGracefulTimeout, &warnings)
	policy.TermTimeout = parseDuration(sp[StopPolicyKeyTermTimeout], SectionStopPolicy+"."+StopPolicyKeyTermTimeout, &warnings)
	policy.KillTimeout = parseDuration(sp[StopPolicyKeyKillTimeout], SectionStopPolicy+"."+StopPolicyKeyKillTimeout, &warnings)
	if value, present := sp[StopPolicyKeyForceKill]; present {
		switch v := value.(type) {
		case bool:
			policy.ForceKill = v
		case string:
			if v == StopPolicyForceKillAuto {
				policy.Automatic = true
			} else {
				warnings = append(warnings, SectionStopPolicy+"."+StopPolicyKeyForceKill+": must be boolean or "+StopPolicyForceKillAuto)
			}
		default:
			warnings = append(warnings, SectionStopPolicy+"."+StopPolicyKeyForceKill+": must be boolean or "+StopPolicyForceKillAuto)
		}
	}
	if koi, ok := sp[StopPolicyKeyKillOnlyIf].(map[string]any); ok {
		policy.KillOnlyIf.Users = cfgval.StringList(koi[StopPolicyKeyUsers])
		policy.KillOnlyIf.ExeAny = cfgval.StringList(koi[StopPolicyKeyExeAny])
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
