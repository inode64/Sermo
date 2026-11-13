package config

import (
	"fmt"
	"testing"
)

func TestValidateProcessCommand(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields string
		want   string
	}{
		{name: "cmd only", fields: `cmd: '(^|/)emerge($| )'`},
		{name: "combined", fields: `exe: /usr/bin/python3, user: root, cmd: emerge`},
		{name: "any exe", fields: `exe_any: [/usr/bin/python3, /bin/bash], cmd: emerge`},
		{name: "invalid regex", fields: `cmd: '['`, want: "cmd is not a valid regex"},
		{name: "empty", fields: `cmd: ''`, want: "cmd must be a non-empty regex"},
		{name: "not string", fields: `cmd: [emerge]`, want: "cmd must be a non-empty regex"},
		{name: "user only", fields: `user: root`, want: "exe, exe_any or cmd is required"},
		{name: "timeout", fields: `cmd: emerge, timeout: 0s`, want: "timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := validateService(t, fmt.Sprintf(`
name: svc
service: svc
checks:
  packages: {type: process, %s, state: absent}
rules:
  packages:
    type: guard
    blocks: [restart]
    if: {not: {process: {%s, state: absent}}}
    then: {action: block, message: packages running}
`, tc.fields, tc.fields))
			if tc.want == "" {
				mustNotHave(t, issues, "packages")
			} else {
				mustHave(t, issues, "checks.packages.")
				mustHave(t, issues, "rules.packages.if.not.process.")
				mustHave(t, issues, tc.want)
			}
		})
	}
}

func TestValidateCommandOnlyWatchNeverAuthorizesKill(t *testing.T) {
	entry := map[string]any{
		"check": map[string]any{"type": "process", "cmd": "emerge", "gone": true},
		"then":  map[string]any{"hook": map[string]any{"command": []any{"/bin/report"}}},
	}
	doc := map[string]any{"watches": map[string]any{"packages": entry}}
	assertNoWatchIssues(t, doc)
	entry["then"] = map[string]any{"kill": map[string]any{"signal": "TERM"}}
	assertWatchIssues(t, doc, "requires check.name to be an absolute resolved exe path", "requires check.user")
}
