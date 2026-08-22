package config

import "testing"

// TestValidateCheckBands covers the `bands:` grammar on a service check: valid
// partial and full overrides load clean, and every malformed shape is named.
func TestValidateCheckBands(t *testing.T) {
	issues := validateService(t, `
name: store
service: mdmon
checks:
  raid0:
    type: raid
    array: md0
    bands:
      degraded: { severity: warning }
      recovering: false
`)
	mustNotHave(t, issues, "checks.raid0.bands")
}

func TestValidateCheckBandsErrors(t *testing.T) {
	issues := validateService(t, `
name: store
service: mdmon
checks:
  raid0:
    type: raid
    array: md0
    bands:
      ghost: { severity: warning }
      degraded: { ok: { op: "~~", value: "high" }, severity: fatal }
      recovering: 3
  load0:
    type: load
    load1: { op: ">", value: 8 }
    bands:
      load1: { severity: warning }
`)
	mustHave(t, issues, `checks.raid0.bands.ghost: "ghost" is not a state raid publishes`)
	mustHave(t, issues, `checks.raid0.bands.degraded.ok has invalid op "~~"`)
	mustHave(t, issues, "checks.raid0.bands.degraded.ok value must be numeric")
	mustHave(t, issues, `checks.raid0.bands.degraded.severity "fatal" must be error or warning`)
	mustHave(t, issues, "checks.raid0.bands.recovering must be a mapping or false")
	// A graph metric converted to a band needs its OK predicate spelled out.
	mustHave(t, issues, "checks.load0.bands.load1 converts a graph metric to a band and must declare ok: {op, value}")
}

// TestValidateCheckNameColonRejected pins the separator rule the band series
// key depends on: "<check>:<metric>" can only be unambiguous if a check's own
// name never carries the separator.
func TestValidateCheckNameColonRejected(t *testing.T) {
	issues := validateService(t, `
name: web
service: nginx
checks:
  "http:main":
    type: tcp
    host: 127.0.0.1
    port: 80
`)
	mustHave(t, issues, "checks.http:main: check names must not contain ':'")
}

// TestValidateWatchCheckBands covers the same grammar through a watch's check
// block, which never reaches validateCheckSection.
func TestValidateWatchCheckBands(t *testing.T) {
	issues := validateService(t, `
name: host
watches:
  watch-raid:
    check:
      type: raid
      array: md0
      bands:
        degraded: { severity: warning }
        bogus: {}
`)
	mustHave(t, issues, `bands.bogus: "bogus" is not a state raid publishes`)
	mustNotHave(t, issues, "bands.degraded")
}
