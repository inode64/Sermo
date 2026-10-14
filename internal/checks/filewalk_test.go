package checks

import (
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The log and size checks resolve their path through the same expansion: a
// literal path passes through untouched, a glob yields its sorted matches (none
// is a valid answer) and reports itself as a glob, a malformed pattern errors.
func TestExpandPathGlob(t *testing.T) {
	root := t.TempDir()
	writeSizedTree(t, root, map[string]int{"b.log": 1, "a.log": 1, "c.txt": 1})

	literal := filepath.Join(root, "missing.log")
	paths, glob, err := expandPathGlob(literal)
	if err != nil || glob || !slices.Equal(paths, []string{literal}) {
		t.Fatalf("literal = %v, %v, %v; want [%s], false, nil", paths, glob, err, literal)
	}
	paths, glob, err = expandPathGlob(filepath.Join(root, "*.log"))
	want := []string{filepath.Join(root, "a.log"), filepath.Join(root, "b.log")}
	if err != nil || !glob || !slices.Equal(paths, want) {
		t.Fatalf("glob = %v, %v, %v; want %v, true, nil", paths, glob, err, want)
	}
	paths, glob, err = expandPathGlob(filepath.Join(root, "*.gz"))
	if err != nil || !glob || len(paths) != 0 {
		t.Fatalf("unmatched glob = %v, %v, %v; want none, true, nil", paths, glob, err)
	}
	if _, _, err = expandPathGlob(filepath.Join(root, "[bad")); err == nil {
		t.Fatal("a malformed pattern must be an error")
	}
	if _, _, err = expandPathGlob(filepath.Join(root, "missing", "*.log")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing fixed directory = %v, want ErrNotExist", err)
	}
	if _, _, err = expandPathGlob(filepath.Join(root, "a.log", "*.log")); !errors.Is(err, errGlobBaseNotDir) {
		t.Fatalf("a file as the fixed directory = %v, want errGlobBaseNotDir", err)
	}
}

func TestGlobBase(t *testing.T) {
	for pattern, want := range map[string]string{
		"/var/lib/mysql/*bin*.[0-9]*": "/var/lib/mysql",
		"/data/*/logs/*.log":          "/data",
		"/*.log":                      "/",
	} {
		if got := globBase(pattern); got != want {
			t.Errorf("globBase(%q) = %q, want %q", pattern, got, want)
		}
	}
}

func TestParsePatternPath(t *testing.T) {
	cases := []struct {
		path, want, err string
	}{
		{path: "/var/log/../log/app.log", want: "/var/log/app.log"},
		{path: "/var/lib/mysql/*bin*.[0-9]*", want: "/var/lib/mysql/*bin*.[0-9]*"},
		{path: "", err: "size check requires a path"},
		{path: "var/log/app.log", err: "size check path must be absolute"},
		{path: "/var/lib/mysql/[bin*", err: "is not a valid glob"},
	}
	for _, tc := range cases {
		got, err := parsePatternPath(map[string]any{CheckKeyPath: tc.path}, CheckTypeSize)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("parsePatternPath(%q) error = %v, want %q", tc.path, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("parsePatternPath(%q) = %q, %v; want %q", tc.path, got, err, tc.want)
		}
	}
}
