package config

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"sermo/internal/cfgval"
)

func TestResolveMergesDefaultsServiceOverrides(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/apache.yml": `
name: apache
variables:
  host: 127.0.0.1
  port: 8080
checks:
  http:
    type: http
    url: "http://${host}:${port}/health"
    expect_status: 200
policy:
  max_actions: 3
`,
		"services/apache-main.yml": `
name: apache-main
uses: apache
checks:
  http:
    url: "http://${host}:${port}/"
`,
	})

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("apache-main")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}

	http := nested(t, resolved.Tree, "checks", "http")
	if got := http["url"]; got != "http://127.0.0.1:8080/" {
		t.Errorf("url = %v, want override expanded", got)
	}
	if got := cfgval.String(http["expect_status"]); got != "200" {
		t.Errorf("expect_status = %v, want inherited 200", got)
	}
	policy := nested(t, resolved.Tree, "policy")
	if got := cfgval.String(policy["cooldown"]); got != "5m" {
		t.Errorf("cooldown = %v, want default 5m", got)
	}
	if got := cfgval.String(policy["max_actions"]); got != "3" {
		t.Errorf("max_actions = %v, want service 3", got)
	}
	stop := nested(t, resolved.Tree, "stop_policy")
	if got := cfgval.String(stop["graceful_timeout"]); got != "30s" {
		t.Errorf("graceful_timeout = %v, want default 30s", got)
	}
}

func TestResolveDryRunDefaultsTargets(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
engine:
  backend: auto
paths:
  services: [ @ROOT@/services ]
  watches: [ @ROOT@/watches ]
  runtime: /run/sermo
defaults:
  dry_run: true
  policy: { cooldown: 5m }
watches:
  load-live:
    dry_run: false
    check:
      type: load
      load1: { op: ">", value: 10 }
    then: { notify: [none] }
`,
		"catalog/services/demo.yml": `
name: demo
service: demo
`,
		"services/demo.yml": `
name: demo
uses: demo
`,
		"watches/data.yml": `
name: data
check:
  type: storage
  path: /data
  used_pct: { op: ">=", value: "90%" }
`,
	})

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	svc, errs := cfg.Resolve("demo")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	if !DryRun(svc.Tree) {
		t.Fatal("service should inherit defaults.dry_run")
	}
	storage, errs := cfg.ResolveStorage("data")
	if len(errs) != 0 {
		t.Fatalf("ResolveStorage() errors = %v", errs)
	}
	if !DryRun(storage.Tree) {
		t.Fatal("storage should inherit defaults.dry_run")
	}
	watches, errs := cfg.ResolveWatches()
	if len(errs) != 0 {
		t.Fatalf("ResolveWatches() errors = %v", errs)
	}
	watch, ok := watches["load-live"].(map[string]any)
	if !ok {
		t.Fatalf("watch not resolved: %v", watches)
	}
	if DryRun(watch) {
		t.Fatal("watch dry_run false should override defaults.dry_run")
	}
	capacity, ok := watches["data"].(map[string]any)
	if !ok {
		t.Fatalf("storage watch not resolved: %v", watches)
	}
	if !DryRun(capacity) {
		t.Fatal("storage watch should inherit defaults.dry_run")
	}
}

func TestCloneOverridesVariableBeforeExpansion(t *testing.T) {
	assertResolvedCheckField(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/redis-main.yml": `
name: redis-main
variables:
  port: 6379
checks:
  ping:
    type: tcp
    port: "${port}"
`,
		"services/redis-cache.yml": `
name: redis-cache
clone: redis-main
variables:
  port: 6380
`,
	}, "redis-cache", "ping", "port", "6380")
}

func TestMultiInstanceServiceOverridesPerInstance(t *testing.T) {
	// Two services share one catalog service (same binary, checks and rules) but each
	// overrides only the variables that make an instance unique: listen port,
	// pidfile and config path. This is the supported pattern for running e.g.
	// two MariaDB or php-fpm instances off a single catalog service — no new mechanism
	// is needed beyond `uses` + per-instance `variables`.
	cfg, err := loadConfig(t, writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/dbserver.yml": `
name: dbserver
service:
  systemd: [dbserver]
variables:
  host: 127.0.0.1
  port: 3306
  pidfile: /run/dbserver/main.pid
  config: /etc/dbserver/main.cnf
pidfile: "${pidfile}"
checks:
  tcp:
    type: tcp
    host: "${host}"
    port: "${port}"
  config:
    type: command
    command: ["dbserverd", "--defaults-file=${config}", "--help"]
`,
		"services/db-inst1.yml": `
name: db-inst1
uses: dbserver
service: db-inst1
variables:
  port: 3306
  pidfile: /run/dbserver/inst1.pid
  config: /etc/dbserver/inst1.cnf
`,
		"services/db-inst2.yml": `
name: db-inst2
uses: dbserver
service: db-inst2
variables:
  port: 3307
  pidfile: /run/dbserver/inst2.pid
  config: /etc/dbserver/inst2.cnf
`,
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	type want struct{ port, pidfile, config string }
	cases := map[string]want{
		"db-inst1": {port: "3306", pidfile: "/run/dbserver/inst1.pid", config: "/etc/dbserver/inst1.cnf"},
		"db-inst2": {port: "3307", pidfile: "/run/dbserver/inst2.pid", config: "/etc/dbserver/inst2.cnf"},
	}
	for name, w := range cases {
		resolved, errs := cfg.Resolve(name)
		if len(errs) != 0 {
			t.Fatalf("Resolve(%s) errors = %v", name, errs)
		}
		if got := cfgval.String(nested(t, resolved.Tree, "checks", "tcp")["port"]); got != w.port {
			t.Errorf("%s tcp.port = %q, want %q", name, got, w.port)
		}
		if got := cfgval.String(resolved.Tree["pidfile"]); got != w.pidfile {
			t.Errorf("%s pidfile = %q, want %q", name, got, w.pidfile)
		}
		if got := cfgval.String(nested(t, resolved.Tree, "checks", "pidfile")["path"]); got != w.pidfile {
			t.Errorf("%s checks.pidfile.path = %q, want %q", name, got, w.pidfile)
		}
		cmd, _ := nested(t, resolved.Tree, "checks", "config")["command"].([]any)
		if joined := fmt.Sprint(cmd...); !strings.Contains(joined, w.config) {
			t.Errorf("%s config check command = %v, want to contain %q", name, cmd, w.config)
		}
	}
}

func TestExpandAnalyzeResolvesUseSilenceRules(t *testing.T) {
	resolved := resolveInstance(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/patterns/common.yml": `
name: common
rules:
  - { id: dep,  match: "(?i)deprecated", severity: warning }
  - { id: note, match: "(?i)note",       severity: warning }
`,
		"catalog/services/svc.yml": `
name: svc
variables:
  binary: /bin/true
watches:
  config-files:
    check:
      type: command
      command: ["${binary}"]
      analyze:
        use: [common]
        silence: [dep]
        rules:
          - { id: local, match: "(?i)ok", severity: ok }
`,
		"services/svc-main.yml": "name: svc-main\nuses: svc\n",
	}, "svc-main")
	checkEntries := resolved.Tree["checks"].(map[string]any)
	analyze := checkEntries["config-files"].(map[string]any)["analyze"].(map[string]any)
	ruleEntries := analyze["rules"].([]any)
	if len(ruleEntries) != 2 {
		t.Fatalf("want 2 resolved rules (note + local), got %d: %v", len(ruleEntries), ruleEntries)
	}
	ids := []string{idOf(ruleEntries[0]), idOf(ruleEntries[1])}
	if ids[0] != "local" || ids[1] != "note" {
		t.Fatalf("resolved rule order = %v, want [local note] (local first for precedence, dep silenced)", ids)
	}
	if _, present := analyze["use"]; present {
		t.Errorf("use must be consumed during resolution")
	}
	if _, present := analyze["silence"]; present {
		t.Errorf("silence must be consumed during resolution")
	}
}

func TestExpandAnalyzeUnknownSetAndBadSilence(t *testing.T) {
	mk := func(analyze string) []string {
		global := writeConfig(t, map[string]string{
			"sermo.yml":                   baseGlobal,
			"catalog/patterns/common.yml": "name: common\nrules:\n  - { id: dep, match: x, severity: warning }\n",
			"catalog/services/svc.yml":    "name: svc\nvariables:\n  binary: /bin/true\nwatches:\n  config-files:\n    check:\n      type: command\n      command: [\"${binary}\"]\n      analyze:\n" + analyze,
			"services/svc-main.yml":       "name: svc-main\nuses: svc\n",
		})
		cfg, err := loadConfig(t, global)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		_, errs := cfg.Resolve("svc-main")
		return errs
	}
	if errs := mk("        use: [nope]\n"); !hasSub(errs, "not a patterns set") {
		t.Errorf("unknown set should error, got %v", errs)
	}
	if errs := mk("        use: [common]\n        silence: [ghost]\n"); !hasSub(errs, "not present in the inherited sets") {
		t.Errorf("bad silence id should error, got %v", errs)
	}
}

func TestExpandPidfileDesugars(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/svc.yml": `
name: svc
pidfile: /run/svc.pid
checks:
  service: { type: service, expect: active }
`,
		"services/svc-main.yml": "name: svc-main\nuses: svc\n",
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("svc-main")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	if got := cfgval.String(resolved.Tree["pidfile"]); got != "/run/svc.pid" {
		t.Fatalf("pidfile = %q, want /run/svc.pid", got)
	}
	if _, present := resolved.Tree["processes"]; present {
		t.Fatalf("pidfile must not create public processes entry: %v", resolved.Tree["processes"])
	}
	// Gated health check.
	checkEntries := resolved.Tree["checks"].(map[string]any)
	chk := checkEntries["pidfile"].(map[string]any)
	if chk["type"] != "pidfile" || chk["path"] != "/run/svc.pid" {
		t.Fatalf("pidfile check = %v", chk)
	}
	req, _ := chk["requires"].([]any)
	if len(req) != 1 || req[0] != "service" {
		t.Fatalf("pidfile check requires = %v, want [service]", chk["requires"])
	}
}

func TestExpandPidfileCandidateListDesugars(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/svc.yml": `
name: svc
pidfile:
  - /run/svc-main.pid
  - /run/svc-legacy.pid
checks:
  service: { type: service, expect: active }
`,
		"services/svc-main.yml": "name: svc-main\nuses: svc\n",
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("svc-main")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	want := []string{"/run/svc-main.pid", "/run/svc-legacy.pid"}
	if got := cfgval.StringList(resolved.Tree["pidfile"]); !slices.Equal(got, want) {
		t.Fatalf("pidfile paths = %v, want %v", got, want)
	}
	checkEntries := resolved.Tree["checks"].(map[string]any)
	chk := checkEntries["pidfile"].(map[string]any)
	if got := cfgval.StringList(chk["path"]); !slices.Equal(got, want) {
		t.Fatalf("check pidfile paths = %v, want %v", got, want)
	}
}

func TestExpandPidfileOptionalMapDesugars(t *testing.T) {
	resolved := resolveInstance(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/svc.yml": `
name: svc
pidfile:
  path: /run/svc.pid
  optional: true
checks:
  service: { type: service, expect: active }
`,
		"services/svc-main.yml": "name: svc-main\nuses: svc\n",
	}, "svc-main")
	if got := cfgval.String(resolved.Tree["pidfile"]); got != "/run/svc.pid" {
		t.Fatalf("pidfile = %q, want /run/svc.pid", got)
	}
	chk := nested(t, resolved.Tree, "checks", "pidfile")
	if optional, _ := chk["optional"].(bool); !optional {
		t.Fatalf("pidfile check optional = %v, want true", chk["optional"])
	}
}

func TestExpandPidfilesDesugarsByRole(t *testing.T) {
	resolved := resolveInstance(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/svc.yml": `
name: svc
pidfiles:
  main:
    - /run/svc-main.pid
    - /run/svc.pid
  helper: /run/svc-helper.pid
processes:
  main:
    exe: /usr/sbin/svc
    user: svc
  helper:
    exe: /usr/sbin/svc-helper
    user: svc
checks:
  service: { type: service, expect: active }
`,
		"services/svc-main.yml": "name: svc-main\nuses: svc\n",
	}, "svc-main")
	pidfiles := resolved.Tree["pidfiles"].(map[string]any)
	if got, want := cfgval.StringList(pidfiles["main"]), []string{"/run/svc-main.pid", "/run/svc.pid"}; !slices.Equal(got, want) {
		t.Fatalf("pidfiles.main = %v, want %v", got, want)
	}
	if got := cfgval.String(pidfiles["helper"]); got != "/run/svc-helper.pid" {
		t.Fatalf("pidfiles.helper = %q, want /run/svc-helper.pid", got)
	}
	checkEntries := resolved.Tree["checks"].(map[string]any)
	main := checkEntries["pidfile-main"].(map[string]any)
	if got := cfgval.StringList(main["path"]); !slices.Equal(got, []string{"/run/svc-main.pid", "/run/svc.pid"}) {
		t.Fatalf("check pidfile-main path = %v", got)
	}
	helper := checkEntries["pidfile-helper"].(map[string]any)
	if got := cfgval.String(helper["path"]); got != "/run/svc-helper.pid" {
		t.Fatalf("check pidfile-helper path = %v", got)
	}
}

func TestExpandFileShorthandsDesugar(t *testing.T) {
	tests := []struct {
		name, shorthand string
		paths           []string
	}{
		{"socket", "socket", []string{"/run/svc-main.sock", "/run/svc-legacy.sock"}},
		{"lockfile", "lockfile", []string{"/run/lock/svc-main.lock", "/run/lock/svc-legacy.lock"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			global := writeConfig(t, map[string]string{
				"sermo.yml":                baseGlobal,
				"catalog/services/svc.yml": fmt.Sprintf("name: svc\n%s:\n  path:\n    - %s\n    - %s\n  optional: true\nchecks:\n  service: { type: service, expect: active }\n", tc.shorthand, tc.paths[0], tc.paths[1]),
				"services/svc-main.yml":    "name: svc-main\nuses: svc\n",
			})
			cfg, err := loadConfig(t, global)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			resolved, errs := cfg.Resolve("svc-main")
			if len(errs) != 0 {
				t.Fatalf("Resolve() errors = %v", errs)
			}
			if _, present := resolved.Tree[tc.shorthand]; present {
				t.Errorf("top-level %s key must be consumed", tc.shorthand)
			}
			chk := nested(t, resolved.Tree, "checks", tc.shorthand)
			if chk["type"] != tc.shorthand || !slices.Equal(cfgval.StringList(chk["path"]), tc.paths) {
				t.Fatalf("%s check = %v, want candidate list %v", tc.shorthand, chk, tc.paths)
			}
			if optional, _ := chk["optional"].(bool); !optional {
				t.Fatalf("%s optional = %v, want true", tc.shorthand, chk["optional"])
			}
			req, _ := chk["requires"].([]any)
			if len(req) != 1 || req[0] != "service" {
				t.Fatalf("%s requires = %v, want [service]", tc.shorthand, chk["requires"])
			}
		})
	}
}

func TestExpandLockfileUsesVariable(t *testing.T) {
	assertExpandedVar(t, "lockfile", "/run/lock/svc.lock")
}

func TestExpandLockfileRejectsRelativeCandidate(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/svc.yml": `
name: svc
lockfile: run/lock/svc.lock
checks:
  service: { type: service, expect: active }
`,
		"services/svc-main.yml": "name: svc-main\nuses: svc\n",
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	_, errs := cfg.Resolve("svc-main")
	if !hasSub(errs, `lockfile path "run/lock/svc.lock" must be absolute`) {
		t.Fatalf("Resolve() errors = %v, want relative lockfile path error", errs)
	}
}

func TestResolveWatchesExpandsCustomVars(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
engine: { backend: auto }
paths:
  services: [ @ROOT@/services ]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
  variables: { cdir: /var/spool }
watches:
  w: { check: { type: file_exists, path: "${cdir}/flag" } }
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	watches, errs := cfg.ResolveWatches()
	if len(errs) != 0 {
		t.Fatalf("ResolveWatches: %v", errs)
	}
	got := watches["w"].(map[string]any)["check"].(map[string]any)["path"]
	if got != "/var/spool/flag" {
		t.Fatalf("watch custom var not expanded: %v", got)
	}
}

func TestChangedLibraryConditionResolvesPath(t *testing.T) {
	// The documented shorthand `changed: {library: X}` resolves the library to
	// its watched file anywhere in a rule's condition tree, exactly like the
	// restart_on_change desugar.
	resolved := resolveInstance(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/libs/glibc.yml": `
name: glibc
variables:
  binary: "/lib64/libc.so.6"
`,
		"services/web.yml": `
name: web
service: web
rules:
  glibc-changed:
    type: alert
    if:
      or:
        - not:
            changed: { library: glibc }
        - changed: { path: /etc/web.conf }
    then: { action: alert, message: "glibc changed" }
`,
	}, "web")
	or, ok := nested(t, resolved.Tree, "rules", "glibc-changed", "if")["or"].([]any)
	if !ok || len(or) != 2 {
		t.Fatalf("if.or = %v", or)
	}
	changed := nested(t, or[0].(map[string]any), "not", "changed")
	if cfgval.String(changed["path"]) != "/lib64/libc.so.6" {
		t.Errorf("changed.path = %v, want /lib64/libc.so.6", changed["path"])
	}
}

func TestChangedUnknownLibraryErrors(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/web.yml": `
name: web
service: web
rules:
  ghost-changed:
    type: alert
    if: { changed: { library: ghost } }
    then: { action: alert, message: "x" }
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	_, errs := cfg.Resolve("web")
	joined := strings.Join(errs, "\n")
	if !strings.Contains(joined, `"ghost"`) || !strings.Contains(joined, "not a library") {
		t.Fatalf("expected unknown-library error, got %v", errs)
	}
}
