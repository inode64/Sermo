package config

import "testing"

func TestValidateReapPolicyAcceptsPairedSelector(t *testing.T) {
	issues := validateService(t, `
name: svc
service: x
reap:
  kill_only_if:
    users: [root]
    exe_any: [/usr/bin/dbus-daemon]
`)
	mustNotHave(t, issues, sectionReap)
}

// A mistyped subkey must fail loudly: a stray is reached by control-group
// membership, so a misread selector would leave the action authorized by
// something the operator never wrote.
func TestValidateReapPolicyRejectsUnknownKeys(t *testing.T) {
	mustHave(t, validateService(t, `
name: svc
service: x
reap:
  kill_if:
    users: [root]
    exe_any: [/usr/bin/dbus-daemon]
`), "reap.kill_if is not supported")

	mustHave(t, validateService(t, `
name: svc
service: x
reap:
  kill_only_if:
    users: [root]
    exe_any: [/usr/bin/dbus-daemon]
    exe: /usr/bin/dbus-daemon
`), "reap.kill_only_if.exe is not supported")
}

func TestValidateReapPolicyRequiresBothSelectorHalves(t *testing.T) {
	mustHave(t, validateService(t, `
name: svc
service: x
reap:
  kill_only_if:
    users: [root]
`), "reap.kill_only_if must define both users and exe_any")

	mustHave(t, validateService(t, `
name: svc
service: x
reap: root
`), "reap must be a mapping with kill_only_if")

	mustHave(t, validateService(t, `
name: svc
service: x
reap:
  kill_only_if: root
`), "reap.kill_only_if must be a mapping defining both users and exe_any")
}

// The selector matches the resolved /proc/<pid>/exe, so a relative path could
// never match anything and can only be a mistake.
func TestValidateReapPolicyRequiresAbsoluteExe(t *testing.T) {
	mustHave(t, validateService(t, `
name: svc
service: x
reap:
  kill_only_if:
    users: [root]
    exe_any: [dbus-daemon]
`), `reap.kill_only_if.exe_any path "dbus-daemon" must be absolute`)
}

// straysCheckConfig builds a service declaring one strays check with extra fields.
func straysCheckConfig(fields string) string {
	return `
name: svc
service: x
processes:
  main: { exe: /usr/bin/x, user: root }
checks:
  my-strays:
    type: strays
` + fields
}

// The set follows from the service's own selectors, so a selector here would look
// meaningful and silently do nothing.
func TestValidateStraysCheckRejectsSelectorFields(t *testing.T) {
	for _, field := range []string{"    exe: /usr/bin/x", "    user: root", "    state: running"} {
		mustHave(t, validateService(t, straysCheckConfig(field)),
			"is not accepted; strays reports the service's control-group members")
	}
}

// op/value are rejected on purpose: as a level predicate OK would mean "the
// predicate holds", inverting `failed:` for this instance while the injected one
// keeps it.
func TestValidateStraysCheckRejectsLevelPredicates(t *testing.T) {
	for _, field := range []string{"    op: '>'", "    value: 3"} {
		mustHave(t, validateService(t, straysCheckConfig(field)),
			"use max to raise the failing bound")
	}
}

func TestValidateStraysCheckBounds(t *testing.T) {
	mustNotHave(t, validateService(t, straysCheckConfig("    max: 5\n")), "my-strays")
	mustNotHave(t, validateService(t, straysCheckConfig("    max_increase: 3\n    within: 10m\n")), "my-strays")

	mustHave(t, validateService(t, straysCheckConfig("    max: -1\n")),
		"checks.my-strays.max must be a non-negative integer")
	mustHave(t, validateService(t, straysCheckConfig("    max_increase: 0\n    within: 10m\n")),
		"checks.my-strays.max_increase must be a positive integer")
	// Growth is measured over wall-clock time, so the span is not optional.
	mustHave(t, validateService(t, straysCheckConfig("    max_increase: 3\n")),
		"checks.my-strays.max_increase requires within")
	mustHave(t, validateService(t, straysCheckConfig("    within: 10m\n")),
		"checks.my-strays.within is only accepted with max_increase")
	mustHave(t, validateService(t, straysCheckConfig("    max_increase: 3\n    within: nope\n")),
		"checks.my-strays.within must be a valid positive duration")
}

func TestValidateEngineReapOwnStrays(t *testing.T) {
	mustNotHave(t, validateGlobalDoc(t, `
engine:
  interval: 1m
  reap_own_strays: false
`), EngineKeyReapOwnStrays)

	mustHave(t, validateGlobalDoc(t, `
engine:
  interval: 1m
  reap_own_strays: sometimes
`), "engine.reap_own_strays must be true or false")
}
