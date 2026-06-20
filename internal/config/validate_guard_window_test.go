package config

import "testing"

// TestValidateGuardRejectsWindow asserts that a guard rule carrying a for/within
// window is rejected: guards are evaluated on demand and never advance a window,
// so the window would otherwise be silently ignored.
func TestValidateGuardRejectsWindow(t *testing.T) {
	issues := validateService(t, `
kind: service
name: x
service: x
checks:
  http: { type: http, url: "http://127.0.0.1/" }
rules:
  guard-with-for:
    type: guard
    blocks: [restart]
    for: { cycles: 3 }
    if: { failed: { check: http } }
    then: { action: block, message: "x" }
  guard-with-within:
    type: guard
    blocks: [restart]
    within: { cycles: 5, min_matches: 2 }
    if: { failed: { check: http } }
    then: { action: block, message: "y" }
`)
	mustHave(t, issues, "guard rules do not support a for window")
	mustHave(t, issues, "guard rules do not support a within window")
}
