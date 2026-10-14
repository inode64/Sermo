package config

import "testing"

func TestValidateSizeCheckValid(t *testing.T) {
	issues := validateService(t, `
name: app
service: x
checks:
  log-growth: { type: size, path: /var/log/app.log, grow_by: 1GB, within: 1h }
  binlogs: { type: size, path: "/var/lib/mysql/mariadb*", max: 20G }
  both: { type: size, path: "/var/lib/mysql/mariadb*", max: 20G, grow_by: 1G, within: 30m }
  max-only-empty-window: { type: size, path: "/var/lib/mysql/mariadb*", max: 20G, grow_by: "", within: "" }
  window-only-empty-max: { type: size, path: /var/log/app.log, max: "", grow_by: 1G, within: 1h }
`)
	mustNotHave(t, issues, "checks.log-growth")
	mustNotHave(t, issues, "checks.binlogs")
	mustNotHave(t, issues, "checks.both")
	mustNotHave(t, issues, "checks.max-only-empty-window")
	mustNotHave(t, issues, "checks.window-only-empty-max")
}

func TestValidateSizeCheckErrors(t *testing.T) {
	cases := map[string]struct {
		body string
		want string
	}{
		"no path":       {`g: { type: size, grow_by: 1GB, within: 1h }`, "size check requires a path"},
		"bad glob":      {`g: { type: size, path: "/var/lib/mysql/[bin*", max: 1G }`, "is not a valid glob"},
		"all empty":     {`g: { type: size, path: /x, max: "", grow_by: "", within: "" }`, "needs max"},
		"relative path": {`g: { type: size, path: x, max: 1G }`, "must be absolute"},
		"no threshold":  {`g: { type: size, path: /x }`, "needs max"},
		"no grow_by":    {`g: { type: size, path: /x, within: 1h }`, "grow_by is required"},
		"bad grow_by":   {`g: { type: size, path: /x, grow_by: nonsense, within: 1h }`, "positive size"},
		"no within":     {`g: { type: size, path: /x, grow_by: 1GB }`, "within is required"},
		"bad within":    {`g: { type: size, path: /x, grow_by: 1GB, within: nope }`, "positive duration"},
		"bad max":       {`g: { type: size, path: /x, max: 1000 }`, "positive size"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			mustHave(t, validateService(t, "name: app\nservice: x\nchecks:\n  "+c.body+"\n"), c.want)
		})
	}
}
