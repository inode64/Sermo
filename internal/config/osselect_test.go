package config

import (
	"reflect"
	"strings"
	"testing"

	"sermo/internal/cfgval"
)

func TestApplyOSSelectorsRejectsDocumentScalar(t *testing.T) {
	cfg := &Config{Global: Global{Raw: map[string]any{
		keyOS: map[string]any{keyOSDefault: "/run/example.pid"},
	}}}

	err := cfg.applyOSSelectors()
	if err == nil || !strings.Contains(err.Error(), "global config: document must resolve to a mapping") {
		t.Fatalf("applyOSSelectors() error = %v", err)
	}
}

func TestCollapseOSBranchShapes(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  any
	}{
		{
			name: "map branch merges with siblings",
			input: map[string]any{
				"timeout": "5s",
				keyOS:     map[string]any{"gentoo": map[string]any{"url": "http://localhost/gentoo"}},
			},
			want: map[string]any{"timeout": "5s", "url": "http://localhost/gentoo"},
		},
		{
			name:  "scalar branch replaces selector-only value",
			input: map[string]any{keyOS: map[string]any{keyOSDefault: "/run/example.pid"}},
			want:  "/run/example.pid",
		},
		{
			name:  "scalar branch is ignored when siblings remain",
			input: map[string]any{"keep": true, keyOS: map[string]any{"gentoo": "/run/example.pid"}},
			want:  map[string]any{"keep": true},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := collapseOS(tc.input, "gentoo")
			if err != nil {
				t.Fatalf("collapseOS() error = %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("collapseOS() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestCollapseOSRejectsMergedMapThatResolvesToScalar(t *testing.T) {
	_, err := collapseOS(map[string]any{
		"keep": true,
		keyOS: map[string]any{
			"gentoo": map[string]any{keyOS: map[string]any{"gentoo": "/run/example.pid"}},
		},
	}, "gentoo")
	if err == nil || !strings.Contains(err.Error(), `os branch "gentoo" must resolve to a mapping when merged`) {
		t.Fatalf("collapseOS() error = %v", err)
	}
}

func TestOSSelectorCollapses(t *testing.T) {
	old := detectedOS
	detectedOS = "gentoo"
	defer func() { detectedOS = old }()

	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/apache.yml": `
name: apache
service:
  os:
    gentoo:
      systemd: [apache.service]
      openrc: [apache]
    debian:
      systemd: [apache2.service]
      openrc: [apache2]
checks:
  http:
    type: http
    timeout: 5s
    os:
      gentoo: { url: "http://localhost/gentoo" }
      debian: { url: "http://localhost/debian" }
policy:
  os:
    debian: { cooldown: 1m }
    default: { cooldown: 9m }
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	body := cfg.CatalogServices["apache"].Body

	// service: the os: block is replaced by the gentoo branch.
	svc := body["service"].(map[string]any)
	if _, present := svc["os"]; present {
		t.Errorf("os selector not collapsed: %v", svc)
	}
	if sysd, _ := svc["systemd"].([]any); len(sysd) != 1 || sysd[0] != "apache.service" {
		t.Errorf("service.systemd = %v, want [apache.service]", svc["systemd"])
	}

	// checks.http: branch merged with its siblings (timeout kept, url added).
	http := nested(t, body, "checks", "http")
	if cfgval.String(http["timeout"]) != "5s" || cfgval.String(http["url"]) != "http://localhost/gentoo" {
		t.Errorf("checks.http = %v, want timeout 5s + gentoo url", http)
	}

	// policy: gentoo absent → the default branch applies.
	policy := body["policy"].(map[string]any)
	if cfgval.String(policy["cooldown"]) != "9m" {
		t.Errorf("policy.cooldown = %v, want default 9m", policy["cooldown"])
	}
}

func TestOSSelectorListBranch(t *testing.T) {
	old := detectedOS
	detectedOS = "gentoo"
	defer func() { detectedOS = old }()

	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/db.yml": `
name: db
pidfile:
  os:
    gentoo: [/run/db1.pid, /run/db.pid]
    default: [/run/db.pid]
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	got, _ := cfg.CatalogServices["db"].Body["pidfile"].([]any)
	if len(got) != 2 || got[0] != "/run/db1.pid" {
		t.Errorf("pidfile = %v, want the gentoo candidate list", cfg.CatalogServices["db"].Body["pidfile"])
	}
}

func TestOSVariableBaked(t *testing.T) {
	old := detectedOS
	detectedOS = "debian"
	defer func() { detectedOS = old }()

	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/app.yml": `
name: app
variables:
  binary: "/opt/${os}/bin/app"
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := DocumentBinary(cfg.CatalogServices["app"].Body); got != "/opt/debian/bin/app" {
		t.Errorf("baked binary = %q, want /opt/debian/bin/app", got)
	}
}

func TestDetectOSFromEnv(t *testing.T) {
	t.Setenv("SERMO_OS", "Gentoo")
	if got := detectOS(); got != "gentoo" {
		t.Errorf("detectOS() = %q, want gentoo", got)
	}
}

// TestGlobalDerivedFieldsFollowOSSelectors pins that the fields Load derives
// from sermo.yml read the same tree validation reads. They were derived before
// `os:` and ${os}/${arch} were applied, so the host ran on defaults and loaded
// no services while validation saw the selected values.
func TestGlobalDerivedFieldsFollowOSSelectors(t *testing.T) {
	old := detectedOS
	detectedOS = "gentoo"
	t.Cleanup(func() { detectedOS = old })
	service := "name: svc\nservice: svc\nprocesses: {}\n"
	defaults := "\ndefaults: { policy: { cooldown: 5m } }\n"

	t.Run("engine directories and templates", func(t *testing.T) {
		cfg, err := loadConfig(t, writeConfig(t, map[string]string{"sermo.yml": `
engine:
  os:
    gentoo: { state: /var/lib/sermo-gentoo, runtime: /run/sermo-gentoo }
paths:
  templates: /etc/sermo/templates-${os}
` + defaults}))
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if got := cfg.Global.StateDir(); got != "/var/lib/sermo-gentoo" {
			t.Errorf("StateDir() = %q", got)
		}
		if got := cfg.Global.RuntimeDir(); got != "/run/sermo-gentoo" {
			t.Errorf("RuntimeDir() = %q", got)
		}
		if got := cfg.Global.TemplateDir(); got != "/etc/sermo/templates-gentoo" {
			t.Errorf("TemplateDir() = %q", got)
		}
	})

	for name, paths := range map[string]string{
		"builtin in a path": `paths: { services: [ "@ROOT@/svc-${os}" ] }`,
		"selector":          `paths: { os: { gentoo: { services: [ @ROOT@/svc-gentoo ] } } }`,
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := loadConfig(t, writeConfig(t, map[string]string{
				"sermo.yml":         paths + defaults,
				"svc-gentoo/a.yml":  service,
				"services/skip.yml": "name: skip\nservice: skip\nprocesses: {}\n",
			}))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if !reflect.DeepEqual(cfg.ServiceNames, []string{"svc"}) {
				t.Fatalf("ServiceNames = %v, want [svc]", cfg.ServiceNames)
			}
		})
	}
}
