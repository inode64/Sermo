package process

import (
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"sermo/internal/cfgval"
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
//
// cmd carries the selector's cmdline regex when it declared one. It can only
// narrow the pair, never widen it: a process still has to match the exact
// resolved exe and the real UID first, so a spoofed argv gains nothing. Without
// it the derived authority would be broader than the selector that produced it,
// and a daemon whose workload children re-exec the same binary (GlusterFS
// bricks, for one) would authorize signalling those children.
type killIdentity struct {
	killIdentityKey
	cmdRe *regexp.Regexp
}

// killIdentityKey is the comparable form of a killIdentity, used to deduplicate
// pairs (a compiled regex is a pointer, so two equal patterns are not equal).
type killIdentityKey struct {
	user string
	exe  string
	cmd  string
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
	seen := map[killIdentityKey]bool{}
	for _, selector := range selectors {
		if !selector.HasStrictIdentity() {
			continue
		}
		// A delegated selector names processes the service owns but Sermo must
		// never signal, so it contributes no authority at all.
		if selector.Delegated {
			continue
		}
		key := killIdentityKey{user: selector.User, exe: selectorExePath(&selector), cmd: selector.Cmd}
		if seen[key] {
			continue
		}
		seen[key] = true
		pairs = append(pairs, killIdentity{killIdentityKey: key, cmdRe: selectorCmdRegexp(&selector)})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].user != pairs[j].user {
			return pairs[i].user < pairs[j].user
		}
		if pairs[i].exe != pairs[j].exe {
			return pairs[i].exe < pairs[j].exe
		}
		return pairs[i].cmd < pairs[j].cmd
	})
	policy.KillOnlyIf.pairs = pairs
	policy.ForceKill = len(pairs) > 0
	return policy
}

// Killable reports whether p may be signalled. It requires a resolved exe that
// exactly matches an exe_any entry AND a real UID matching a users entry. A
// process with an unresolvable exe is never killable, a delegated process is
// never killable, and an empty selector (no users or no exe) matches nothing —
// all fail-safe.
func (s KillSelector) Killable(p Process, resolve UserResolver) bool {
	if !s.Configured() {
		return false
	}
	if protectedKillProcess(p) {
		return false
	}
	// A delegated process is the service's workload, kept alive on purpose by an
	// init unit that stops only its main process. It is reported, never signalled.
	if p.Delegated {
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
		if !ok || uid != p.UID || pair.exe != p.Exe {
			continue
		}
		if pair.cmdRe != nil && !pair.cmdRe.MatchString(strings.Join(p.Cmdline, " ")) {
			continue
		}
		return true
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

// ParseReapPolicy extracts the resolved `reap:` block into the selector that
// authorizes signalling the service's stray processes. An absent block yields an
// unconfigured selector, which matches nothing: reaping a stray is opt-in per
// service, and the fail-safe is to report the process and touch it.
//
// Unlike stop_policy it rejects unknown keys. A stray is reached by cgroup
// membership rather than by a selector that named it, so a mistyped subkey would
// silently hand the action a selector the operator did not write; the same typo
// under stop_policy leaves the existing (narrower) authority in place.
func ParseReapPolicy(tree map[string]any) (KillSelector, []string) {
	var selector KillSelector
	block, ok := tree[SectionReap].(map[string]any)
	if !ok {
		if _, present := tree[SectionReap]; present {
			return selector, []string{SectionReap + ": must be a mapping with " + ReapKeyKillOnlyIf}
		}
		return selector, nil
	}

	var warnings []string
	for _, key := range slices.Sorted(maps.Keys(block)) {
		if key != ReapKeyKillOnlyIf {
			warnings = append(warnings, SectionReap+": "+key+" is not supported; the block accepts "+ReapKeyKillOnlyIf)
		}
	}
	koi, ok := block[ReapKeyKillOnlyIf].(map[string]any)
	if !ok {
		return selector, append(warnings, ReapKillOnlyIfPath+": must be a mapping with "+ReapKeyUsers+" and "+ReapKeyExeAny)
	}
	for _, key := range slices.Sorted(maps.Keys(koi)) {
		if key != ReapKeyUsers && key != ReapKeyExeAny {
			warnings = append(warnings, ReapKillOnlyIfPath+": "+key+" is not supported; it accepts "+ReapKeyUsers+" and "+ReapKeyExeAny)
		}
	}
	selector.Users = cfgval.StringList(koi[ReapKeyUsers])
	selector.ExeAny = cfgval.StringList(koi[ReapKeyExeAny])
	if !selector.Configured() {
		// Return the empty selector, not the partial one: a half-written selector
		// must authorize nothing rather than whatever half it does carry.
		return KillSelector{}, append(warnings, ReapKillOnlyIfPath+": requires both "+ReapKeyUsers+" and "+ReapKeyExeAny)
	}
	return selector, warnings
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
