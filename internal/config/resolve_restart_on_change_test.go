package config

import (
	"fmt"
	"maps"
	"strings"
	"testing"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/rules"
)

func mustHaveRestartOnChangeActions(t *testing.T, then map[string]any, wantMessage string) {
	t.Helper()
	actions, ok := then["actions"].([]any)
	if !ok || len(actions) != 2 {
		t.Fatalf("then.actions = %#v, want alert + restart", then["actions"])
	}
	alert, ok := actions[0].(map[string]any)
	if !ok {
		t.Fatalf("then.actions[0] = %#v, want mapping", actions[0])
	}
	if got := cfgval.String(alert["type"]); got != string(rules.ActionAlert) {
		t.Fatalf("then.actions[0].type = %q, want alert", got)
	}
	if got := cfgval.String(alert["message"]); wantMessage != "" && got != wantMessage {
		t.Fatalf("then.actions[0].message = %q, want %q", got, wantMessage)
	}
	restart, ok := actions[1].(map[string]any)
	if !ok {
		t.Fatalf("then.actions[1] = %#v, want mapping", actions[1])
	}
	if got := cfgval.String(restart["type"]); got != string(rules.ActionRestart) {
		t.Fatalf("then.actions[1].type = %q, want restart", got)
	}
}

func TestReloadOnChangeDesugarsToReloadRule(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/udev.yml": `
name: udev
service: systemd-udevd
reload_on_change:
  paths:
    - /etc/udev/rules.d
    - /lib/udev/rules.d
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("udev")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	if _, present := resolved.Tree["reload_on_change"]; present {
		t.Errorf("reload_on_change should be desugared away")
	}
	for i, wantPath := range []string{"/etc/udev/rules.d", "/lib/udev/rules.d"} {
		rule := fmt.Sprintf("reload-on-change-%d", i+1)
		then := nested(t, resolved.Tree, "rules", rule, "then")
		if cfgval.String(then["action"]) != "reload" {
			t.Errorf("%s action = %v, want reload", rule, then["action"])
		}
		changed := nested(t, resolved.Tree, "rules", rule, "if", "changed")
		if cfgval.String(changed["path"]) != wantPath {
			t.Errorf("%s changed.path = %v, want %q", rule, changed["path"], wantPath)
		}
	}
}

func TestAddChangedRemediationRule(t *testing.T) {
	rulesMap := map[string]any{}
	changed := map[string]any{rules.FieldPath: "/etc/service.conf"}
	then := map[string]any{rules.RuleFieldAction: string(rules.ActionReload)}
	if err := addChangedRemediationRule(rulesMap, keyReloadOnChange, "reload-on-change-1", changed, then); err != nil {
		t.Fatal(err)
	}
	rule := nested(t, rulesMap, "reload-on-change-1")
	if got := nested(t, rule, rules.RuleFieldIf, rules.ConditionChanged); !maps.Equal(got, changed) {
		t.Errorf("changed condition = %#v, want %#v", got, changed)
	}
	if got := nested(t, rule, rules.RuleFieldThen); !maps.Equal(got, then) {
		t.Errorf("then = %#v, want %#v", got, then)
	}
	if err := addChangedRemediationRule(rulesMap, keyReloadOnChange, "reload-on-change-1", changed, then); err == nil || !strings.Contains(err.Error(), `reload_on_change would overwrite existing rule "reload-on-change-1"`) {
		t.Errorf("duplicate error = %v", err)
	}
}

func TestRestartOnChangeDesugarsToChangedRule(t *testing.T) {
	resolved := resolveInstance(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/libs/glibc.yml": `
name: glibc
display_name: "GNU C Library"
variables:
  binary: "/lib64/libc.so.6"
`,
		"services/web.yml": `
name: web
service: web
restart_on_change:
  libraries: [glibc]
`,
	}, "web")
	if _, present := resolved.Tree["restart_on_change"]; present {
		t.Errorf("restart_on_change should be desugared away")
	}
	then := nested(t, resolved.Tree, "rules", "restart-on-change-glibc", "then")
	mustHaveRestartOnChangeActions(t, then, "web will restart after library change: ${change.library} (${change.path})")
	changed := nested(t, resolved.Tree, "rules", "restart-on-change-glibc", "if", "changed")
	if cfgval.String(changed["path"]) != "/lib64/libc.so.6" {
		t.Errorf("changed.path = %v, want /lib64/libc.so.6", changed["path"])
	}
	preflight := nested(t, resolved.Tree, "preflight", "library-glibc-file")
	if cfgval.String(preflight["type"]) != checks.CheckTypeFile || cfgval.String(preflight["path"]) != "/lib64/libc.so.6" || !cfgval.Bool(preflight[checks.CheckKeyNonEmpty]) {
		t.Errorf("library preflight = %v, want file /lib64/libc.so.6", preflight)
	}
}

func TestRestartOnChangeAppsDesugarToChangedVersionRules(t *testing.T) {
	tests := []struct {
		name            string
		restartOnChange string
		wantLevel       string
	}{
		{
			name: "list defaults to patch",
			restartOnChange: `
restart_on_change:
  apps: [containerd]
`,
			wantLevel: "patch",
		},
		{
			name: "map carries level",
			restartOnChange: `
restart_on_change:
  apps:
    containerd:
      level: minor
`,
			wantLevel: "minor",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			global := writeConfig(t, map[string]string{
				"sermo.yml": baseGlobal,
				"catalog/apps/containerd.yml": `
name: containerd
preflight:
  version:
    type: command
    command: ["/usr/bin/containerd", "--version"]
    timeout: 5s
`,
				"services/containerd.yml": `
name: containerd
service: containerd
apps: [containerd]
` + tt.restartOnChange,
			})
			cfg, err := loadConfig(t, global)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if issues := Validate(cfg); len(issues) != 0 {
				t.Fatalf("Validate() issues = %v, want none", issues)
			}
			resolved, errs := cfg.Resolve("containerd")
			if len(errs) != 0 {
				t.Fatalf("Resolve() errors = %v", errs)
			}
			if _, present := resolved.Tree["restart_on_change"]; present {
				t.Errorf("restart_on_change should be desugared away")
			}
			rule := nested(t, resolved.Tree, "rules", "restart-on-change-containerd-version")
			if got := cfgval.String(rule["type"]); got != "remediation" {
				t.Fatalf("rule type = %q, want remediation", got)
			}
			changed := nested(t, rule, "if", "changed")
			if got := cfgval.String(changed["app"]); got != "containerd" {
				t.Fatalf("changed.app = %q, want containerd", got)
			}
			if got := cfgval.String(changed["level"]); got != tt.wantLevel {
				t.Fatalf("changed.level = %q, want %q", got, tt.wantLevel)
			}
			mustHaveRestartOnChangeActions(t, nested(t, rule, "then"), "containerd will restart after version change of ${change.app}: ${change.old_version} -> ${change.new_version}")
			if cfgval.String(nested(t, resolved.Tree, "preflight", "containerd-version")["type"]) != "command" {
				t.Fatal("resolved service must expose containerd-version preflight for changed.app")
			}
		})
	}
}

func TestRestartOnChangePathsDesugarToRestartRule(t *testing.T) {
	resolved := resolveValidInstance(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/web.yml": `
name: web
service: web
variables:
  config: /etc/web/web.conf
restart_on_change:
  paths:
    - ${config}
`,
	}, "web")
	if _, present := resolved.Tree["restart_on_change"]; present {
		t.Errorf("restart_on_change should be desugared away")
	}
	rule := nested(t, resolved.Tree, "rules", "restart-on-change-config-1")
	if got := cfgval.String(rule["type"]); got != "remediation" {
		t.Fatalf("rule type = %q, want remediation", got)
	}
	if got := cfgval.String(nested(t, rule, "if", "changed")["path"]); got != "/etc/web/web.conf" {
		t.Fatalf("changed.path = %q, want /etc/web/web.conf", got)
	}
	mustHaveRestartOnChangeActions(t, nested(t, rule, "then"), "web will restart after config change: ${change.path}")
}

func TestRestartOnChangeMessagesCustomizeGeneratedAlerts(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/filebeat.yml": `
name: filebeat
preflight:
  version:
    type: command
    command: ["/usr/bin/filebeat", "version"]
    timeout: 5s
`,
		"services/filebeat.yml": `
name: filebeat
display_name: Filebeat
service: filebeat
apps: [filebeat]
variables:
  config: /etc/filebeat/filebeat.yml
restart_on_change:
  paths:
    - ${config}
    - /etc/filebeat/modules.d
  apps:
    - filebeat
  messages:
    path: "${display_name} will restart after config change: ${change.path}"
    app: "${display_name} will restart after version change of ${change.app}: ${change.old_version} -> ${change.new_version}"
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("Validate() issues = %v, want none", issues)
	}
	resolved, errs := cfg.Resolve("filebeat")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	mustHaveRestartOnChangeActions(t, nested(t, resolved.Tree, "rules", "restart-on-change-config-1", "then"),
		"Filebeat will restart after config change: ${change.path}")
	mustHaveRestartOnChangeActions(t, nested(t, resolved.Tree, "rules", "restart-on-change-filebeat-version", "then"),
		"Filebeat will restart after version change of ${change.app}: ${change.old_version} -> ${change.new_version}")
}

func TestRestartOnChangeFlagsOverrideDefaults(t *testing.T) {
	app := `
name: containerd
preflight:
  version:
    type: command
    command: ["/usr/bin/containerd", "--version"]
    timeout: 5s
`
	tests := []struct {
		name        string
		globalFlags string
		catalog     string
		service     string
		rule        string
		wantRule    bool
	}{
		{
			name:        "global blocks version",
			globalFlags: "    version: false\n",
			service: `
name: containerd
service: containerd
apps: [containerd]
restart_on_change:
  apps: [containerd]
`,
			rule:     "restart-on-change-containerd-version",
			wantRule: false,
		},
		{
			name:        "service re-enables version",
			globalFlags: "    version: false\n",
			service: `
name: containerd
service: containerd
apps: [containerd]
restart_on_change:
  version: true
  apps: [containerd]
`,
			rule:     "restart-on-change-containerd-version",
			wantRule: true,
		},
		{
			name:        "service blocks inherited catalog version",
			globalFlags: "    version: true\n",
			catalog: `
name: containerd
service: containerd
apps: [containerd]
restart_on_change:
  apps: [containerd]
`,
			service: `
name: containerd-main
uses: containerd
restart_on_change:
  version: false
`,
			rule:     "restart-on-change-containerd-version",
			wantRule: false,
		},
		{
			name:        "global blocks config",
			globalFlags: "    config: false\n",
			service: `
name: web
service: web
restart_on_change:
  paths: [/etc/web/web.conf]
`,
			rule:     "restart-on-change-config-1",
			wantRule: false,
		},
		{
			name:        "service re-enables config",
			globalFlags: "    config: false\n",
			service: `
name: web
service: web
restart_on_change:
  config: true
  paths: [/etc/web/web.conf]
`,
			rule:     "restart-on-change-config-1",
			wantRule: true,
		},
		{
			name:        "service blocks inherited config",
			globalFlags: "    config: true\n",
			catalog: `
name: web
service: web
restart_on_change:
  paths: [/etc/web/web.conf]
`,
			service: `
name: web-main
uses: web
restart_on_change:
  config: false
`,
			rule:     "restart-on-change-config-1",
			wantRule: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{
				"sermo.yml":            strings.Replace(baseGlobal, "defaults:\n", "defaults:\n  restart_on_change:\n"+tt.globalFlags, 1),
				"services/service.yml": tt.service,
			}
			if strings.Contains(tt.service, "containerd") || strings.Contains(tt.catalog, "containerd") {
				files["catalog/apps/containerd.yml"] = app
			}
			if tt.catalog != "" {
				files["catalog/services/catalog.yml"] = tt.catalog
			}
			global := writeConfig(t, files)
			cfg, err := loadConfig(t, global)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if issues := Validate(cfg); len(issues) != 0 {
				t.Fatalf("Validate() issues = %v, want none", issues)
			}
			serviceName := cfg.ServiceNames[0]
			resolved, errs := cfg.Resolve(serviceName)
			if len(errs) != 0 {
				t.Fatalf("Resolve() errors = %v", errs)
			}
			rulesMap, _ := resolved.Tree["rules"].(map[string]any)
			_, gotRule := rulesMap[tt.rule]
			if gotRule != tt.wantRule {
				t.Fatalf("generated rule %q present = %v, want %v", tt.rule, gotRule, tt.wantRule)
			}
		})
	}
}

func TestRestartOnChangeAppVersionValidatesInputs(t *testing.T) {
	baseApp := `
name: containerd
preflight:
  version:
    type: command
    command: ["/usr/bin/containerd", "--version"]
    timeout: 5s
`
	tests := []struct {
		name    string
		app     string
		service string
		want    string
	}{
		{
			name: "app must be linked",
			app:  baseApp,
			service: `
name: containerd
service: containerd
restart_on_change:
  apps: [containerd]
`,
			want: `restart_on_change app "containerd" must also be listed in apps`,
		},
		{
			name: "invalid level",
			app:  baseApp,
			service: `
name: containerd
service: containerd
apps: [containerd]
restart_on_change:
  apps:
    containerd:
      level: revision
`,
			want: `restart_on_change.apps.containerd.level "revision" is not one of`,
		},
		{
			name: "missing version command",
			app: `
name: containerd
preflight:
  health:
    type: command
    command: ["/usr/bin/containerd", "--help"]
    timeout: 5s
`,
			service: `
name: containerd
service: containerd
apps: [containerd]
restart_on_change:
  apps: [containerd]
`,
			want: `changed app "containerd" has no app version command`,
		},
		{
			name: "generated rule collision",
			app:  baseApp,
			service: `
name: containerd
service: containerd
apps: [containerd]
restart_on_change:
  apps: [containerd]
rules:
  restart-on-change-containerd-version:
    type: remediation
    if: { service: { state: failed } }
    then: { action: restart }
`,
			want: `restart_on_change would overwrite existing rule "restart-on-change-containerd-version"`,
		},
		{
			name: "map values must be mappings",
			app:  baseApp,
			service: `
name: containerd
service: containerd
apps: [containerd]
restart_on_change:
  apps:
    containerd: minor
`,
			want: `restart_on_change.apps.containerd must be a mapping`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertValidateIssue(t, map[string]string{
				"sermo.yml":                   baseGlobal,
				"catalog/apps/containerd.yml": tt.app,
				"services/containerd.yml":     tt.service,
			}, tt.want)
		})
	}
}

func TestRestartOnChangeValidatesInputs(t *testing.T) {
	tests := []struct {
		name    string
		service string
		want    string
	}{
		{
			name: "config flag must be bool",
			service: `
name: web
service: web
restart_on_change:
  config: "false"
  paths: [/etc/web/web.conf]
`,
			want: `restart_on_change.config must be true or false`,
		},
		{
			name: "version flag must be bool",
			service: `
name: web
service: web
restart_on_change:
  version: "true"
`,
			want: `restart_on_change.version must be true or false`,
		},
		{
			name: "unknown key",
			service: `
name: web
service: web
restart_on_change:
  mode: restart
`,
			want: `restart_on_change.mode is not supported`,
		},
		{
			name: "paths must be string or list",
			service: `
name: web
service: web
restart_on_change:
  paths: { config: /etc/web/web.conf }
`,
			want: `restart_on_change.paths must be a string or list of strings`,
		},
		{
			name: "messages must be mapping",
			service: `
name: web
service: web
restart_on_change:
  paths: [/etc/web/web.conf]
  messages: notify
`,
			want: `restart_on_change.messages must be a mapping`,
		},
		{
			name: "messages reject unknown key",
			service: `
name: web
service: web
restart_on_change:
  paths: [/etc/web/web.conf]
  messages:
    config: notify
`,
			want: `restart_on_change.messages.config is not supported`,
		},
		{
			name: "messages require strings",
			service: `
name: web
service: web
restart_on_change:
  paths: [/etc/web/web.conf]
  messages:
    path: 7
`,
			want: `restart_on_change.messages.path must be a non-empty string`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertValidateIssue(t, map[string]string{
				"sermo.yml":        baseGlobal,
				"services/web.yml": tt.service,
			}, tt.want)
		})
	}
}

func TestDefaultsRestartOnChangeValidatesFlagsOnly(t *testing.T) {
	tests := []struct {
		name       string
		defaultROC string
		want       string
	}{
		{
			name:       "must be mapping",
			defaultROC: "true",
			want:       `defaults.restart_on_change must be a mapping`,
		},
		{
			name: "config flag must be bool",
			defaultROC: `
    config: "false"
`,
			want: `defaults.restart_on_change.config must be true or false`,
		},
		{
			name: "version flag must be bool",
			defaultROC: `
    version: "true"
`,
			want: `defaults.restart_on_change.version must be true or false`,
		},
		{
			name: "apps not allowed in defaults",
			defaultROC: `
    apps: [containerd]
`,
			want: `defaults.restart_on_change.apps is not supported`,
		},
		{
			name: "paths not allowed in defaults",
			defaultROC: `
    paths: [/etc/web/web.conf]
`,
			want: `defaults.restart_on_change.paths is not supported`,
		},
		{
			name: "libraries not allowed in defaults",
			defaultROC: `
    libraries: [glibc]
`,
			want: `defaults.restart_on_change.libraries is not supported`,
		},
		{
			name: "messages not allowed in defaults",
			defaultROC: `
    messages: { path: notify }
`,
			want: `defaults.restart_on_change.messages is not supported`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			globalCfg := `
engine:
  backend: auto
paths:
  services: [ @ROOT@/services ]
  runtime: /run/sermo
defaults:
  restart_on_change: ` + tt.defaultROC + `
  policy:
    cooldown: 5m
`
			assertValidateIssue(t, map[string]string{"sermo.yml": globalCfg}, tt.want)
		})
	}
}

func TestRestartOnChangeUnknownLibraryErrors(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		// nginx is a catalog service, not a library: referencing it must error.
		"catalog/services/nginx.yml": "name: nginx\nservice: nginx\n",
		"services/web.yml": `
name: web
service: web
restart_on_change:
  libraries: [nginx, ghost]
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	_, errs := cfg.Resolve("web")
	joined := strings.Join(errs, "\n")
	for _, want := range []string{"nginx", "ghost"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected error mentioning %q, got %v", want, errs)
		}
	}
}

func TestChangedAppVersionRuleValidatesResolvedVersionCommand(t *testing.T) {
	resolved := resolveValidInstance(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/containerd.yml": `
name: containerd
preflight:
  version:
    type: command
    command: ["/usr/bin/containerd", "--version"]
    timeout: 5s
`,
		"services/containerd.yml": `
name: containerd
service: containerd
apps: [containerd]
rules:
  restart-if-containerd-version-changed:
    type: remediation
    if:
      changed:
        app: containerd
        level: patch
    then:
      action: restart
`,
	}, "containerd")
	changed := nested(t, resolved.Tree, "rules", "restart-if-containerd-version-changed", "if", "changed")
	if got := cfgval.String(changed["app"]); got != "containerd" {
		t.Fatalf("changed.app = %q, want containerd", got)
	}
	if cfgval.String(nested(t, resolved.Tree, "preflight", "containerd-version")["type"]) != "command" {
		t.Fatal("resolved service must expose containerd-version preflight for changed.app")
	}
}

func TestChangedAppVersionRuleRequiresVersionCommand(t *testing.T) {
	assertValidateIssue(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/containerd.yml": `
name: containerd
preflight:
  health:
    type: command
    command: ["/usr/bin/containerd", "--help"]
    timeout: 5s
`,
		"services/containerd.yml": `
name: containerd
service: containerd
apps: [containerd]
rules:
  restart-if-containerd-version-changed:
    type: remediation
    if: { changed: { app: containerd, level: patch } }
    then: { action: restart }
`,
	}, `changed app "containerd" has no app version command`)
}
