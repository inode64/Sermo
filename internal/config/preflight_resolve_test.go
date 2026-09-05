package config

import (
	"os"
	"path/filepath"
	"testing"

	"sermo/internal/cfgval"
)

func TestPreflightBinarySelectsExecutableCandidate(t *testing.T) {
	dir := t.TempDir()
	notExec := filepath.Join(dir, "not-exec")
	if err := os.WriteFile(notExec, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	execPath := filepath.Join(dir, "exec")
	if err := os.WriteFile(execPath, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/app.yml": `
name: app
variables:
  binary:
    - ` + notExec + `
    - ` + execPath + `
preflight:
  binary: { type: binary, path: "${binary}" }
checks:
  process: { type: process, exe: "${binary}", user: root }
`,
		"services/app-main.yml": "name: app-main\nuses: app\n",
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("app-main")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	vars := nested(t, resolved.Tree, "variables")
	if got := cfgval.String(vars["binary"]); got != execPath {
		t.Fatalf("variables.binary = %q, want executable %q", got, execPath)
	}
	bin := nested(t, nested(t, resolved.Tree, "preflight"), "binary")
	if got := cfgval.String(bin["path"]); got != execPath {
		t.Fatalf("preflight.binary.path = %q, want %q", got, execPath)
	}
	proc := nested(t, nested(t, resolved.Tree, "checks"), "process")
	if got := cfgval.String(proc["exe"]); got != execPath {
		t.Fatalf("process exe = %q, want %q", got, execPath)
	}
}

func TestPreflightCandidateGlobResolvesAcrossLayouts(t *testing.T) {
	dir := t.TempDir()
	multiarch := filepath.Join(dir, "lib", "x86_64-linux-gnu")
	if err := os.MkdirAll(multiarch, 0o755); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(multiarch, "libdemo.so.1")
	if err := os.WriteFile(lib, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/app.yml": `
name: app
variables:
  binary:
    - ` + filepath.Join(dir, "lib64", "libdemo.so.1") + `
    - "` + filepath.Join(dir, "lib", "*-linux-gnu*", "libdemo.so.1") + `"
preflight:
  file: { type: file, path: "${binary}" }
checks:
  lib: { type: file, path: "${binary}" }
`,
		"services/app-main.yml": "name: app-main\nuses: app\n",
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("app-main")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	vars := nested(t, resolved.Tree, "variables")
	if got := cfgval.String(vars["binary"]); got != lib {
		t.Fatalf("variables.binary = %q, want glob match %q", got, lib)
	}
	check := nested(t, nested(t, resolved.Tree, "checks"), "lib")
	if got := cfgval.String(check["path"]); got != lib {
		t.Fatalf("check path = %q, want %q", got, lib)
	}
}

func TestCandidateGlobFallbackPrefersLiteral(t *testing.T) {
	dir := t.TempDir()
	literal := filepath.Join(dir, "lib64", "libdemo.so.1")
	// Nothing exists: the glob rung must not become the value — a pattern makes
	// a poor missing-file message — and the literal rung must.
	got := firstExistingStringPath([]string{
		filepath.Join(dir, "lib", "*-linux-gnu*", "libdemo.so.1"),
		literal,
	})
	if got != literal {
		t.Fatalf("fallback = %q, want literal %q", got, literal)
	}
	// All rungs are patterns: the first pattern is still better than "".
	pattern := filepath.Join(dir, "lib", "*", "libdemo.so.1")
	if got := firstExistingStringPath([]string{pattern}); got != pattern {
		t.Fatalf("all-pattern fallback = %q, want %q", got, pattern)
	}
}

func TestPidfileRejectsRelativeCandidate(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/app.yml": `
name: app
pidfile: run/app.pid
`,
		"services/app-main.yml": "name: app-main\nuses: app\n",
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issues := Validate(cfg)
	if !hasIssue(issues, `pidfile path "run/app.pid" must be absolute`) {
		t.Fatalf("Validate issues = %v, want relative pidfile issue", issues)
	}
}
