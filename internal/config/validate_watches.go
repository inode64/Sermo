package config

import (
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/conn"
	"sermo/internal/emission"
	"sermo/internal/process"
	"sermo/internal/rules"
)

func validateWatches(watches map[string]any, locksDir string, notifiers map[string]struct{}, defaultNotify []string, add func(string, ...any)) {
	for _, name := range slices.Sorted(maps.Keys(watches)) {
		prefix := watchPath(name)
		checkPath := watchCheckPath(name)
		entry, ok := watches[name].(map[string]any)
		if !ok {
			add(validationMappingFormat, prefix)
			continue
		}
		validateWatchMetadata(name, entry, add)
		if mode, present := entry[keyMonitor]; present {
			validateMonitorMode(watchFieldPath(name, keyMonitor), mode, add)
		}
		if v, ok := entry[keyEnabled].(bool); ok && !v {
			continue
		}

		// Entry-level fields are validated before the check so a watch with a
		// missing/invalid check still reports every other problem in one pass.
		validatePositiveDurationField(entry, keyInterval, watchFieldPath(name, keyInterval), add)
		if v, present := entry[keyDryRun]; present {
			if _, ok := v.(bool); !ok {
				add(validationBooleanFormat, watchFieldPath(name, keyDryRun))
			}
		}
		validateSeverityField(prefix, entry, add)
		validateEmission(entry, watchFieldPath(name, emission.Section), add)
		validateNotifyRefs(name, entry, notifiers, add)
		validateWindow(prefix, entry, add)
		validateWatchPolicy(prefix, entry, add)

		check, ok := entry[WatchKeyCheck].(map[string]any)
		if !ok {
			add(validationRequiredFormat, checkPath)
			continue
		}
		validateCheckSummary(checkPath, check, add)
		// A watch's check block never reaches validateCheckSection, so its
		// severity is validated here — before the per-type switch, so net, icmp,
		// swap, file and process are covered too.
		validateSeverityField(checkPath, check, add)
		typ := cfgval.String(check[checks.CheckKeyType])
		validateCheckBands(checkPath, typ, check, add)
		if v, present := check[checks.CheckKeySLA]; present {
			if _, isBool := v.(bool); !isBool {
				add(validationBooleanFormat, checkPath+"."+checks.CheckKeySLA)
			}
		}
		validateRaidNotifyOn(name, typ, entry, notifiers, defaultNotify, add)
		validateRAIDControl(name, typ, entry, check, add)
		validateReplicationControl(name, typ, entry, add)
		validateWatchMountBlock(name, typ, entry, add)
		switch typ {
		case checks.CheckTypeStorage:
			// The one single-shot type with its own case: a storage watch may carry
			// a then.expand action, so its hook block allows expand.
			validateStorageFields(checkPath, check, add)
			validateHookBlock(prefix, entry, watchNativeActions{expand: true, recoverHook: true}, defaultNotify, add)
		case checks.CheckTypeNet:
			validateNetCheck(name, check, entry, defaultNotify, add)
		case checks.CheckTypeICMP:
			validateICMPCheck(name, check, entry, defaultNotify, add)
		case checks.CheckTypeSwap:
			validateSwapCheck(name, entry, defaultNotify, add)
		case checks.CheckTypeFile:
			validateFileCheck(name, check, entry, defaultNotify, add)
		case checks.CheckTypeProcess:
			validateProcessWatch(name, check, entry, defaultNotify, add)
		case checks.CheckTypeProcessPolicy:
			validateProcessPolicyWatch(name, check, entry, defaultNotify, add)
		case "":
			add(validationRequiredFormat, watchCheckFieldPath(name, checks.CheckKeyType))
		default:
			// Any single-shot service check (tcp, http, load, oom, cert, …) can be
			// a host watch: validate its fields with the same per-type validators a
			// checks: section uses and require a hook (section: unified checks).
			if validateWatchableCheck(checkPath, typ, check, locksDir, add) {
				validateHookBlock(prefix, entry, watchNativeActions{makeStep: typ == checks.CheckTypeClock, recoverHook: true}, defaultNotify, add)
			} else {
				add(validationValueNotSupportedFormat, watchCheckFieldPath(name, checks.CheckKeyType), typ)
			}
		}
	}
}

func validateRAIDControl(name, typ string, entry, check map[string]any, add addFunc) {
	prefix := watchFieldPath(name, WatchKeyRAIDControl)
	value, present := entry[WatchKeyRAIDControl]
	if !present {
		return
	}
	control, ok := value.(map[string]any)
	if !ok {
		add(validationMappingFormat, prefix)
		return
	}
	if typ != checks.CheckTypeRAID {
		add("%s is only valid on a raid watch", prefix)
		return
	}
	if cfgval.String(check[checks.CheckKeyArray]) == "" {
		add("%s requires check.%s", prefix, checks.CheckKeyArray)
	}
	for key := range control {
		if key != RAIDControlKeyPauseResume {
			add("%s.%s is not supported", prefix, key)
		}
	}
	if value, found := control[RAIDControlKeyPauseResume]; !found {
		add("%s.%s is required", prefix, RAIDControlKeyPauseResume)
	} else if _, ok := value.(bool); !ok {
		add(validationBooleanFormat, prefix+"."+RAIDControlKeyPauseResume)
	}
}

// validateReplicationControl accepts replication_control only on a replication
// watch, with exactly the boolean start key.
func validateReplicationControl(name, typ string, entry map[string]any, add addFunc) {
	prefix := watchFieldPath(name, WatchKeyReplicationControl)
	value, present := entry[WatchKeyReplicationControl]
	if !present {
		return
	}
	control, ok := value.(map[string]any)
	if !ok {
		add(validationMappingFormat, prefix)
		return
	}
	if typ != checks.CheckTypeReplication {
		add("%s is only valid on a replication watch", prefix)
		return
	}
	for key := range control {
		if key != ReplicationControlKeyStart {
			add("%s.%s is not supported", prefix, key)
		}
	}
	if value, found := control[ReplicationControlKeyStart]; !found {
		add("%s.%s is required", prefix, ReplicationControlKeyStart)
	} else if _, ok := value.(bool); !ok {
		add(validationBooleanFormat, prefix+"."+ReplicationControlKeyStart)
	}
}

func validateWatchMountBlock(name, typ string, entry map[string]any, add func(string, ...any)) {
	mountPath := watchFieldPath(name, keyMount)
	mount, ok := entry[keyMount].(map[string]any)
	if _, present := entry[keyMount]; !present {
		return
	}
	if !ok {
		add(validationMappingFormat, mountPath)
		return
	}
	if typ != checks.CheckTypeStorage {
		add("%s is only valid on a storage watch", mountPath)
		return
	}
	validateStorageMount(mount, func(format string, args ...any) {
		add(watchPath(name)+"."+format, args...)
	})
}

// validateServiceWatches validates a service tree's embedded `watches:` section.
// Fire-and-forget entries run the host-watch runtime with the service's scoped
// check deps, so service-scoped service/metric/process_count checks are permitted
// here while the host-wide process watch and net/icmp/swap multi-metric watches
// are rejected. Entries without then are desugared to checks:, and entries with
// then.action are desugared to checks:+rules:, by expandServiceWatches; this pass
// still validates their grammar when it sees an unexpanded tree. Per-type field
// grammar is shared with host watches.
func validateServiceWatches(tree map[string]any, locksDir string, notifiers map[string]struct{}, defaultNotify []string, add func(string, ...any)) {
	watches, ok := tree[sectionWatches].(map[string]any)
	if !ok {
		if _, present := tree[sectionWatches]; present {
			add("watches must be a mapping of named watches")
		}
		return
	}
	for _, name := range slices.Sorted(maps.Keys(watches)) {
		entry, ok := watches[name].(map[string]any)
		if !ok {
			add(validationMappingFormat, watchPath(name))
			continue
		}
		validateServiceWatch(name, entry, locksDir, notifiers, defaultNotify, add)
	}
}

func validateServiceWatch(name string, entry map[string]any, locksDir string, notifiers map[string]struct{}, defaultNotify []string, add addFunc) {
	prefix := watchPath(name)
	if name == ServiceMonitorKeyVersion || name == ServiceMonitorKeyConfig {
		add("%s name is reserved for the version/config monitor; rename it", prefix)
		return
	}
	if v, ok := entry[keyEnabled].(bool); ok && !v {
		return
	}
	validateServiceWatchEntry(name, entry, notifiers, add)
	checkPath := watchCheckPath(name)
	check, ok := entry[WatchKeyCheck].(map[string]any)
	if !ok {
		add(validationRequiredFormat, checkPath)
		return
	}
	validateCheckSummary(checkPath, check, add)
	typ := cfgval.String(check[checks.CheckKeyType])
	if typ == "" {
		add(validationRequiredFormat, watchCheckFieldPath(name, checks.CheckKeyType))
		return
	}
	rawThen, hasThen := entry[rules.RuleFieldThen]
	then, _ := rawThen.(map[string]any)
	if !hasThen {
		if !validateSingleShotCheckFields(checkPath, typ, check, locksDir, add) {
			add(validationValueNotSupportedFormat, watchCheckFieldPath(name, checks.CheckKeyType), typ)
		}
		return
	}
	if !validateServiceWatchType(name, typ, checkPath, check, locksDir, add) {
		return
	}
	if action := cfgval.String(then[rules.RuleFieldAction]); action != "" {
		validateWatchThenAction(prefix, action, then, add)
		return
	}
	validateHookBlock(prefix, entry, watchNativeActions{expand: typ == checks.CheckTypeStorage, makeStep: typ == checks.CheckTypeClock, recoverHook: true}, defaultNotify, add)
}

func validateServiceWatchEntry(name string, entry map[string]any, notifiers map[string]struct{}, add addFunc) {
	prefix := watchPath(name)
	validateWatchMetadata(name, entry, add)
	if mode, present := entry[keyMonitor]; present {
		validateMonitorMode(watchFieldPath(name, keyMonitor), mode, add)
	}
	validatePositiveDurationField(entry, keyInterval, watchFieldPath(name, keyInterval), add)
	if v, present := entry[keyDryRun]; present {
		if _, ok := v.(bool); !ok {
			add(validationBooleanFormat, watchFieldPath(name, keyDryRun))
		}
	}
	validateEmission(entry, watchFieldPath(name, emission.Section), add)
	if then, ok := entry[rules.RuleFieldThen].(map[string]any); ok {
		if _, present := then[rules.RuleFieldNotify]; present {
			validateNotifySelection(thenFieldPath(prefix, rules.RuleFieldNotify), then[rules.RuleFieldNotify], notifiers, add)
		}
	}
	validateWindow(prefix, entry, add)
	validateWatchPolicy(prefix, entry, add)
}

func validateServiceWatchType(name, typ, checkPath string, check map[string]any, locksDir string, add addFunc) bool {
	checkTypePath := watchCheckFieldPath(name, checks.CheckKeyType)
	switch {
	case typ == checks.CheckTypeNet || typ == checks.CheckTypeICMP || typ == checks.CheckTypeSwap:
		add("%s %q is host-scoped; declare it under the global watches: section", checkTypePath, typ)
		return false
	case typ == checks.CheckTypeProcess:
		add("%s \"process\" matches host-wide (and can kill); use process_count or metric for service-scoped process monitoring, or a host watch", checkTypePath)
		return false
	case !serviceWatchableType(typ):
		add(validationValueNotSupportedFormat, checkTypePath, typ)
		return false
	}
	validateSingleShotCheckFields(checkPath, typ, check, locksDir, add)
	return true
}

// isRuleClassAction reports whether a then.action turns a service watch into a
// checks:+rules: desugar target — an operation (restart/start/stop/reload/resume),
// a guard (block) or an alert — rather than a fire-and-forget hook/notify/expand/kill
// watch.
func isRuleClassAction(action string) bool {
	return rules.ActionType(action).Valid()
}

// validateWatchThenAction validates a unified service watch whose then declares a
// rule-class action. It desugars to a generated check + rule, so its then accepts
// action/message/blocks/notify but not fire-and-forget hook/expand/kill side
// effects or watch-only notification cadence.
func validateWatchThenAction(prefix, action string, then map[string]any, add func(string, ...any)) {
	if !isRuleClassAction(action) {
		add(validationNotOneOfFormat, thenFieldPath(prefix, rules.RuleFieldAction), action, rules.RuleActionSummary)
		return
	}
	for _, k := range []string{WatchThenKeyHook, WatchThenKeyExpand, WatchThenKeyKill, WatchThenKeyMakeStep} {
		if _, has := then[k]; has {
			add("%s cannot be combined with an action (a watch is either an operation/alert or a fire-and-forget %s)", thenFieldPath(prefix, k), k)
		}
	}
	allowed := set(rules.RuleFieldAction, rules.RuleFieldMessage, rules.RuleFieldBlocks, rules.RuleFieldNotify)
	for _, k := range slices.Sorted(maps.Keys(then)) {
		if _, ok := allowed[k]; !ok {
			add("%s is not supported with an action", thenFieldPath(prefix, k))
		}
	}
	if action == string(rules.ActionBlock) {
		if _, hasNotify := then[rules.RuleFieldNotify]; hasNotify {
			add("%s is not supported with action: block; guard rules do not notify", thenFieldPath(prefix, rules.RuleFieldNotify))
		}
		if cfgval.String(then[rules.RuleFieldMessage]) == "" {
			add("%s is required with action: block", thenFieldPath(prefix, rules.RuleFieldMessage))
		}
		blocks := cfgval.StringList(then[rules.RuleFieldBlocks])
		if len(blocks) == 0 {
			add("%s requires a non-empty blocks: [list of actions] for a block (guard) action", prefix+"."+rules.RuleFieldThen)
		}
		for _, b := range blocks {
			if !rules.ActionType(b).IsOperation() {
				add("%s entry %q must be an operation action (restart/start/stop/reload/resume)", thenFieldPath(prefix, rules.RuleFieldBlocks), b)
			}
		}
	} else if action == string(rules.ActionAlert) {
		if cfgval.String(then[rules.RuleFieldMessage]) == "" {
			add("%s is required with action: alert", thenFieldPath(prefix, rules.RuleFieldMessage))
		}
	} else if _, hasBlocks := then[rules.RuleFieldBlocks]; hasBlocks {
		add("%s is only valid with action: block", thenFieldPath(prefix, rules.RuleFieldBlocks))
	}
}

// serviceWatchableType reports whether typ can back a service-embedded watch: any
// built-in single-shot type (including the service-scoped service/metric types,
// which have per-service deps here) or a connection protocol. The host-scoped
// multi-metric types (net/icmp/swap) and the host-wide `process` watch are
// rejected by the caller before this is reached.
func serviceWatchableType(typ string) bool {
	if checks.IsSingleShotType(typ) {
		return true
	}
	_, ok := conn.Lookup(typ)
	return ok
}

func validateWatchMetadata(name string, entry map[string]any, add func(string, ...any)) {
	for _, key := range []string{keyDisplayName, keyDescription, keyCategory} {
		if v, present := entry[key]; present {
			if _, ok := v.(string); !ok {
				add("%s must be a string", watchFieldPath(name, key))
			}
		}
	}
}

// validateWatchMakeStepAction checks a clock watch's `then.makestep` action: the
// forced clock correction Sermo issues natively over chronyd's command socket.
// Unlike then.expand it REQUIRES a positive watch-level policy.cooldown (safety
// invariant 8) — a zero policy allows an action on every cycle, and a clock step
// is a discontinuity, not an idempotent nudge.
func validateWatchMakeStepAction(prefix string, block, then map[string]any, allow bool, add func(string, ...any)) bool {
	raw, present := then[WatchThenKeyMakeStep]
	step, hasStep := raw.(map[string]any)
	switch {
	case present && !hasStep:
		add("%s must be a mapping with an optional %s", thenMakeStepPath(prefix), WatchMakeStepKeySocket)
	case hasStep && !allow:
		add("%s is only valid on a clock watch", thenMakeStepPath(prefix))
	case hasStep:
		// Reject rather than ignore an unknown key: a host/port here would look
		// like the daemon was addressed that way, and it cannot be — chronyd's
		// UDP port refuses privileged commands.
		for _, key := range slices.Sorted(maps.Keys(step)) {
			if key != WatchMakeStepKeySocket {
				add(validationNotSupportedFormat, thenMakeStepPath(prefix)+"."+key)
			}
		}
		if socket := cfgval.String(step[WatchMakeStepKeySocket]); socket != "" && !filepath.IsAbs(socket) {
			add("%s %q must be an absolute path to chronyd's command socket",
				thenMakeStepPath(prefix)+"."+WatchMakeStepKeySocket, socket)
		}
		policy, _ := block[sectionPolicy].(map[string]any)
		if !isPositiveDuration(cfgval.String(policy[rules.PolicyKeyCooldown])) {
			add("%s requires %s as a positive duration: a forced clock step must be paced",
				thenMakeStepPath(prefix), prefix+"."+policyPathCooldown)
		}
	}
	return hasStep
}

// watchNativeActions names the native then-actions a watch type permits. A
// struct rather than three adjacent booleans, which no call site could read.
type watchNativeActions struct {
	expand   bool
	kill     bool
	makeStep bool
	// recoverHook admits then.recover_hook: only single-check watches carry the
	// failed-to-ok edge it runs on.
	recoverHook bool
}

// validateHookBlock validates a `then` action block: a hook and/or a notify list
// (at least one), or a storage-only expand action. The hook command (when present)
// must be a non-empty array with a valid optional timeout. Notifier-name
// references are checked separately by validateNotifyRefs (which has the
// configured notifier set).
func validateHookBlock(prefix string, block map[string]any, allow watchNativeActions, defaultNotify []string, add func(string, ...any)) {
	then, ok := watchThenMapping(prefix, block, add)
	if !ok {
		return
	}
	validateWatchThenKeys(prefix, then, add)
	hook, hasHook := then[WatchThenKeyHook].(map[string]any)
	recoverHook, hasRecoverHook := then[WatchThenKeyRecoverHook].(map[string]any)
	if _, present := then[WatchThenKeyRecoverHook]; present && !allow.recoverHook {
		add("%s is only supported on single-check watches", thenFieldPath(prefix, WatchThenKeyRecoverHook))
		hasRecoverHook = false
	}
	notify := cfgval.StringList(then[rules.RuleFieldNotify])
	_, hasNotifyOn := then[WatchThenKeyNotifyOn]
	validateWatchNotifyInterval(prefix, then, hasNotifyOn, notify, defaultNotify, add)
	hasExpand := validateWatchExpandAction(prefix, then, allow.expand, add)
	hasKill := validateWatchKillAction(prefix, then, allow.kill, add)
	hasMakeStep := validateWatchMakeStepAction(prefix, block, then, allow.makeStep, add)
	// An explicit `then: { notify: [none] }` (or with a hook/expand/kill too) is a
	// deliberate monitor-only watch (state in the dashboard and events, no
	// delivery). A present `then` that selects nothing is rejected. Omitting the
	// `then` key entirely is another supported way to get alert-only behavior.
	if !hasHook && !hasRecoverHook && !hasNotifyOn && !HasEffectiveNotifyAction(notify, defaultNotify) && !hasExpand && !hasKill && !hasMakeStep && !NotifyOptedOut(notify) {
		add("%s requires a hook, notify, kill, expand and/or makestep", prefix+"."+rules.RuleFieldThen)
		return
	}
	validateWatchHookAction(prefix, hook, hasHook, add)
	validateWatchRecoverHookAction(prefix, recoverHook, hasRecoverHook, add)
}

// validateWatchRecoverHookAction validates then.recover_hook with the same
// shape rules as then.hook.
func validateWatchRecoverHookAction(prefix string, hook map[string]any, hasHook bool, add func(string, ...any)) {
	if !hasHook {
		return
	}
	path := thenFieldPath(prefix, WatchThenKeyRecoverHook)
	if !cfgval.IsNonEmptyStringArray(hook[WatchHookKeyCommand]) {
		add("%s must be a non-empty array", path+"."+WatchHookKeyCommand)
	}
	validatePositiveDurationField(hook, WatchHookKeyTimeout, path+"."+WatchHookKeyTimeout, add)
	validateCommandExpectations(path, hook, add)
}

func watchThenMapping(prefix string, block map[string]any, add func(string, ...any)) (map[string]any, bool) {
	rawThen, present := block[rules.RuleFieldThen]
	if !present {
		// Absent `then` is valid: the watch is alert/monitor-only. Its `check` +
		// `for` (or per-metric conditions) still produces firing state and events,
		// but does not inherit global notifications or run a hook.
		return nil, false
	}
	then, ok := rawThen.(map[string]any)
	if !ok {
		add(validationMappingFormat, prefix+"."+rules.RuleFieldThen)
	}
	return then, ok
}

func validateWatchThenKeys(prefix string, then map[string]any, add func(string, ...any)) {
	allowed := set(WatchThenKeyHook, WatchThenKeyRecoverHook, rules.RuleFieldNotify, WatchThenKeyNotifyInterval, WatchThenKeyNotifyOn, WatchThenKeyExpand, WatchThenKeyKill, WatchThenKeyMakeStep)
	for _, key := range slices.Sorted(maps.Keys(then)) {
		if _, ok := allowed[key]; !ok {
			add(validationNotSupportedFormat, thenFieldPath(prefix, key))
		}
	}
}

func validateWatchNotifyInterval(prefix string, then map[string]any, hasNotifyOn bool, notify, defaultNotify []string, add func(string, ...any)) {
	v, present := then[WatchThenKeyNotifyInterval]
	if !present {
		return
	}
	// notify_interval re-sends while a watch remains firing. It requires targets
	// and cannot be combined with event-specific notification routing.
	switch {
	case !isPositiveDuration(cfgval.String(v)):
		add(validationPositiveDurationFormat, thenFieldPath(prefix, WatchThenKeyNotifyInterval), cfgval.String(v))
	case hasNotifyOn:
		add("%s is not supported with %s", thenFieldPath(prefix, WatchThenKeyNotifyInterval), thenFieldPath(prefix, WatchThenKeyNotifyOn))
	case !HasEffectiveNotifyAction(notify, defaultNotify):
		add("%s has no effect without notify targets", thenFieldPath(prefix, WatchThenKeyNotifyInterval))
	}
}

func validateWatchExpandAction(prefix string, then map[string]any, allowExpand bool, add func(string, ...any)) bool {
	rawExpand, present := then[WatchThenKeyExpand]
	expand, hasExpand := rawExpand.(map[string]any)
	switch {
	case present && !hasExpand:
		add("%s must be a mapping with a `by` size", thenFieldPath(prefix, WatchThenKeyExpand))
	case hasExpand && !allowExpand:
		add("%s is only valid on a storage watch", thenFieldPath(prefix, WatchThenKeyExpand))
	case hasExpand:
		if by, ok := cfgval.ByteSize(expand[WatchExpandKeyBy]); !ok || by == 0 {
			add("%s %q must be a positive size with a K/M/G/T suffix (e.g. 5G)", thenFieldPath(prefix, WatchThenKeyExpand)+"."+WatchExpandKeyBy, cfgval.String(expand[WatchExpandKeyBy]))
		}
	}
	return hasExpand
}

func validateWatchKillAction(prefix string, then map[string]any, allowKill bool, add func(string, ...any)) bool {
	rawKill, present := then[WatchThenKeyKill]
	kill, hasKill := rawKill.(map[string]any)
	switch {
	case present && !hasKill:
		add(validationMappingFormat, thenKillPath(prefix))
	case hasKill && !allowKill:
		add("%s is only valid on a process watch", thenKillPath(prefix))
	case hasKill:
		validateKillAction(prefix, kill, add)
	}
	return hasKill
}

func validateWatchHookAction(prefix string, hook map[string]any, hasHook bool, add func(string, ...any)) {
	if !hasHook {
		return
	}
	if !cfgval.IsNonEmptyStringArray(hook[WatchHookKeyCommand]) {
		add("%s must be a non-empty array", thenHookPath(prefix)+"."+WatchHookKeyCommand)
	}
	validatePositiveDurationField(hook, WatchHookKeyTimeout, thenHookPath(prefix)+"."+WatchHookKeyTimeout, add)
	validateCommandExpectations(thenHookPath(prefix), hook, add)
}

// validateRaidNotifyOn validates the RAID-only event-specific notification
// mapping. It deliberately stays separate from then.notify: the latter keeps
// its existing firing-episode semantics.
func validateRaidNotifyOn(name, typ string, entry map[string]any, notifiers map[string]struct{}, defaultNotify []string, add addFunc) {
	then, ok := entry[rules.RuleFieldThen].(map[string]any)
	if !ok {
		return
	}
	raw, present := then[WatchThenKeyNotifyOn]
	if !present {
		return
	}
	prefix := thenFieldPath(watchPath(name), WatchThenKeyNotifyOn)
	events, err := cfgval.StrictStringList(raw)
	if err != nil || len(events) == 0 {
		add("%s must be a non-empty event list", prefix)
		return
	}
	allowed := set(checks.RaidNotifyEvents...)
	if typ == checks.CheckTypeLVM {
		allowed = set(checks.LVMNotifyOnChange)
	}
	if typ != checks.CheckTypeRAID && typ != checks.CheckTypeLVM {
		add("%s is only valid on a raid or lvm watch", prefix)
		return
	}
	for _, event := range events {
		if _, ok := allowed[event]; !ok {
			add(validationValueNotSupportedFormat, prefix, event)
		}
	}
	if notify, present := then[rules.RuleFieldNotify]; present {
		validateNotifySelection(thenFieldPath(watchPath(name), rules.RuleFieldNotify), notify, notifiers, add)
	}
	if !HasEffectiveNotifyAction(cfgval.StringList(then[rules.RuleFieldNotify]), defaultNotify) {
		add("%s requires notify targets", prefix)
	}
}

// validateKillAction checks a process watch's `then.kill` action: an optional
// signal (TERM default, or KILL — validated by the same process.ParseKillSignal
// the daemon uses), an optional boolean `escalate`, and the optional grace
// durations that only apply when escalating.
func validateKillAction(prefix string, kill map[string]any, add func(string, ...any)) {
	if s := cfgval.String(kill[WatchKillKeySignal]); s != "" {
		if _, err := process.ParseKillSignal(s); err != nil {
			add("%s %q must be %s", thenKillPath(prefix)+"."+WatchKillKeySignal, s, process.KillSignalSummary)
		}
	}
	if v, present := kill[WatchKillKeyEscalate]; present {
		if _, ok := v.(bool); !ok {
			add(validationBooleanFormat, thenKillPath(prefix)+"."+WatchKillKeyEscalate)
		}
	}
	for _, f := range []string{WatchKillKeyTermTimeout, WatchKillKeyKillTimeout} {
		validatePositiveDurationField(kill, f, thenKillPath(prefix)+"."+f, add)
	}
}

// validateWatchPolicy checks a watch-level `policy` block — the pacing for a
// firing watch's actions (notably then.expand): an optional positive cooldown
// plus the same max_actions/backoff extras a service policy allows. Unlike a
// service, a watch does not generally require a cooldown; absent means "fire
// every cycle". then.makestep is the exception and requires one, enforced by
// validateWatchMakeStepAction.
func validateWatchPolicy(prefix string, entry map[string]any, add addFunc) {
	raw, present := entry[sectionPolicy]
	if !present {
		return
	}
	policy, ok := raw.(map[string]any)
	if !ok {
		add("%s.policy must be a mapping", prefix)
		return
	}
	padd := func(format string, args ...any) { add(prefix+"."+format, args...) }
	validatePositiveDurationField(policy, rules.PolicyKeyCooldown, prefix+"."+policyPathCooldown, add)
	validatePolicyExtras(entry, padd)
}

// HasNotifyAction reports whether names selects at least one real notifier — a
// non-empty selection that is not the `none` sentinel. Shared by config
// validation, the daemon's watch builder and the wizard so the notify-selection
// rule lives in one place.
func HasNotifyAction(names []string) bool {
	return len(names) > 0 && !NotifyOptedOut(names)
}

// NotifyOptedOut reports whether a notify selection is the explicit `none`
// opt-out. Unlike an empty selection (which inherits the global default and is
// inert without one), the sentinel is a deliberate "monitor but never deliver"
// choice — valid anywhere a notify list is, even with no other action.
func NotifyOptedOut(names []string) bool {
	return slices.Contains(names, NotifyNone)
}

// HasEffectiveNotifyAction reports whether a watch ends up delivering to a
// notifier: an explicit selection (HasNotifyAction), or an omitted selection that
// inherits a non-empty defaultNotify. The `none` sentinel always suppresses
// delivery.
func HasEffectiveNotifyAction(names, defaultNotify []string) bool {
	if NotifyOptedOut(names) {
		return false
	}
	return HasNotifyAction(names) || (len(names) == 0 && len(defaultNotify) > 0)
}

// validateMetricWatchEntry rejects entry-level then/for/within on a multi-metric
// watch (net, icmp, swap): those fields belong inside each metric's own block.
func validateMetricWatchEntry(name string, entry map[string]any, add func(string, ...any)) {
	validateInvalidWatchEntryFields(name, "multi-metric", entry, []string{rules.RuleFieldThen, rules.RuleFieldFor, rules.RuleFieldWithin, rules.RuleFieldClear}, "metrics.<name>.%s", add)
}

// validateStatefulWatchEntry rejects entry-level for/within on a file or process
// watch: these use internal per-path/per-PID state and never read the shared
// rules window fields at the entry level.
func validateStatefulWatchEntry(name, typ string, entry map[string]any, add func(string, ...any)) {
	validateInvalidWatchEntryFields(name, typ, entry, []string{rules.RuleFieldFor, rules.RuleFieldWithin, rules.RuleFieldClear}, "", add)
}

func validateInvalidWatchEntryFields(name, typ string, entry map[string]any, keys []string, moveHint string, add func(string, ...any)) {
	for _, key := range keys {
		if _, present := entry[key]; present {
			msg := fmt.Sprintf("%s is not valid on a %s watch", watchFieldPath(name, key), typ)
			if moveHint != "" {
				msg += fmt.Sprintf("; move it into the metric's own block ("+moveHint+")", key)
			}
			add("%s", msg)
		}
	}
}

// validateNetCheck validates a net interface watch: an interface and a non-empty
// metrics map, each metric with a valid condition and its own hook.
func validateNetCheck(name string, check, entry map[string]any, defaultNotify []string, add func(string, ...any)) {
	validateMetricWatchEntry(name, entry, add)
	if cfgval.String(check[checks.CheckKeyInterface]) == "" {
		add("%s is required for a net check", watchCheckFieldPath(name, checks.CheckKeyInterface))
	}
	validateMetricWatchEntries(name, "net", entry, defaultNotify, validateNetMetricCondition, add)
}

func validateMetricWatchEntries(name, typ string, entry map[string]any, defaultNotify []string, validateCondition func(string, string, map[string]any, addFunc), add addFunc) {
	watchMetrics, ok := entry[sectionMetrics].(map[string]any)
	if !ok || len(watchMetrics) == 0 {
		add("%s is required and must be non-empty for a %s check", watchMetricsPath(name), typ)
		return
	}
	for _, key := range slices.Sorted(maps.Keys(watchMetrics)) {
		prefix := watchMetricPath(name, key)
		metric, ok := watchMetrics[key].(map[string]any)
		if !ok {
			add(validationMappingFormat, prefix)
			continue
		}
		validateCondition(prefix, key, metric, add)
		validateSeverityField(prefix, metric, add)
		validateEmission(metric, prefix+"."+emission.Section, add)
		validateHookBlock(prefix, metric, watchNativeActions{}, defaultNotify, add)
		validateWindow(prefix, metric, add)
	}
}

// validateNetMetricCondition validates one net metric's condition fields:
// state (expect/on), speed (on: change) or errors (delta + optional counters).
// Shared by the multi-metric net watch and the single-shot net check, so the
// condition grammar cannot drift between the two surfaces.
func validateNetMetricCondition(prefix, metric string, m map[string]any, add addFunc) {
	switch metric {
	case checks.NetMetricState:
		validateStateMetric(prefix, m, add)
	case checks.NetMetricSpeed:
		if cfgval.String(m[checks.CheckKeyOn]) != checks.OnModeChange {
			add("%s requires on: change", prefix)
		}
	case checks.NetMetricErrors:
		delta, ok := m[checks.CheckKeyDelta].(map[string]any)
		if !ok {
			add("%s.delta {op, value} is required", prefix)
		} else {
			validateOpNumeric(prefix+"."+checks.CheckKeyDelta, delta, add)
		}
		if c, present := m[checks.CheckKeyCounters]; present {
			if !cfgval.IsNonEmptyStringArray(c) {
				add("%s.counters must be a non-empty list", prefix)
			}
		}
	case checks.NetMetricAddress:
		exp := cfgval.String(m[checks.CheckKeyExpect])
		onChange := cfgval.String(m[checks.CheckKeyOn]) == checks.OnModeChange
		if exp == "" && !onChange {
			add("%s requires expect: present|absent or on: change", prefix)
		} else if exp != "" && exp != checks.NetAddrPresent && exp != checks.NetAddrAbsent {
			add("%s.expect must be %s", prefix, checks.NetAddrSummary)
		}
	default:
		add("%s is not a supported net metric (%s)", prefix, checks.NetMetricSummary)
	}
}

// validateWatchableCheck validates the fields of a single-shot service check used
// as a host watch and reports whether the type is watchable. service/metric/
// process are excluded: they need per-service context (backend status, a metric
// sampler, process discovery) that the watch builder does not provide.
func validateWatchableCheck(prefix, typ string, fields map[string]any, locksDir string, add addFunc) bool {
	if checks.IsServiceScopedType(typ) {
		return false
	}
	return validateSingleShotCheckFields(prefix, typ, fields, locksDir, add)
}

// validateSwapCheck validates a swap watch: a non-empty metrics map of usage
// (used_pct/free_pct/free_bytes thresholds) and/or io (per-cycle delta), each
// with its own hook (mirrors validateNetCheck).
func validateSwapCheck(name string, entry map[string]any, defaultNotify []string, add func(string, ...any)) {
	validateMetricWatchEntry(name, entry, add)
	validateMetricWatchEntries(name, "swap", entry, defaultNotify, validateSwapMetricCondition, add)
}

// validateSwapMetricCondition validates one swap metric's condition fields:
// usage (level thresholds) or io (per-cycle delta). Shared by the multi-metric
// swap watch and the single-shot swap check, so the condition grammar cannot
// drift between the two surfaces.
func validateSwapMetricCondition(prefix, metric string, m map[string]any, add addFunc) {
	switch metric {
	case checks.SwapMetricUsage:
		validateThresholdPreds(prefix, m, checks.SwapUsageFields, add)
	case checks.SwapMetricIO:
		delta, ok := m[checks.CheckKeyDelta].(map[string]any)
		if !ok {
			add("%s.delta {op, value} is required", prefix)
		} else {
			validateOpNumeric(prefix+"."+checks.CheckKeyDelta, delta, add)
		}
	default:
		add("%s is not a supported swap metric (%s)", prefix, checks.SwapMetricSummary)
	}
}

// validateStateMetric validates a state metric condition shared by net/icmp:
// expect up|down OR on: change.
func validateStateMetric(prefix string, m map[string]any, add func(string, ...any)) {
	exp := cfgval.String(m[checks.CheckKeyExpect])
	onChange := cfgval.String(m[checks.CheckKeyOn]) == checks.OnModeChange
	if exp == "" && !onChange {
		add("%s requires expect: up|down or on: change", prefix)
	} else if exp != "" && exp != checks.NetStateUp && exp != checks.NetStateDown {
		add("%s.expect must be %s", prefix, checks.NetStateSummary)
	}
}

// validateICMPCheck validates an icmp host watch: a host (+ optional positive
// count) and a non-empty metrics map, each metric with a valid condition and its
// own hook.
func validateICMPCheck(name string, check, entry map[string]any, defaultNotify []string, add func(string, ...any)) {
	validateMetricWatchEntry(name, entry, add)
	if cfgval.String(check[checks.CheckKeyHost]) == "" {
		add("%s is required for an icmp check", watchCheckFieldPath(name, checks.CheckKeyHost))
	}
	validatePositiveIntField(check, checks.CheckKeyCount, watchCheckFieldPath(name, checks.CheckKeyCount), add)
	validateMetricWatchEntries(name, "icmp", entry, defaultNotify, validateICMPMetricCondition, add)
}

// validateICMPMetricCondition validates one icmp metric's condition fields:
// state (expect/on) or latency (threshold xor change). Shared by the
// multi-metric icmp watch and the single-shot icmp check, so the condition
// grammar cannot drift between the two surfaces.
func validateICMPMetricCondition(prefix, metric string, m map[string]any, add addFunc) {
	switch metric {
	case checks.NetMetricState:
		validateStateMetric(prefix, m, add)
	case checks.IcmpMetricLatency:
		th, hasT := m[checks.CheckKeyThreshold].(map[string]any)
		ch, hasC := m[checks.CheckKeyChange].(map[string]any)
		if !hasT && !hasC {
			add("%s requires threshold {op, value} or change {delta}", prefix)
		}
		if hasT && hasC {
			add("%s must set only one of threshold or change", prefix)
		}
		if hasT {
			validateOpNumeric(prefix+".threshold", th, add)
		}
		if hasC {
			if !isNumeric(cfgval.String(ch[checks.CheckKeyDelta])) {
				add("%s.change delta %q must be numeric", prefix, cfgval.String(ch[checks.CheckKeyDelta]))
			}
		}
	default:
		add("%s is not a supported icmp metric (%s)", prefix, checks.ICMPMetricSummary)
	}
}

// validateFileCheck validates a file watch: path or paths, optional recursive
// traversal flags, and at least one attribute condition (size threshold/change,
// permissions/owner on change, existence on delete, older_than), plus the entry's hook.
func validateFileCheck(name string, check, entry map[string]any, defaultNotify []string, add func(string, ...any)) {
	validateStatefulWatchEntry(name, checks.CheckTypeFile, entry, add)
	if _, err := FileWatchPaths(check); err != nil {
		add("%s: %s", watchCheckPath(name), err)
	}
	if v, present := check[checks.CheckKeyRecursive]; present {
		if _, ok := v.(bool); !ok {
			add(validationBooleanFormat, watchCheckFieldPath(name, checks.CheckKeyRecursive))
		}
	}
	if v, present := check[checks.CheckKeyIncludeHidden]; present {
		if _, ok := v.(bool); !ok {
			add(validationBooleanFormat, watchCheckFieldPath(name, checks.CheckKeyIncludeHidden))
		}
	}
	if v, present := check[checks.CheckKeyAbsentOK]; present {
		if _, ok := v.(bool); !ok {
			add(validationBooleanFormat, watchCheckFieldPath(name, checks.CheckKeyAbsentOK))
		}
	}

	conds := 0
	if sz, ok := check[checks.CheckKeySize].(map[string]any); ok {
		conds++
		if cfgval.String(sz[checks.CheckKeyOn]) != checks.OnModeChange {
			if !isValidCompareOp(cfgval.String(sz[checks.CheckKeyOp])) || !isNumeric(cfgval.String(sz[checks.CheckKeyValue])) {
				add("%s requires on: change or {op, value} with a numeric value", watchCheckFieldPath(name, checks.CheckKeySize))
			}
		}
	}
	for _, attr := range []string{checks.CheckKeyPermissions, checks.CheckKeyOwner} {
		if m, ok := check[attr].(map[string]any); ok {
			conds++
			if cfgval.String(m[checks.CheckKeyOn]) != checks.OnModeChange {
				add("%s requires on: change", watchCheckFieldPath(name, attr))
			}
		}
	}
	if e, ok := check[checks.CheckKeyExistence].(map[string]any); ok {
		conds++
		if cfgval.String(e[checks.CheckKeyOn]) != checks.OnModeDelete {
			add("%s requires on: delete", watchCheckFieldPath(name, checks.CheckKeyExistence))
		}
	}
	if v, present := check[checks.CheckKeyOlderThan]; present {
		conds++
		if !isPositiveDuration(cfgval.String(v)) {
			add("%s must be a valid positive duration", watchCheckFieldPath(name, checks.CheckKeyOlderThan))
		}
	}
	if conds == 0 {
		add("%s requires at least one of %s", watchCheckPath(name), FileWatchConditionSummary)
	}

	validateHookBlock(watchPath(name), entry, watchNativeActions{}, defaultNotify, add)
}

// validateProcessWatch validates a process watch: a name, an optional user, and
// at least one condition (for duration, or cpu/memory/io {op, value}), plus the
// entry's hook.
func validateProcessWatch(name string, check, entry map[string]any, defaultNotify []string, add func(string, ...any)) {
	validateStatefulWatchEntry(name, checks.CheckTypeProcess, entry, add)
	if cfgval.String(check[checks.CheckKeyName]) == "" {
		add("%s is required for a process check", watchCheckFieldPath(name, checks.CheckKeyName))
	}
	conds := 0
	if v, present := check[checks.CheckKeyFor]; present {
		conds++
		if !isPositiveDuration(cfgval.String(v)) {
			add(validationPositiveDurationFormat, watchCheckFieldPath(name, checks.CheckKeyFor), cfgval.String(v))
		}
	}
	for _, attr := range checks.ProcessWatchPredFields {
		m, ok := check[attr].(map[string]any)
		if !ok {
			continue
		}
		conds++
		if !isValidCompareOp(cfgval.String(m[checks.CheckKeyOp])) || !isNumeric(cfgval.String(m[checks.CheckKeyValue])) {
			add("%s requires {op, value} with a numeric value", watchCheckFieldPath(name, attr))
		}
	}
	if v, present := check[checks.CheckKeyGone]; present {
		if b, ok := v.(bool); !ok {
			add(validationBooleanFormat, watchCheckFieldPath(name, checks.CheckKeyGone))
		} else if b {
			conds++
		}
	}
	if conds == 0 {
		add("%s requires at least one of %s", watchCheckPath(name), ProcessWatchConditionSummary)
	}

	// A process watch is the one type that may carry a native `then.kill` action.
	validateHookBlock(watchPath(name), entry, watchNativeActions{kill: true}, defaultNotify, add)
	validateProcessWatchKillSelector(name, check, entry, add)
}

func validateProcessWatchKillSelector(name string, check, entry map[string]any, add func(string, ...any)) {
	then, ok := entry[rules.RuleFieldThen].(map[string]any)
	if !ok {
		return
	}
	if _, present := then[WatchThenKeyKill]; !present {
		return
	}
	if !filepath.IsAbs(cfgval.String(check[checks.CheckKeyName])) {
		add("%s requires check.name to be an absolute resolved exe path", thenKillPath(watchPath(name)))
	}
	if cfgval.String(check[checks.CheckKeyUser]) == "" {
		add("%s requires check.user", thenKillPath(watchPath(name)))
	}
}

// validateProcessPolicyWatch validates the alert-only execution policy for one
// real user. Every configured allow entry needs an exact absolute executable;
// an optional cmd regex can only narrow that executable identity. The policy
// never accepts a hook or native action, so a configuration mistake cannot turn
// an observation watch into a process-control path.
func validateProcessPolicyWatch(name string, check, entry map[string]any, defaultNotify []string, add func(string, ...any)) {
	validateStatefulWatchEntry(name, checks.CheckTypeProcessPolicy, entry, add)
	if cfgval.String(check[checks.CheckKeyUser]) == "" {
		add("%s is required for a process_policy check", watchCheckFieldPath(name, checks.CheckKeyUser))
	}
	allows, ok := check[checks.CheckKeyAllow].(map[string]any)
	if !ok || len(allows) == 0 {
		add("%s is required and must be a non-empty mapping for a process_policy check", watchCheckFieldPath(name, checks.CheckKeyAllow))
	} else {
		for _, allowName := range slices.Sorted(maps.Keys(allows)) {
			validateProcessPolicyAllow(name, allowName, allows[allowName], add)
		}
	}
	if _, present := entry[sectionPolicy]; present {
		add("%s is not valid on an alert-only process_policy watch", watchFieldPath(name, sectionPolicy))
	}
	validateAlertOnlyWatchThen(name, entry, defaultNotify, add)
}

func validateProcessPolicyAllow(name, allowName string, raw any, add func(string, ...any)) {
	prefix := watchCheckFieldPath(name, checks.CheckKeyAllow) + "." + allowName
	allow, ok := raw.(map[string]any)
	if !ok {
		add(validationMappingFormat, prefix)
		return
	}
	for _, key := range slices.Sorted(maps.Keys(allow)) {
		if key != checks.CheckKeyExe && key != process.SelectorKeyCmd {
			add(validationNotSupportedFormat, prefix+"."+key)
		}
	}
	exe := cfgval.String(allow[checks.CheckKeyExe])
	if exe == "" {
		add("%s.%s is required", prefix, checks.CheckKeyExe)
	} else if !filepath.IsAbs(exe) || filepath.Clean(exe) != exe {
		add("%s.%s must be a clean absolute resolved executable path", prefix, checks.CheckKeyExe)
	}
	if rawCmd, present := allow[process.SelectorKeyCmd]; present {
		cmd, ok := rawCmd.(string)
		if !ok || cmd == "" {
			add("%s.%s must be a non-empty anchored RE2 expression", prefix, process.SelectorKeyCmd)
			return
		}
		if !strings.HasPrefix(cmd, "^") || !strings.HasSuffix(cmd, "$") {
			add("%s.%s must be anchored with ^ and $", prefix, process.SelectorKeyCmd)
		}
		if _, err := regexp.Compile(cmd); err != nil {
			add("%s.%s is invalid: %v", prefix, process.SelectorKeyCmd, err)
		}
	}
}

// validateAlertOnlyWatchThen permits only notification delivery on an
// execution-policy watch. Omitting then remains valid and records a dashboard
// and event-log alert without running an external action.
func validateAlertOnlyWatchThen(name string, entry map[string]any, defaultNotify []string, add func(string, ...any)) {
	prefix := watchPath(name)
	then, ok := watchThenMapping(prefix, entry, add)
	if !ok {
		return
	}
	for _, key := range slices.Sorted(maps.Keys(then)) {
		if !IsAlertOnlyWatchThenKey(key) {
			add("%s is not valid on an alert-only process_policy watch", thenFieldPath(prefix, key))
		}
	}
	notify := cfgval.StringList(then[rules.RuleFieldNotify])
	validateWatchNotifyInterval(prefix, then, false, notify, defaultNotify, add)
	if len(then) == 0 {
		add("%s requires notify or omit then for dashboard/event-log alerts", prefix+"."+rules.RuleFieldThen)
	}
}
