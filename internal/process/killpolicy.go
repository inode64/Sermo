package process

import (
	"cmp"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"sermo/internal/cfgval"
)

// KillSelector is a stop_policy.kill_only_if selector. A process is killable
// only if its real UID matches one of Users AND its resolved exe exactly matches
// one of ExeAny.
type KillSelector struct {
	Users    []string
	ExeAny   []string
	pairs    []killIdentity
	exePaths []string
}

// NewKillSelector resolves executable identities once for an immutable policy.
func NewKillSelector(users, exes []string) KillSelector {
	s := KillSelector{Users: slices.Clone(users), ExeAny: slices.Clone(exes), exePaths: make([]string, len(exes))}
	for i, exe := range exes {
		s.exePaths[i] = canonicalizePath(exe)
	}
	return s
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
	// KeepChildren is stop_policy.keep_children. Discovery delegates every
	// non-principal process, so a stop waits only for the principals and never
	// signals the rest. Without it every residual must go before a start.
	KeepChildren bool
	// Member reports positive evidence that a residual belongs to what this
	// operation targets: kernel cgroup membership of the service's own init unit
	// for a stop (the authority systemd's KillMode=control-group uses), or the
	// verified processes of one SSH session for its close. A member may receive
	// SIGTERM even without kill_only_if, and SIGKILL with force_kill true or
	// auto. A process keep_children keeps is delegated by discovery, so it never
	// reaches this authority. Nil grants nothing.
	Member func(Process) bool
}

// TermAllowed reports whether residual cleanup may send SIGTERM to proc.
func (p KillPolicy) TermAllowed(proc Process, resolve UserResolver) bool {
	return p.ForceKill && p.KillOnlyIf.Killable(proc, resolve) || p.memberSignallable(proc)
}

// KillAllowed reports whether residual cleanup may escalate proc to SIGKILL.
// kill_only_if needs force_kill; a member of the service's own cgroup needs
// force_kill true or auto, even when auto found no strict identity to pair. A
// service that sets force_kill: false is never killed outright.
func (p KillPolicy) KillAllowed(proc Process, resolve UserResolver) bool {
	if p.ForceKill && p.KillOnlyIf.Killable(proc, resolve) {
		return true
	}
	return (p.ForceKill || p.Automatic) && p.memberSignallable(proc)
}

// memberSignallable applies the membership authority with the same fail-safe
// exclusions as KillSelector.Killable: protected, delegated, blocked and
// unverifiable processes are never signalled. Delivery revalidates the recorded
// generation, executable, real UID and cgroup through the pidfd, so a process
// that changed or left its group is spared.
func (p KillPolicy) memberSignallable(proc Process) bool {
	if !signalCandidate(proc) || proc.ExePrev != "" && proc.ExeFile.Inode == 0 {
		return false
	}
	return p.IsMember(proc)
}

// IsMember reports positive membership evidence for proc: a Member probe that
// accepts it.
func (p KillPolicy) IsMember(proc Process) bool {
	return p.Member != nil && p.Member(proc)
}

// signalCandidate applies the exclusions every signal authority shares: PID 1
// and kernel threads, a recorded refusal, an executable that cannot be resolved,
// and a delegated process — the service's workload, kept alive on purpose by an
// init unit that stops only its main process, reported but never signalled.
func signalCandidate(proc Process) bool {
	return !protectedKillProcess(proc) && !proc.Delegated && proc.SignalBlockReason == "" && proc.signalExecutable() != ""
}

// BlockReason explains why residual cleanup cannot signal p under this policy.
// It shares TermAllowed with signal delivery, so previews cannot grant authority.
func (p KillPolicy) BlockReason(proc Process, resolve UserResolver) string {
	if proc.SignalBlockReason != "" {
		return proc.SignalBlockReason
	}
	if p.memberSignallable(proc) {
		return ""
	}
	if !p.ForceKill {
		return "stop_policy.force_kill disables residual cleanup"
	}
	if proc.ExePrev != "" && proc.ExeFile.Inode == 0 {
		return "deleted executable file could not be verified"
	}
	if !p.KillOnlyIf.Killable(proc, resolve) {
		return "process does not match an authorized executable and real UID"
	}
	return ""
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
		// A delegated selector names processes the service owns but Sermo must
		// never signal, so it contributes no authority at all.
		if !selector.IsStrictPrincipal() {
			continue
		}
		key := killIdentityKey{user: selector.User, exe: selectorExePath(&selector), cmd: selector.Cmd}
		if seen[key] {
			continue
		}
		seen[key] = true
		pairs = append(pairs, killIdentity{killIdentityKey: key, cmdRe: selectorCmdRegexp(&selector)})
	}
	slices.SortFunc(pairs, func(a, b killIdentity) int {
		if result := cmp.Compare(a.user, b.user); result != 0 {
			return result
		}
		if result := cmp.Compare(a.exe, b.exe); result != 0 {
			return result
		}
		return cmp.Compare(a.cmd, b.cmd)
	})
	policy.KillOnlyIf.pairs = pairs
	policy.ForceKill = len(pairs) > 0
	return policy
}

// Killable reports whether p may be signalled. It requires a resolved exe that
// exactly matches an exe_any entry AND a real UID matching a users entry. A
// process with an unreadable exe is never killable; a deleted executable needs
// a verified kernel-held file and ownership. A delegated process is
// never killable, and an empty selector (no users or no exe) matches nothing —
// all fail-safe.
func (s KillSelector) Killable(p Process, resolve UserResolver) bool {
	if !s.Configured() {
		return false
	}
	if !signalCandidate(p) {
		return false
	}
	p.Exe = p.signalExecutable()
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

// ProtectedIdentity reports whether a process must never be a signal target
// nor counted as a host workload: PID 1, or a kernel thread (PID 2 and its
// children, which resolve no executable and carry no command line). It is the
// same predicate every kill gate applies, exported so host-wide inventories
// skip exactly what the reaper refuses.
func ProtectedIdentity(id Identity) bool {
	return id.PID <= 1 || protectedKernelProcess(id.PID, id.PPID, id.ExeOK, id.Cmdline)
}

func protectedKernelProcess(pid, ppid int, exeOK bool, cmdline []string) bool {
	return (pid == 2 || ppid == 2) && !exeOK && len(cmdline) == 0
}

func (s KillSelector) exeMatches(exe string) bool {
	if s.exePaths != nil {
		return slices.Contains(s.exePaths, exe)
	}
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

// StopPolicyFieldPath is the user-facing path of one stop_policy field.
func StopPolicyFieldPath(field string) string {
	return SectionStopPolicy + "." + field
}

// KeepChildren reports the resolved stop_policy.keep_children flag, for the
// discoverers that need it without parsing the whole stop policy.
func KeepChildren(tree map[string]any) bool {
	sp, _ := tree[SectionStopPolicy].(map[string]any)
	keep, _ := sp[StopPolicyKeyKeepChildren].(bool)
	return keep
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
	policy.GracefulTimeout = stopPolicyDuration(sp, StopPolicyKeyGracefulTimeout, &warnings)
	policy.TermTimeout = stopPolicyDuration(sp, StopPolicyKeyTermTimeout, &warnings)
	policy.KillTimeout = stopPolicyDuration(sp, StopPolicyKeyKillTimeout, &warnings)
	if value, present := sp[StopPolicyKeyForceKill]; present {
		if v, ok := value.(bool); ok {
			policy.ForceKill = v
		} else if value == StopPolicyForceKillAuto {
			policy.Automatic = true
		} else {
			warnings = append(warnings, StopPolicyFieldPath(StopPolicyKeyForceKill)+" must be a boolean or \""+StopPolicyForceKillAuto+"\"")
		}
	}
	if value, present := sp[StopPolicyKeyKeepChildren]; present {
		if v, ok := value.(bool); ok {
			policy.KeepChildren = v
		} else {
			warnings = append(warnings, StopPolicyFieldPath(StopPolicyKeyKeepChildren)+" must be a boolean")
		}
	}
	if koi, ok := sp[StopPolicyKeyKillOnlyIf].(map[string]any); ok {
		policy.KillOnlyIf = NewKillSelector(cfgval.StringList(koi[StopPolicyKeyUsers]), cfgval.StringList(koi[StopPolicyKeyExeAny]))
		if !cfgval.IsNonEmptyStringList(koi[StopPolicyKeyUsers]) || !cfgval.IsNonEmptyStringList(koi[StopPolicyKeyExeAny]) {
			warnings = append(warnings, StopPolicyFieldPath(StopPolicyKeyKillOnlyIf)+" must define both "+StopPolicyKeyUsers+" and "+StopPolicyKeyExeAny+", each non-empty")
		}
	} else if policy.ForceKill {
		warnings = append(warnings, StopPolicyFieldPath(StopPolicyKeyForceKill)+"=true requires "+StopPolicyKeyKillOnlyIf)
	}
	return policy, warnings
}

// ParseKillOnlyIf parses one strict paired kill_only_if selector: a mapping of
// exactly users and exe_any, both non-empty. path names the block in the
// warnings. A missing, malformed or half-written selector yields the empty
// selector — which authorizes nothing — never the half it does carry. It is the
// single parser for every explicit kill authorization outside stop_policy (host
// watch kill actions).
func ParseKillOnlyIf(raw any, path string) (KillSelector, []string) {
	koi, ok := raw.(map[string]any)
	if !ok {
		return KillSelector{}, []string{path + " must be a mapping defining both " + StopPolicyKeyUsers + " and " + StopPolicyKeyExeAny}
	}
	var warnings []string
	for _, key := range slices.Sorted(maps.Keys(koi)) {
		if key != StopPolicyKeyUsers && key != StopPolicyKeyExeAny {
			warnings = append(warnings, path+"."+key+" is not supported; it accepts "+StopPolicyKeyUsers+" and "+StopPolicyKeyExeAny)
		}
	}
	selector := NewKillSelector(cfgval.StringList(koi[StopPolicyKeyUsers]), cfgval.StringList(koi[StopPolicyKeyExeAny]))
	if !selector.Configured() {
		// Return the empty selector, not the partial one: a half-written selector
		// must authorize nothing rather than whatever half it does carry.
		return KillSelector{}, append(warnings, path+" must define both "+StopPolicyKeyUsers+" and "+StopPolicyKeyExeAny+", each non-empty")
	}
	return selector, warnings
}

func stopPolicyDuration(stopPolicy map[string]any, key string, warnings *[]string) time.Duration {
	v, present := stopPolicy[key]
	if !present {
		return 0
	}
	d, ok := cfgval.ParseDuration(v)
	if !ok || d <= 0 {
		field := StopPolicyFieldPath(key)
		*warnings = append(*warnings, fmt.Sprintf("%s %q must be a valid positive duration", field, cfgval.String(v)))
		return 0
	}
	return d
}
