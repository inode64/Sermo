package config

import (
	"sort"
	"testing"
)

func TestVersionLessOrdersNumerically(t *testing.T) {
	got := []string{"10.0", "8.3", "8.11", "9", "", "8.3.1"}
	sort.Slice(got, func(i, j int) bool { return versionLess(got[i], got[j]) })

	want := []string{"", "8.3", "8.3.1", "8.11", "9", "10.0"}
	if len(got) != len(want) {
		t.Fatalf("length mismatch: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("version order wrong:\n got  %v\n want %v", got, want)
		}
	}
}

func TestVersionLessNonNumericSuffix(t *testing.T) {
	// Non-numeric trailing segments fall back to a lexicographic compare on
	// that segment, keeping the comparator total and stable.
	if versionLess("8.3-rc2", "8.3-rc1") {
		t.Fatal("8.3-rc2 must not sort before 8.3-rc1")
	}
	if !versionLess("8.3", "8.3-rc1") {
		t.Fatal("8.3 (shorter) must sort before 8.3-rc1")
	}
}

func TestTrimVersionSuffix(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		globs   []string
		want    string
		trimmed bool
	}{
		{name: "subcommand suffix", value: "5.3_archive", globs: []string{"_*"}, want: "5.3", trimmed: true},
		{name: "longest suffix wins", value: "5.3_log_verify", globs: []string{"_*"}, want: "5.3", trimmed: true},
		{name: "bare version untouched", value: "5.3", globs: []string{"_*"}, want: "5.3"},
		{name: "no suffix match", value: "8.332-p09", globs: []string{"_*"}, want: "8.332-p09"},
		{name: "several globs", value: "1.2-beta", globs: []string{"_*", "-*"}, want: "1.2", trimmed: true},
		{name: "would empty the value", value: "_archive", globs: []string{"_*"}, want: "_archive"},
		{name: "trims to the shortest remainder", value: "5.3", globs: []string{".3"}, want: "5", trimmed: true},
		{name: "no globs", value: "5.3_archive", want: "5.3_archive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, trimmed := trimVersionSuffix(tt.value, tt.globs)
			if got != tt.want || trimmed != tt.trimmed {
				t.Fatalf("trimVersionSuffix(%q, %v) = (%q, %v), want (%q, %v)", tt.value, tt.globs, got, trimmed, tt.want, tt.trimmed)
			}
		})
	}
}

func TestValidateVersionsCurrentFromSpecs(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/java.yml": `
name: java-%i-%v
versions:
  current_from:
    - ""
    - { path: /usr/lib/jvm/static/bin/java }
    - 42
preflight:
  binary: { type: binary, path: "${binary}" }
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issues := Validate(cfg)
	mustHave(t, issues, "versions.current_from[0] must be a non-empty path string")
	mustHave(t, issues, "versions.current_from[1] must be a path string or list of path strings")
	mustHave(t, issues, "versions.current_from[2] must be a path string or list of path strings")
}

func TestValidateVersionsSuffixSpecs(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/db.yml": `
name: db%v
versions:
  from: /usr/bin/db${version}
  suffix:
    - "_*"
    - ""
    - "*"
    - 42
preflight:
  binary: { type: binary, path: "${binary}" }
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issues := Validate(cfg)
	mustHave(t, issues, "versions.suffix[1] must be a non-empty suffix glob")
	mustHave(t, issues, `versions.suffix[2] must start with a literal separator, not a wildcard: "*" would consume the version itself`)
	mustHave(t, issues, "versions.suffix[3] must be a suffix glob string or list of suffix glob strings")
}
