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

// The expected stray count is zero and the set follows from the service's own
// selectors, so a threshold or a selector here would look meaningful and
// silently do nothing.
func TestValidateStraysCheckRejectsFieldsThatDoNothing(t *testing.T) {
	for _, field := range []string{"exe: /usr/bin/x", "user: root", "op: '>'", "value: 3"} {
		mustHave(t, validateService(t, `
name: svc
service: x
processes:
  main: { exe: /usr/bin/x, user: root }
checks:
  my-strays:
    type: strays
    `+field+`
`), "is not accepted; strays reports the service's control-group members")
	}
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
