package config

import "testing"

func TestValidateStaleMountsCheck(t *testing.T) {
	assertServiceValidation(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  any:   { type: stale_mounts }
  two:   { type: stale_mounts, count: { op: ">=", value: 2 } }
`, "any", `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  many: { type: stale_mounts, count: many }
`,
		"checks.many")
}
