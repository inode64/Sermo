package config

import (
	"reflect"
	"testing"
)

func TestBindirCandidates(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "prefix",
			in:   "${bindir}/mysqld",
			want: []string{"/usr/bin/mysqld", "/usr/sbin/mysqld", "/usr/local/bin/mysqld", "/usr/local/sbin/mysqld"},
		},
		{
			name: "combined with version template",
			in:   "${bindir}/php-fpm${version}",
			want: []string{
				"/usr/bin/php-fpm${version}",
				"/usr/sbin/php-fpm${version}",
				"/usr/local/bin/php-fpm${version}",
				"/usr/local/sbin/php-fpm${version}",
			},
		},
		{
			name: "no marker returns nil",
			in:   "/opt/custom/bin/foo",
			want: nil,
		},
		{
			name: "non-path value returns nil",
			in:   "demo",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bindirCandidates(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("bindirCandidates(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestExpandBindirValue(t *testing.T) {
	t.Run("string with marker becomes candidate list", func(t *testing.T) {
		got := expandBindirValue("${bindir}/mysqld")
		want := []any{"/usr/bin/mysqld", "/usr/sbin/mysqld", "/usr/local/bin/mysqld", "/usr/local/sbin/mysqld"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("list flattens marker and literal items in order", func(t *testing.T) {
		got := expandBindirValue([]any{"${bindir}/mariadbd", "/opt/mysql/bin/mysqld"})
		want := []any{
			"/usr/bin/mariadbd", "/usr/sbin/mariadbd", "/usr/local/bin/mariadbd", "/usr/local/sbin/mariadbd",
			"/opt/mysql/bin/mysqld",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("value without marker is unchanged", func(t *testing.T) {
		if got := expandBindirValue("demo"); got != "demo" {
			t.Errorf("got %v, want %q", got, "demo")
		}
		if got := expandBindirValue(8080); got != 8080 {
			t.Errorf("got %v, want %d", got, 8080)
		}
	})
}

// TestExpandBindirOnLoad checks that ${bindir} is expanded in stored document
// bodies at load time, so downstream resolution and validation only see the
// concrete candidate list.
func TestExpandBindirOnLoad(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/demo.yml": `
name: demo
display_name: "Demo"
variables:
  binary: ${bindir}/demo
preflight:
  binary: { type: binary, path: "${binary}" }
`,
	})

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	doc := cfg.Apps["demo"]
	if doc == nil {
		t.Fatal("demo app not loaded")
	}
	vars, _ := doc.Body["variables"].(map[string]any)
	got := vars["binary"]
	want := []any{"/usr/bin/demo", "/usr/sbin/demo", "/usr/local/bin/demo", "/usr/local/sbin/demo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("variables.binary = %#v, want %#v", got, want)
	}

	// Off-host (none of the candidates exist), the binary resolves to the first
	// candidate so the value stays a well-formed absolute path.
	if bin := DocumentBinary(doc.Body); bin != "/usr/bin/demo" {
		t.Errorf("DocumentBinary = %q, want %q", bin, "/usr/bin/demo")
	}

	// The expanded document validates cleanly (absolute candidate paths).
	for _, issue := range Validate(cfg) {
		t.Errorf("unexpected validation issue: %s", issue)
	}
}

// versions.from and versions.current_from name binaries in the same standard
// directories variables.binary does, so ${bindir} has to reach them too --
// otherwise a template discovering a family of tools silently matches nothing.
func TestExpandBindirVersionDiscoveryPaths(t *testing.T) {
	expanded := func(suffix string) []any {
		return []any{"/usr/bin/" + suffix, "/usr/sbin/" + suffix, "/usr/local/bin/" + suffix, "/usr/local/sbin/" + suffix}
	}
	tests := []struct {
		name     string
		versions map[string]any
		key      string
		want     any
	}{
		{
			name:     "from",
			key:      "from",
			versions: map[string]any{"from": "${bindir}/db${version}"},
			want:     expanded("db${version}"),
		},
		{
			name:     "current_from",
			key:      "current_from",
			versions: map[string]any{"current_from": "${bindir}/db"},
			want:     expanded("db"),
		},
		{
			name:     "backend keyed from",
			key:      "from",
			versions: map[string]any{"from": map[string]any{"openrc": "${bindir}/db${version}"}},
			want:     map[string]any{"openrc": expanded("db${version}")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := map[string]any{keyVersions: tt.versions}
			expandBindirVersionPaths(body)
			versions, _ := body[keyVersions].(map[string]any)
			if got := versions[tt.key]; !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("versions.%s = %#v, want %#v", tt.key, got, tt.want)
			}
		})
	}
}

// A versions block without ${bindir}, or without discovery paths at all, passes
// through untouched.
func TestExpandBindirVersionPathsLeavesOtherValues(t *testing.T) {
	body := map[string]any{keyVersions: map[string]any{
		"from":   "/opt/db${version}/bin/db",
		"suffix": "_*",
	}}
	expandBindirVersionPaths(body)
	versions, _ := body[keyVersions].(map[string]any)
	if got := versions["from"]; got != "/opt/db${version}/bin/db" {
		t.Fatalf("versions.from = %#v, want the original string", got)
	}
	if got := versions["suffix"]; got != "_*" {
		t.Fatalf("versions.suffix = %#v, want %q", got, "_*")
	}
}
