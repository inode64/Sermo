package config

import (
	"maps"
	"os"
	"path/filepath"
	"testing"
)

// webGlobalDefaults is the minimum sermo.yml needs beyond the web block for
// Validate to be silent, so these tests can assert on an empty issue list.
const webGlobalDefaults = `
defaults:
  policy:
    cooldown: 5m
`

// loadWebGlobal loads a sermo.yml carrying a web block, plus any extra files
// (the secret files themselves), and returns the loaded config.
func loadWebGlobal(t *testing.T, sermo string, secrets map[string]string) *Config {
	t.Helper()
	files := map[string]string{"sermo.yml": webGlobalDefaults + sermo}
	maps.Copy(files, secrets)
	cfg, err := loadConfig(t, writeConfig(t, files))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return cfg
}

func TestWebPasswordFileSuppliesPasswords(t *testing.T) {
	tests := []struct {
		name      string
		sermo     string
		secrets   map[string]string
		want      string
		wantGuest string
	}{
		{
			name: "absolute path, trailing newline trimmed",
			sermo: `
web:
  port: 9797
  password_file: @ROOT@/secrets/web.pass
`,
			secrets: map[string]string{"secrets/web.pass": "s3cret\n"},
			want:    "s3cret",
		},
		{
			name: "path relative to sermo.yml",
			sermo: `
web:
  port: 9797
  password_file: secrets/web.pass
`,
			secrets: map[string]string{"secrets/web.pass": "s3cret"},
			want:    "s3cret",
		},
		{
			name: "surrounding whitespace trimmed",
			sermo: `
web:
  port: 9797
  password_file: secrets/web.pass
`,
			secrets: map[string]string{"secrets/web.pass": "  s3cret  \r\n"},
			want:    "s3cret",
		},
		{
			name: "admin and guest from separate files",
			sermo: `
web:
  port: 9797
  password_file: secrets/web.pass
  guest_password_file: secrets/guest.pass
`,
			secrets: map[string]string{
				"secrets/web.pass":   "s3cret\n",
				"secrets/guest.pass": "lookonly\n",
			},
			want:      "s3cret",
			wantGuest: "lookonly",
		},
		{
			name: "inline password still works",
			sermo: `
web:
  port: 9797
  password: "s3cret"
  guest_password: "lookonly"
`,
			want:      "s3cret",
			wantGuest: "lookonly",
		},
		{
			name: "file for admin, inline for guest",
			sermo: `
web:
  port: 9797
  password_file: secrets/web.pass
  guest_password: "lookonly"
`,
			secrets:   map[string]string{"secrets/web.pass": "s3cret\n"},
			want:      "s3cret",
			wantGuest: "lookonly",
		},
		{
			name: "env expansion applies to the path",
			sermo: `
web:
  port: 9797
  password_file: "${env:SERMO_TEST_SECRET_DIR}/web.pass"
`,
			secrets: map[string]string{"secrets/web.pass": "s3cret\n"},
			want:    "s3cret",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// The env case needs the temp root before writeConfig picks it; set
			// a relative dir instead, which resolves against the sermo.yml dir.
			t.Setenv("SERMO_TEST_SECRET_DIR", "secrets")
			cfg := loadWebGlobal(t, tc.sermo, tc.secrets)
			if issues := Validate(cfg); len(issues) != 0 {
				t.Fatalf("Validate() = %v, want none", issues)
			}
			if got := cfg.Global.WebPassword(); got != tc.want {
				t.Errorf("WebPassword() = %q, want %q", got, tc.want)
			}
			if got := cfg.Global.WebGuestPassword(); got != tc.wantGuest {
				t.Errorf("WebGuestPassword() = %q, want %q", got, tc.wantGuest)
			}
		})
	}
}

func TestWebPasswordFileIssues(t *testing.T) {
	tests := []struct {
		name    string
		sermo   string
		secrets map[string]string
		want    string
	}{
		{
			name: "password and password_file are mutually exclusive",
			sermo: `
web:
  port: 9797
  password: "s3cret"
  password_file: secrets/web.pass
`,
			secrets: map[string]string{"secrets/web.pass": "other\n"},
			want:    "web.password and web.password_file are mutually exclusive",
		},
		{
			name: "guest_password and guest_password_file are mutually exclusive",
			sermo: `
web:
  port: 9797
  guest_password: "lookonly"
  guest_password_file: secrets/guest.pass
`,
			secrets: map[string]string{"secrets/guest.pass": "other\n"},
			want:    "web.guest_password and web.guest_password_file are mutually exclusive",
		},
		{
			name: "empty path",
			sermo: `
web:
  port: 9797
  password_file: "   "
`,
			want: "web.password_file must name a file holding the password",
		},
		{
			name: "not a string",
			sermo: `
web:
  port: 9797
  password_file: 42
`,
			want: "web.password_file must be a string",
		},
		{
			name: "missing file",
			sermo: `
web:
  port: 9797
  password_file: secrets/absent.pass
`,
			want: "web.password_file cannot be read",
		},
		{
			name: "empty file",
			sermo: `
web:
  port: 9797
  password_file: secrets/web.pass
`,
			secrets: map[string]string{"secrets/web.pass": "\n\n"},
			want:    "is empty",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mustHave(t, Validate(loadWebGlobal(t, tc.sermo, tc.secrets)), tc.want)
		})
	}
}

// An unreadable secret file must not abort the load: `config validate` has to
// report it, and unrelated CLI commands must keep working.
func TestWebPasswordFileUnreadableIsNotALoadError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads mode-0000 files")
	}
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
web:
  port: 9797
  password_file: secrets/web.pass
`,
		"secrets/web.pass": "s3cret\n",
	})
	secret := filepath.Join(filepath.Dir(global), "secrets", "web.pass")
	if err := os.Chmod(secret, 0o000); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if got := cfg.Global.WebPassword(); got != "" {
		t.Errorf("WebPassword() = %q, want empty", got)
	}
	mustHave(t, Validate(cfg), "web.password_file cannot be read")
}
