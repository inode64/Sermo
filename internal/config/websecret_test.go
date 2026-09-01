package config

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sermo/internal/webcred"
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

// assertAccepts checks that list accepts every password in want, and holds
// nothing when want is empty. Credentials may be hashed, so the only honest
// assertion is whether they let a password through.
func assertAccepts(t *testing.T, list webcred.List, label string, want ...string) {
	t.Helper()
	want = slices.DeleteFunc(slices.Clone(want), func(s string) bool { return s == "" })
	if len(want) == 0 {
		if !list.Empty() {
			t.Errorf("%s is not empty, want no credentials", label)
		}
		return
	}
	if list.Empty() {
		t.Errorf("%s is empty, want configured credentials", label)
	}
	for _, password := range want {
		if !list.Verify(t.Context(), password) {
			t.Errorf("%s does not accept %q", label, password)
		}
	}
}

// assertRejects checks that credentials assigned to another role do not grant
// access through list.
func assertRejects(t *testing.T, list webcred.List, label string, passwords ...string) {
	t.Helper()
	for _, password := range passwords {
		if password != "" && list.Verify(t.Context(), password) {
			t.Errorf("%s accepts %q, want it rejected", label, password)
		}
	}
}

// mustHashLines returns a bcrypt credential line for password, at the cheapest
// work factor: these tests assert on parsing and verification, not on cost.
func mustHashLines(t *testing.T, password string) string {
	t.Helper()
	line, err := webcred.HashBcrypt(password, webcred.MinBcryptCost)
	if err != nil {
		t.Fatalf("HashBcrypt() error = %v", err)
	}
	return line
}

func TestWebPasswordFileSuppliesPasswords(t *testing.T) {
	tests := []struct {
		name      string
		sermo     string
		secrets   map[string]string
		want      []string
		wantGuest []string
	}{
		{
			name: "absolute path, trailing newline trimmed",
			sermo: `
web:
  port: 9797
  password_file: @ROOT@/secrets/web.pass
`,
			secrets: map[string]string{"secrets/web.pass": mustHashLines(t, "s3cret") + "\n"},
			want:    []string{"s3cret"},
		},
		{
			name: "path relative to sermo.yml",
			sermo: `
web:
  port: 9797
  password_file: secrets/web.pass
`,
			secrets: map[string]string{"secrets/web.pass": mustHashLines(t, "s3cret")},
			want:    []string{"s3cret"},
		},
		{
			name: "surrounding whitespace trimmed",
			sermo: `
web:
  port: 9797
  password_file: secrets/web.pass
`,
			secrets: map[string]string{"secrets/web.pass": "  " + mustHashLines(t, "s3cret") + "  \r\n"},
			want:    []string{"s3cret"},
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
				"secrets/web.pass":   mustHashLines(t, "s3cret") + "\n",
				"secrets/guest.pass": mustHashLines(t, "lookonly") + "\n",
			},
			want:      []string{"s3cret"},
			wantGuest: []string{"lookonly"},
		},
		{
			name: "env expansion applies to the path",
			sermo: `
web:
  port: 9797
  password_file: "${env:SERMO_TEST_SECRET_DIR}/web.pass"
`,
			secrets: map[string]string{"secrets/web.pass": mustHashLines(t, "s3cret") + "\n"},
			want:    []string{"s3cret"},
		},
		{
			name: "one credential per line, any of them grants access",
			sermo: `
web:
  port: 9797
  password_file: secrets/web.pass
`,
			secrets: map[string]string{"secrets/web.pass": "# admins\n" + mustHashLines(t, "first") + "\n\n" + mustHashLines(t, "second") + "\n" + mustHashLines(t, "third") + "\n"},
			want:    []string{"first", "second", "third"},
		},
		{
			name: "hashed credentials verify the password behind them",
			sermo: `
web:
  port: 9797
  password_file: secrets/web.pass
  guest_password_file: secrets/guest.pass
`,
			secrets: map[string]string{
				"secrets/web.pass":   mustHashLines(t, "s3cret") + "   # ana\n",
				"secrets/guest.pass": mustHashLines(t, "lookonly") + "\n",
			},
			want:      []string{"s3cret"},
			wantGuest: []string{"lookonly"},
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
			admin := cfg.Global.WebCredentials()
			guest := cfg.Global.WebGuestCredentials()
			assertAccepts(t, admin, "WebCredentials()", tc.want...)
			assertAccepts(t, guest, "WebGuestCredentials()", tc.wantGuest...)
			assertRejects(t, admin, "WebCredentials()", tc.wantGuest...)
			assertRejects(t, guest, "WebGuestCredentials()", tc.want...)
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
			name: "inline password is retired",
			sermo: `
web:
  port: 9797
  password: "s3cret"
`,
			want: "web.password is no longer supported; use web.password_file with hashed credentials",
		},
		{
			name: "inline guest password is retired",
			sermo: `
web:
  port: 9797
  guest_password: "lookonly"
`,
			want: "web.guest_password is no longer supported; use web.guest_password_file with hashed credentials",
		},
		{
			name: "empty path",
			sermo: `
web:
  port: 9797
  password_file: "   "
`,
			want: "web.password_file must name a file holding hashed credentials",
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
			want:    "web.password_file holds no credential",
		},
		{
			name: "only comments",
			sermo: `
web:
  port: 9797
  password_file: secrets/web.pass
`,
			secrets: map[string]string{"secrets/web.pass": "# who knows\n#  \n"},
			want:    "web.password_file holds no credential",
		},
		{
			name: "unknown hash format names the line",
			sermo: `
web:
  port: 9797
  password_file: secrets/web.pass
`,
			secrets: map[string]string{"secrets/web.pass": mustHashLines(t, "good") + "\n$md5$deadbeef\n"},
			want:    `web.password_file line 2: unsupported hash format "$md5$"`,
		},
		{
			name: "truncated bcrypt hash is not taken as a password",
			sermo: `
web:
  port: 9797
  password_file: secrets/web.pass
`,
			secrets: map[string]string{"secrets/web.pass": "$2a$12$tooshort\n"},
			want:    "web.password_file line 1: malformed bcrypt credential",
		},
		{
			name: "more credentials than the limit",
			sermo: `
web:
  port: 9797
  password_file: secrets/web.pass
`,
			secrets: map[string]string{"secrets/web.pass": strings.Repeat(mustHashLines(t, "pw")+"\n", webcred.MaxCredentials+1)},
			want:    "more than 64 credentials",
		},
		{
			name: "inline hash is retired too",
			sermo: `
web:
  port: 9797
  password: "$md5$deadbeef"
`,
			want: `web.password is no longer supported; use web.password_file with hashed credentials`,
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
		"secrets/web.pass": mustHashLines(t, "s3cret") + "\n",
	})
	secret := filepath.Join(filepath.Dir(global), "secrets", "web.pass")
	if err := os.Chmod(secret, 0o000); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	assertAccepts(t, cfg.Global.WebCredentials(), "WebCredentials()")
	mustHave(t, Validate(cfg), "web.password_file cannot be read")
}

// The permission warning in sermod stats these paths, so a relative
// password_file must come back resolved against the sermo.yml directory — a raw
// relative path would silently stat the wrong file, or none.
func TestWebCredentialFilesAreResolved(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
web:
  port: 9797
  password_file: secrets/web.pass
  guest_password_file: @ROOT@/secrets/guest.pass
`,
		"secrets/web.pass":   mustHashLines(t, "s3cret") + "\n",
		"secrets/guest.pass": mustHashLines(t, "lookonly") + "\n",
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	files := cfg.Global.WebCredentialFiles()
	if len(files) != 2 {
		t.Fatalf("WebCredentialFiles() = %v, want both files", files)
	}
	base := filepath.Dir(global)
	want := []string{filepath.Join(base, "secrets", "web.pass"), filepath.Join(base, "secrets", "guest.pass")}
	for i, path := range files {
		if path != want[i] {
			t.Errorf("WebCredentialFiles()[%d] = %q, want %q", i, path, want[i])
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("stat %q: %v", path, err)
		}
	}

	// No file keys, nothing to inspect.
	withoutFiles := loadWebGlobal(t, `
web:
  port: 9797
`, nil)
	if got := withoutFiles.Global.WebCredentialFiles(); len(got) != 0 {
		t.Errorf("WebCredentialFiles() without file fields = %v, want none", got)
	}
}
