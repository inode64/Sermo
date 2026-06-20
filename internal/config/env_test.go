package config

import "testing"

func TestExpandEnvString(t *testing.T) {
	t.Setenv("SECRET_TOKEN", "s3cret")
	cases := []struct{ in, want string }{
		{"Bearer ${env:SECRET_TOKEN}", "Bearer s3cret"},
		{"${env:MISSING}", ""},                              // unset -> empty
		{"${env:MISSING:-fallback}", "fallback"},            // unset -> default
		{"${env:SECRET_TOKEN:-fallback}", "s3cret"},         // set -> value (not default)
		{"${other} ${env:SECRET_TOKEN}", "${other} s3cret"}, // leaves non-env refs alone
		{"no refs here", "no refs here"},
	}
	for _, tc := range cases {
		if got := expandEnvString(tc.in); got != tc.want {
			t.Errorf("expandEnvString(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestServiceCheckUsesEnvSecret(t *testing.T) {
	t.Setenv("API_TOKEN", "tok-123")
	issues := validateService(t, `
kind: service
name: svc
service: x
policy: { cooldown: 5m }
checks:
  api:
    type: http
    url: "https://api/health"
    headers: { Authorization: "Bearer ${env:API_TOKEN}" }
`)
	if len(issues) != 0 {
		t.Fatalf("env ref should resolve cleanly: %v", issues)
	}
	// the resolved tree carries the secret from the environment
	cfg := loadServiceConfig(t, `
kind: service
name: svc
service: x
policy: { cooldown: 5m }
checks:
  api: { type: http, url: "https://api/health", headers: { Authorization: "Bearer ${env:API_TOKEN}" } }
`)
	resolved, errs := cfg.Resolve("svc")
	if len(errs) != 0 {
		t.Fatalf("resolve errs: %v", errs)
	}
	got := resolved.Tree["checks"].(map[string]any)["api"].(map[string]any)["headers"].(map[string]any)["Authorization"]
	if got != "Bearer tok-123" {
		t.Fatalf("Authorization = %v, want Bearer tok-123", got)
	}
}

func TestEnvSecretMissingDoesNotError(t *testing.T) {
	// A secret that is not set at validate time must not fail validation.
	issues := validateService(t, `
kind: service
name: svc
service: x
policy: { cooldown: 5m }
checks:
  api: { type: http, url: "https://api/health", headers: { Authorization: "Bearer ${env:DEFINITELY_UNSET_TOKEN}" } }
`)
	if len(issues) != 0 {
		t.Fatalf("an unset env secret must not error: %v", issues)
	}
}

func TestGlobalEnvSecret(t *testing.T) {
	t.Setenv("SMTP_DSN", "smtp://user:pw@mail.example.com:587")
	global := writeConfig(t, map[string]string{"sermo.yml": `
paths: { services: [ @ROOT@/enabled ] }
defaults: { policy: { cooldown: 5m } }
notifiers:
  ops:
    type: email
    dsn: "${env:SMTP_DSN}"
    from: "x@y"
    to: [a@b]
`})
	cfg, err := Load(global)
	if err != nil {
		t.Fatal(err)
	}
	n := cfg.Global.Raw["notifiers"].(map[string]any)["ops"].(map[string]any)
	if n["dsn"] != "smtp://user:pw@mail.example.com:587" {
		t.Fatalf("global env not expanded: %v", n["dsn"])
	}
}

// loadServiceConfig builds a Config from a single service document on the base
// global, for resolution tests.
func loadServiceConfig(t *testing.T, serviceYAML string) *Config {
	t.Helper()
	global := writeConfig(t, map[string]string{
		"sermo.yml":       baseGlobal,
		"enabled/svc.yml": serviceYAML,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return cfg
}
