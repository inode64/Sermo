package config

import "testing"

func TestValidateLogCheck(t *testing.T) {
	bad := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  no-path:    { type: log, regex: "a", count: { op: ">", value: 1 }, within: 5m }
  rel-path:   { type: log, path: logs/x.log, regex: "a", count: { op: ">", value: 1 }, within: 5m }
  no-regex:   { type: log, path: /var/log/x.log, count: { op: ">", value: 1 }, within: 5m }
  bad-regex:  { type: log, path: /var/log/x.log, regex: "(", count: { op: ">", value: 1 }, within: 5m }
  no-count:   { type: log, path: /var/log/x.log, regex: "a", within: 5m }
  bad-op:     { type: log, path: /var/log/x.log, regex: "a", count: { op: "=>", value: 1 }, within: 5m }
  bad-val:    { type: log, path: /var/log/x.log, regex: "a", count: { op: ">", value: lots }, within: 5m }
  no-within:  { type: log, path: /var/log/x.log, regex: "a", count: { op: ">", value: 1 } }
  bad-within: { type: log, path: /var/log/x.log, regex: "a", count: { op: ">", value: 1 }, within: nope }
`)
	mustHave(t, bad, "no-path log check requires a path")
	mustHave(t, bad, "rel-path log check path must be absolute")
	mustHave(t, bad, "no-regex log check requires a regex")
	mustHave(t, bad, "bad-regex log check regex is invalid")
	mustHave(t, bad, "no-count log check requires a count {op, value}")
	mustHave(t, bad, `bad-op.count has an invalid op "=>"`)
	mustHave(t, bad, `bad-val.count value "lots" must be numeric`)
	mustHave(t, bad, "no-within.within is required")
	mustHave(t, bad, `bad-within.within "nope" must be a valid positive duration`)

	good := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  otlp-timeouts:
    type: log
    path: /var/www/app/var/log/symfony-messenger_*_err.log
    regex: 'cURL error 28: (Connection|Operation) timed out'
    count: { op: ">", value: 3 }
    within: 5m
    optional: true
`)
	mustNotHave(t, good, "otlp-timeouts")
}
