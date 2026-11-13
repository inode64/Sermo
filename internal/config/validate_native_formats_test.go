package config

import "testing"

func TestValidateNativeFormatAndProtocolFields(t *testing.T) {
	issues := validateService(t, `
name: svc
service: svc
policy: { cooldown: 5m }
checks:
  bad-format: { type: config, format: yaml, path: /etc/app.xml }
  ambiguous: { type: config, format: xml, path: /etc/app.xml, command: [test] }
  bad-rpc: { type: rpcbind, program: -1 }
  bad-memory: { type: docker, memory: true }
`)
	mustHave(t, issues, "format must be xml or jar")
	mustHave(t, issues, "format and command are mutually exclusive")
	mustHave(t, issues, "unsigned 32-bit integer")
	mustHave(t, issues, "docker memory requires a container")
	good := validateService(t, `
name: svc
service: svc
policy: { cooldown: 5m }
checks:
  xml: { type: config, format: xml, path: /etc/app.xml }
  jar: { type: config, format: jar, path: /usr/share/app/app.jar }
  rpc: { type: rpcbind, program: 100024, version: 1, transport: tcp }
  memory: { type: docker, memory: true, container: app }
`)
	if len(good) > 0 {
		t.Fatalf("valid native config: %v", good)
	}
}

func TestValidateExplicitUnitList(t *testing.T) {
	good := validateService(t, `
name: svc
service: svc
policy: { cooldown: 5m }
checks:
  activated: { type: service, backend: systemd, service: [app.service, app.socket], expect: active }
`)
	if len(good) > 0 {
		t.Fatalf("unit list: %v", good)
	}
}
