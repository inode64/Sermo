package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestEnsureConfigPathDirPreservesOperatorText(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    string
		keep     []string // substrings (comments, order) that must survive
		wantList []string
	}{
		{
			name:     "block list with comments",
			input:    "# Operator notes\nengine:\n  backend: auto # pinned\npaths:\n  watches:\n    - net.d # first\n  runtime: /run/sermo\n# trailer\n",
			keep:     []string{"# Operator notes", "backend: auto # pinned", "- net.d # first", "# trailer", "engine:\n  backend"},
			wantList: []string{"net.d", "storages"},
		},
		{
			name:     "flow list",
			input:    "engine:\n  interval: 30s # cadence\npaths:\n  watches: [net.d] # list\n",
			keep:     []string{"interval: 30s # cadence", "[net.d, storages] # list"},
			wantList: []string{"net.d", "storages"},
		},
		{
			name:     "scalar entry",
			input:    "# head\npaths:\n  watches: net.d\n",
			keep:     []string{"# head"},
			wantList: []string{"net.d", "storages"},
		},
		{
			name:     "missing key",
			input:    "# head\npaths:\n  runtime: /run/sermo # rt\n",
			keep:     []string{"# head", "runtime: /run/sermo # rt"},
			wantList: []string{"storages"},
		},
		{
			name:     "missing paths",
			input:    "# head\nengine:\n  backend: auto # pinned\n",
			keep:     []string{"# head", "backend: auto # pinned"},
			wantList: []string{"storages"},
		},
		{
			// Not expressible as a syntax edit: falls back to a plain render
			// that is still the correct config.
			name:     "flow mapping falls back",
			input:    "paths: {runtime: /run/x}\n",
			wantList: []string{"storages"},
		},
		{
			name:     "empty file",
			input:    "",
			wantList: []string{"storages"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			global := filepath.Join(dir, "sermo.yml")
			if err := os.WriteFile(global, []byte(tc.input), 0o640); err != nil {
				t.Fatal(err)
			}
			bak, err := ensureConfigPathDir(global, "watches", "storages", filepath.Join(dir, "storages"))
			if err != nil {
				t.Fatalf("ensureConfigPathDir() = %v", err)
			}
			out, err := os.ReadFile(global)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.keep {
				if !strings.Contains(string(out), want) {
					t.Errorf("output lost %q:\n%s", want, out)
				}
			}
			var doc struct {
				Paths struct {
					Watches []string `yaml:"watches"`
				} `yaml:"paths"`
			}
			if err := yaml.Unmarshal(out, &doc); err != nil {
				t.Fatalf("output is not valid YAML: %v\n%s", err, out)
			}
			if !reflect.DeepEqual(doc.Paths.Watches, tc.wantList) {
				t.Fatalf("paths.watches = %v, want %v\n%s", doc.Paths.Watches, tc.wantList, out)
			}
			if got, err := os.ReadFile(bak); err != nil || string(got) != tc.input {
				t.Fatalf("backup %s = %q (%v), want the original", bak, got, err)
			}
			info, err := os.Stat(global)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o640 {
				t.Fatalf("mode = %o, want the original 0640", info.Mode().Perm())
			}
			assertNoStagingFiles(t, dir)
		})
	}
}

// A second wizard run must not overwrite the first backup (the operator's
// original) with the already rewritten config.
func TestEnsureConfigPathDirNeverOverwritesBackup(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	original := "# original\nengine:\n  interval: 30s\n"
	if err := os.WriteFile(global, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := ensureConfigPathDir(global, "watches", "storages", filepath.Join(dir, "storages"))
	if err != nil {
		t.Fatal(err)
	}
	afterFirst, err := os.ReadFile(global)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ensureConfigPathDir(global, "services", "services.d", filepath.Join(dir, "services.d"))
	if err != nil {
		t.Fatal(err)
	}
	if first != global+".bak" || second == first || !strings.HasPrefix(second, global+".bak.") {
		t.Fatalf("backups = %q, %q", first, second)
	}
	if got, _ := os.ReadFile(first); string(got) != original {
		t.Fatalf("first backup overwritten: %q", got)
	}
	if got, _ := os.ReadFile(second); string(got) != string(afterFirst) {
		t.Fatalf("second backup = %q, want %q", got, afterFirst)
	}
}

// A symlinked sermo.yml keeps its link; the file it points at is rewritten.
func TestEnsureConfigPathDirFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real", "sermo.yml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("engine:\n  interval: 30s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "sermo.yml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureConfigPathDir(link, "watches", "storages", filepath.Join(dir, "storages")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced by a regular file: %v", err)
	}
	if got, _ := os.ReadFile(target); !strings.Contains(string(got), "storages") {
		t.Fatalf("link target not updated: %s", got)
	}
}

func assertNoStagingFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("staging file left behind: %s", e.Name())
		}
	}
}
