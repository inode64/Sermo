package config

import (
	"testing"
)

func TestValidateDocumentAliases(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/web.yml": `
name: web
aliases: [web]
`,
		"catalog/services/db.yml": `
name: db
aliases: [bad/name]
`,
		"catalog/services/cache.yml": `
name: cache
aliases: ["", alt, alt]
`,
		"catalog/services/api.yml": `
name: api
aliases: [alt]
`,
		"catalog/services/plain.yml": `
name: plain
aliases: nope
`,
	})

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issues := Validate(cfg)
	for _, want := range []string{
		`alias "web" duplicates the document name`,
		`alias "bad/name" must be a simple name without path separators`,
		"aliases must not contain empty names",
		`duplicate alias "alt"`,
		`alias "alt" is already used by catalog service`,
		"aliases must be a list of simple names",
	} {
		if !hasIssue(issues, want) {
			t.Errorf("missing issue containing %q in %v", want, issues)
		}
	}
}

func TestValidateCleanConfig(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/redis-main.yml": `
name: redis-main
service: redis
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("Validate() issues = %v, want none", issues)
	}
}

func TestValidateGlobalErrors(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
engine:
  backend: bogus
paths:
  services: [ @ROOT@/services ]
  locks: /run/sermo/locks
  runtime: relative/path
  templates: relative/templates
  unexpected: /tmp/sermo
defaults:
  mystery: true
  policy:
    cooldown: 0s
security:
  allow_sigkill_by_default: true
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issues := Validate(cfg)
	wantSubstrings := []string{
		"engine.backend",
		"paths.locks is not supported; runtime locks derive from paths.runtime",
		"paths.runtime",
		"paths.templates",
		"paths.unexpected is not supported",
		"security.allow_sigkill_by_default",
		"defaults.mystery is not supported",
		"defaults.policy.cooldown",
	}
	for _, want := range wantSubstrings {
		if !hasIssue(issues, want) {
			t.Errorf("missing issue containing %q in %v", want, issues)
		}
	}
}

func TestValidateMissingVariableAndPort(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/bad.yml": `
name: bad
checks:
  http: { type: http, url: "http://${missing}/", port: 99999 }
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issues := Validate(cfg)
	if !hasIssue(issues, "variable ${missing} used in checks.http.url") {
		t.Errorf("missing undefined-variable issue: %v", issues)
	}
	if !hasIssue(issues, "must resolve to a port in 1..65535") {
		t.Errorf("missing port-range issue: %v", issues)
	}
}

func TestValidateCloneCycle(t *testing.T) {
	assertValidateIssue(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/a.yml": `
name: a
clone: b
`,
		"services/b.yml": `
name: b
clone: a
`,
	}, "clone cycle detected")
}

func TestValidateNestedVariableRejected(t *testing.T) {
	assertValidateIssue(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/nested.yml": `
name: nested
variables:
  a: "${b}"
  b: "x"
`,
	}, "references another variable")
}

// TestDescriptionHasNoFallback guards the asymmetry: unlike display_name,
// description is never materialized from name. A document without a description
// renders without one.
func TestDescriptionHasNoFallback(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/plain.yml": `
name: plain
service: plain
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("plain")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	if _, present := resolved.Tree["description"]; present {
		t.Errorf("description should be absent, got %v", resolved.Tree["description"])
	}
}

func TestValidateDuplicateServiceName(t *testing.T) {
	assertValidateIssue(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/one.yml": `
name: dup
`,
		"services/two.yml": `
name: dup
`,
	}, "duplicate service name")
}

func TestValidateRejectsPathLikeDocumentName(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/bad.yml": `
name: ../escape
service: mysql
`,
		"catalog/services/bad.yml": `
name: apache/main
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issues := Validate(cfg)
	if !hasIssue(issues, `document name "../escape" must be a simple name without path separators`) {
		t.Fatalf("missing service name issue in %v", issues)
	}
	if !hasIssue(issues, `document name "apache/main" must be a simple name without path separators`) {
		t.Fatalf("missing catalog service name issue in %v", issues)
	}
}

func TestValidateEngineDurations(t *testing.T) {
	issues := validateGlobalDoc(t, `
engine:
  interval: notaduration
  default_timeout: 0s
  operation_timeout: bad
  artifact_interval: invalid
  unexpected: true
  max_parallel_checks: 0
paths:
  services: [ @ROOT@/services ]
defaults:
  policy: { cooldown: 5m }
`)
	for _, want := range []string{
		"engine.interval",
		"engine.default_timeout",
		"engine.operation_timeout",
		"engine.artifact_interval",
		"engine.unexpected is not supported",
		"engine.max_parallel_checks",
	} {
		mustHave(t, issues, want)
	}
}

func TestValidateServiceRestartNotice(t *testing.T) {
	valid := validateGlobalDoc(t, `
engine:
  service_restart_notice:
    uptime_below: 5m
    notify: wall
    subject: "${restart.service}: restarted"
    message: "${restart.process} pid ${restart.pid} uptime ${restart.uptime}"
defaults:
  policy: { cooldown: 5m }
`)
	for _, token := range []string{"service_restart_notice", "restart.service", "restart.process"} {
		mustNotHave(t, valid, token)
	}

	invalid := validateGlobalDoc(t, `
engine:
  service_restart_notice:
    uptime_below: 0s
    notify: unknown
    message: ""
    extra: true
defaults:
  policy: { cooldown: 5m }
`)
	for _, token := range []string{
		"engine.service_restart_notice.uptime_below",
		"engine.service_restart_notice.notify references unknown notifier",
		"engine.service_restart_notice.message is required",
		"engine.service_restart_notice.extra is not supported",
	} {
		mustHave(t, invalid, token)
	}
}

func TestValidateBackoffDurations(t *testing.T) {
	// A garbage initial must not let a valid max slip through. A previous
	// implementation left the parsed initial at 0, so any max compared >= 0
	// and passed.
	badInitial := validateService(t, `
name: svc
service: svc
policy:
  cooldown: 5m
  backoff: { initial: nonsense, max: 10s }
`)
	mustHave(t, badInitial, "policy.backoff.initial")

	// An omitted max reports its own parse error, not the misleading ">= initial".
	missingMax := validateService(t, `
name: svc
service: svc
policy:
  cooldown: 5m
  backoff: { initial: 5s }
`)
	mustHave(t, missingMax, "policy.backoff.max must be a valid positive duration")

	// max < initial is still rejected with the ordering message.
	maxBelow := validateService(t, `
name: svc
service: svc
policy:
  cooldown: 5m
  backoff: { initial: 30s, max: 5s }
`)
	mustHave(t, maxBelow, "policy.backoff.max must be >= initial")

	// A valid pair produces no backoff issue.
	ok := validateService(t, `
name: svc
service: svc
policy:
  cooldown: 5m
  backoff: { initial: 5s, max: 1m }
`)
	mustNotHave(t, ok, "backoff")
}

func TestValidateEngineOperationTimeoutAcceptsPositive(t *testing.T) {
	mustNotHave(t, validateGlobalDoc(t, `
engine:
  operation_timeout: 90s
paths:
  services: [ @ROOT@/services ]
defaults:
  policy: { cooldown: 5m }
`), "operation_timeout")
}

func TestValidateEngineLogPaths(t *testing.T) {
	issues := validateGlobalDoc(t, `
engine:
  access: relative.log
  events: /var/log/sermo/event.log
  diagnostics_interval: 1h
paths:
  services: [ @ROOT@/services ]
defaults:
  policy: { cooldown: 5m }
`)
	mustHave(t, issues, "engine.access")
	mustHave(t, issues, "engine.diagnostics_interval")
}

func TestValidateEngineUserLookup(t *testing.T) {
	issues := validateGlobalDoc(t, `
engine:
  user_lookup: ldap
  user_lookup_timeout: 0s
paths:
  services: [ @ROOT@/services ]
defaults:
  policy: { cooldown: 5m }
`)
	mustHave(t, issues, "engine.user_lookup")
	mustHave(t, issues, "engine.user_lookup_timeout")
}

func TestValidateEngineUserLookupAcceptsDocumentedModes(t *testing.T) {
	for _, mode := range []string{"auto", "native", "getent", "numeric"} {
		t.Run(mode, func(t *testing.T) {
			mustNotHave(t, validateGlobalDoc(t, `
engine:
  user_lookup: `+mode+`
  user_lookup_timeout: 250ms
paths:
  services: [ @ROOT@/services ]
defaults:
  policy: { cooldown: 5m }
`), "user_lookup")
		})
	}
}

func TestValidateLibvirtControl(t *testing.T) {
	valid := validateService(t, `
name: svc
control:
  type: libvirt
  domain: vm01
  uuid: 2b3f3d26-bb45-4b25-b65a-1e3ef86fc1a4
  socket: /run/libvirt/libvirt-sock
`)
	mustNotHave(t, valid, "control")

	mustHave(t, validateService(t, `
name: svc
control: { type: libvirt }
`), "control.domain is required")
	mustHave(t, validateService(t, `
name: svc
control:
  type: libvirt
  domain: vm01
  uuid: nope
`), "control.uuid")
	mustHave(t, validateService(t, `
kind:
name: svc
control:
  type: libvirt
  domain: vm01
  socket: /run/libvirt/libvirt-sock
  host: 127.0.0.1
`), "must not set both socket and host")
}

func TestValidateLibvirtNetworkControl(t *testing.T) {
	valid := validateService(t, `
name: libvirt-net-default
control:
  type: libvirt-network
  network: default
  socket: /run/libvirt/virtnetworkd-sock
`)
	mustNotHave(t, valid, "control")

	mustHave(t, validateService(t, `
name: svc
control: { type: libvirt-network }
`), "control.network is required")
	mustHave(t, validateService(t, `
name: svc
control:
  type: libvirt-network
  network: default
  domain: vm01
`), "control key")
	mustHave(t, validateService(t, `
name: svc
control:
  type: libvirt-network
  network: default
  socket: /run/libvirt/libvirt-sock
  host: 127.0.0.1
`), "must not set both socket and host")
}

func TestValidateDockerControl(t *testing.T) {
	valid := validateService(t, `
name: svc
control:
  type: docker
  container: web
  socket: /run/docker.sock
`)
	mustNotHave(t, valid, "control")

	validTCP := validateService(t, `
name: svc
control:
  type: docker
  container: web
  host: 127.0.0.1
  port: 2376
  tls: skip-verify
`)
	mustNotHave(t, validTCP, "control")

	mustHave(t, validateService(t, `
name: svc
control: { type: docker }
`), "control.container is required")
	mustHave(t, validateService(t, `
name: svc
control:
  type: docker
  container: web
  socket: docker.sock
`), "control.socket")
	mustHave(t, validateService(t, `
name: svc
control:
  type: docker
  container: web
  socket: /run/docker.sock
  host: 127.0.0.1
`), "must not set both socket and host")
	mustHave(t, validateService(t, `
name: svc
control:
  type: docker
  container: web
  host: 127.0.0.1
  port: 70000
`), "control.port")
	mustHave(t, validateService(t, `
name: svc
control:
  type: docker
  container: web
  tls: maybe
`), "control.tls")
	mustHave(t, validateService(t, `
name: svc
control:
  type: docker
  container: web
  interface: eth0
`), "control key \"interface\"")
}

func TestValidateStorageMountIntegration(t *testing.T) {
	// A storage check carries space/inode predicates and/or a mounted condition in one
	// entry (no separate mount type) — including a mount-only storage check.
	good := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  data: { type: storage, path: /data, used_pct: { op: ">=", value: 90 }, mounted: true }
  mountonly: { type: storage, path: /srv, mounted: true }
`)
	mustNotHave(t, good, "checks.data")
	mustNotHave(t, good, "checks.mountonly")

	bad := validateService(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  empty: { type: storage, path: /data }
  bad-mounted: { type: storage, path: /data, mounted: "yes" }
  unsupported-mount-controls: { type: storage, path: /data, mounted: true, fstype: ext4, device: /dev/sdb1, options: [rw] }
`)
	mustHave(t, bad, "checks.empty requires a space/inode predicate")
	mustHave(t, bad, "checks.bad-mounted.mounted must be a boolean")
	mustHave(t, bad, "checks.unsupported-mount-controls.fstype is not supported for a storage check")
	mustHave(t, bad, "checks.unsupported-mount-controls.device is not supported for a storage check")
	mustHave(t, bad, "checks.unsupported-mount-controls.options is not supported for a storage check")
}

func TestValidateAppVersionFrom(t *testing.T) {
	assertCatalogValidation(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/consumer.yml": `
name: consumer
variables:
  binary: /usr/bin/consumer
version_from: provider
`,
		"catalog/apps/provider.yml": `
name: provider
variables:
  binary: /usr/bin/provider
preflight:
  version: { type: command, command: ["/usr/bin/provider", "--version"] }
`,
		"catalog/apps/missing.yml": `
name: missing
variables:
  binary: /usr/bin/missing
version_from: ghost
`,
		"catalog/apps/self.yml": `
name: self
variables:
  binary: /usr/bin/self
version_from: self
`,
		"catalog/apps/a.yml": `
name: a
variables:
  binary: /usr/bin/a
version_from: b
`,
		"catalog/apps/b.yml": `
name: b
variables:
  binary: /usr/bin/b
version_from: a
`,
		"catalog/apps/bad-name.yml": `
name: bad-name
variables:
  binary: /usr/bin/bad-name
version_from: ../provider
`,
		"catalog/apps/bad-type.yml": `
name: bad-type
variables:
  binary: /usr/bin/bad-type
version_from: [provider]
`,
		"catalog/services/not-app.yml": `
name: not-app
version_from: provider
service: not-app
`,
	}, "app consumer",
		`version_from references unknown app "ghost"`,
		"version_from must not reference itself",
		"version_from cycle detected",
		`version_from "../provider" must be a simple name`,
		"version_from must be a non-empty app name",
		"version_from is only supported on app catalog documents")
}

func TestValidateAppVersionMatch(t *testing.T) {
	assertCatalogValidation(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/mysql.yml": `
name: mysql
variables:
  binary: /usr/sbin/mysqld
version_match: { excludes: MariaDB }
preflight:
  version: { type: command, command: ["/usr/sbin/mysqld", "--version"] }
`,
		"catalog/apps/bad-shape.yml": `
name: bad-shape
variables:
  binary: /usr/bin/bad-shape
version_match: MariaDB
preflight:
  version: { type: command, command: ["/usr/bin/bad-shape", "--version"] }
`,
		"catalog/apps/bad-key.yml": `
name: bad-key
variables:
  binary: /usr/bin/bad-key
version_match: { rejects: MariaDB }
preflight:
  version: { type: command, command: ["/usr/bin/bad-key", "--version"] }
`,
		"catalog/apps/bad-regex.yml": `
name: bad-regex
variables:
  binary: /usr/bin/bad-regex
version_match: { regex: "[" }
preflight:
  version: { type: command, command: ["/usr/bin/bad-regex", "--version"] }
`,
		"catalog/apps/no-version.yml": `
name: no-version
variables:
  binary: /usr/bin/no-version
version_match: { contains: Demo }
`,
		"catalog/services/not-app.yml": `
name: not-app
version_match: { contains: Demo }
service: not-app
`,
	}, "app mysql",
		"version_match must be a mapping",
		`version_match unknown key "rejects"`,
		"version_match regex",
		"version_match requires a version command",
		"version_match is only supported on app catalog documents")
}

func TestValidateVersionsFromInitBranches(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/svc.yml": `
name: svc%v
versions:
  from:
    default: /etc/svc-${version}
    systemd:
      - /usr/lib/systemd/system/svc@${version}.service
      - ""
    openrc: 42
    launchd: /Library/LaunchDaemons/svc-${version}.plist
checks:
  service: { type: service, expect: active }
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issues := Validate(cfg)
	mustHave(t, issues, "versions.from.default is not supported; use systemd or openrc")
	mustHave(t, issues, "versions.from.systemd[1] must be a non-empty path string")
	mustHave(t, issues, "versions.from.openrc must be a path string or list of path strings")
	mustHave(t, issues, "versions.from.launchd is not supported; use systemd or openrc")
}
