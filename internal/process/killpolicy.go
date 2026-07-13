package process

import (
	"sermo/internal/cfgval"
	"time"
)

// KillSelector is a stop_policy.kill_only_if selector. A process is killable
// only if its real UID matches one of Users AND its resolved exe exactly matches
// one of ExeAny.
type KillSelector struct {
	Users  []string
	ExeAny []string
}

// Configured reports whether the selector has the minimum fields required to
// authorize signalling. Empty or partial selectors intentionally match nothing.
func (s KillSelector) Configured() bool {
	return len(s.Users) > 0 && len(s.ExeAny) > 0
}

// KillPolicy is the resolved stop_policy governing signal escalation.
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
	return s.exeMatches(p.Exe) && s.userMatches(p.UID, resolve)
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
	if b, ok := sp[StopPolicyKeyForceKill].(bool); ok {
		policy.ForceKill = b
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
