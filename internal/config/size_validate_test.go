package config

import "testing"

func TestValidateSizeCheckValid(t *testing.T) {
	issues := validateService(t, `
name: app
service: x
checks:
  log-growth: { type: size, path: /var/log/app.log, grow_by: 1GB, within: 1h }
`)
	mustNotHave(t, issues, "checks.log-growth")
}

func TestValidateSizeCheckErrors(t *testing.T) {
	cases := map[string]struct {
		body string
		want string
	}{
		"no path":     {`g: { type: size, grow_by: 1GB, within: 1h }`, "path is required"},
		"no grow_by":  {`g: { type: size, path: /x, within: 1h }`, "grow_by is required"},
		"bad grow_by": {`g: { type: size, path: /x, grow_by: nonsense, within: 1h }`, "positive size"},
		"no within":   {`g: { type: size, path: /x, grow_by: 1GB }`, "within is required"},
		"bad within":  {`g: { type: size, path: /x, grow_by: 1GB, within: nope }`, "positive duration"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			mustHave(t, validateService(t, "name: app\nservice: x\nchecks:\n  "+c.body+"\n"), c.want)
		})
	}
}
