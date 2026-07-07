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
