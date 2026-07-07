package appinspect

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/execx"
)

type testRunner map[string]execx.Result

func (r testRunner) Run(_ context.Context, name string, _ ...string) (execx.Result, error) {
	if res, ok := r[name]; ok {
		return res, nil
	}
	return execx.Result{ExitCode: 127}, fmt.Errorf("%s: not found", name)
}

type testUserRunner struct {
	testRunner
	users []string
	names []string
}

func (r *testUserRunner) RunUser(ctx context.Context, user string, name string, args ...string) (execx.Result, error) {
	r.users = append(r.users, user)
	r.names = append(r.names, name)
	return r.Run(ctx, name, args...)
}

func TestInspectUsesNamespacedAppPreflight(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "webd")
	if err := os.WriteFile(binary, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved := config.Resolved{Tree: map[string]any{
		"preflight": map[string]any{
			"web-binary":  map[string]any{"type": "binary", "path": binary},
			"web-version": map[string]any{"type": "command", "command": []any{binary, "--version"}},
		},
	}}

	report := Inspect(context.Background(), testRunner{
		binary: {Stdout: "Webd 1.2.3\n", ExitCode: 0},
	}, "web", resolved)
	if !report.Installed || !report.OK || report.Binary != binary || report.Status != "ok" {
		t.Fatalf("Inspect() = %+v, want installed ok report for namespaced binary", report)
	}
	if report.Version != "Webd 1.2.3" || report.VersionShort != "1.2.3" {
		t.Fatalf("version = %q short=%q, want Webd 1.2.3 / 1.2.3", report.Version, report.VersionShort)
	}
}

func TestInspectCommandUser(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "postgres")
	if err := os.WriteFile(binary, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved := config.Resolved{Tree: map[string]any{
		"preflight": map[string]any{
			"binary":  map[string]any{"type": "binary", "path": binary},
			"version": map[string]any{"type": "command", "user": "postgres", "command": []any{binary, "--version"}},
		},
	}}
	runner := &testUserRunner{testRunner: testRunner{binary: {Stdout: "postgres 17.5\n", ExitCode: 0}}}

	report := Inspect(context.Background(), runner, "postgres", resolved)
	if !report.OK || report.VersionShort != "17.5" {
		t.Fatalf("Inspect() = %+v, want ok postgres version", report)
	}
	if !slices.Equal(runner.users, []string{"postgres"}) || !slices.Equal(runner.names, []string{binary}) {
		t.Fatalf("RunUser calls users=%v names=%v", runner.users, runner.names)
	}
}

func TestListPolkitVersionFromPkexecIntegerOutput(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	catalogDir := filepath.Join(root, "catalog")
	servicesDir := filepath.Join(root, "services")
	for _, dir := range []string{binDir, filepath.Join(catalogDir, "apps"), filepath.Join(catalogDir, "services"), servicesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	polkitd := filepath.Join(binDir, "polkitd")
	pkexec := filepath.Join(binDir, "pkexec")
	for _, path := range []string{polkitd, pkexec} {
		if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(catalogDir, "apps", "polkit.yml"), []byte(fmt.Sprintf(`
name: polkit
display_name: "Polkit"
category: system
variables:
  binary: %q
  pkexec: %q
preflight:
  binary: { type: binary, path: "${binary}" }
  pkexec: { type: binary, path: "${pkexec}" }
  version: { type: command, command: ["${pkexec}", "--version"], timeout: 10s }
`, polkitd, pkexec)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catalogDir, "services", "polkit.yml"), []byte(`
name: polkit
display_name: "Polkit"
category: system
service:
  systemd: [polkit]
apps: [polkit]
checks:
  service: { type: service, expect: active }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: systemd }
paths: { services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, servicesDir)), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(global, config.WithCatalogDirs(catalogDir))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	runner := testRunner{pkexec: {Stdout: "pkexec version 126\n", ExitCode: 0}}

	apps := List(context.Background(), runner, cfg, config.CategoryApp, false)
	if len(apps) != 1 {
		t.Fatalf("app reports = %+v, want one installed polkit app", apps)
	}
	if apps[0].Version != "pkexec version 126" || apps[0].VersionShort != "126" {
		t.Fatalf("polkit app version = %q short=%q, want pkexec version 126 / 126", apps[0].Version, apps[0].VersionShort)
	}

	services := List(context.Background(), runner, cfg, config.CategoryService, false, WithOptionalVersion())
	if len(services) != 1 {
		t.Fatalf("service reports = %+v, want one installed polkit service", services)
	}
	if services[0].Version != "pkexec version 126" || services[0].VersionShort != "126" {
		t.Fatalf("polkit service version = %q short=%q, want pkexec version 126 / 126", services[0].Version, services[0].VersionShort)
	}
}

func TestListMarksTemplateCurrentByBaseShortVersion(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	jvmDir := filepath.Join(root, "jvm")
	catalogDir := filepath.Join(root, "catalog")
	servicesDir := filepath.Join(root, "services")
	for _, dir := range []string{binDir, jvmDir, filepath.Join(catalogDir, "apps"), servicesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	currentJava := filepath.Join(binDir, "java")
	java21 := filepath.Join(jvmDir, "openjdk-bin-21.0.11_p10", "bin", "java")
	java25 := filepath.Join(jvmDir, "openjdk-bin-25.0.3_p9", "bin", "java")
	for _, path := range []string{currentJava, java21, java25} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(catalogDir, "apps", "java.yml"), []byte(fmt.Sprintf(`
name: java-%%i-%%v
display_name: "Java ${instance} ${version} ${current}"
versions:
  from: "%s/${instance}-bin-${version}/bin/java"
  current_from: "%s"
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "-version"], timeout: 10s }
`, jvmDir, currentJava)), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths: { services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, servicesDir)), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(global, config.WithCatalogDirs(catalogDir))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := config.DisplayName(cfg.Apps["java-openjdk-21.0.11_p10"].Body, ""); got != "Java openjdk 21.0.11_p10" {
		t.Fatalf("loaded display name = %q, want no static current marker", got)
	}

	apps := List(context.Background(), testRunner{
		currentJava: {Stderr: "openjdk version \"21.0.11\" 2026-04-21 LTS\n", ExitCode: 0},
		java21:      {Stderr: "openjdk version \"21.0.11\" 2026-04-21 LTS\n", ExitCode: 0},
		java25:      {Stderr: "openjdk version \"25.0.3\" 2026-04-21 LTS\n", ExitCode: 0},
	}, cfg, config.CategoryApp, false)
	byName := map[string]Report{}
	for _, app := range apps {
		byName[app.Name] = app
	}
	if got := byName["java-openjdk-21.0.11_p10"].DisplayName; got != "Java openjdk 21.0.11_p10 current" {
		t.Fatalf("java 21 display name = %q, want current marker", got)
	}
	if got := byName["java-openjdk-25.0.3_p9"].DisplayName; got != "Java openjdk 25.0.3_p9" {
		t.Fatalf("java 25 display name = %q, want no current marker", got)
	}
}

func TestInspectCanTreatVersionFailureAsOptional(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "webd")
	if err := os.WriteFile(binary, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved := config.Resolved{Tree: map[string]any{
		"preflight": map[string]any{
			"binary":  map[string]any{"type": "binary", "path": binary},
			"version": map[string]any{"type": "command", "command": []any{binary, "--version"}},
		},
	}}
	runner := testRunner{binary: {Stderr: "bad flag\n", ExitCode: 2}}

	strict := Inspect(context.Background(), runner, "web", resolved)
	if strict.OK || strict.Status == "ok" {
		t.Fatalf("strict Inspect() = %+v, want version failure", strict)
	}
	if !strings.Contains(strict.Output, "bad flag") {
		t.Fatalf("a failing probe must capture the command output, got %q", strict.Output)
	}
	optional := Inspect(context.Background(), runner, "web", resolved, WithOptionalVersion())
	if !optional.OK || optional.Status != "ok" {
		t.Fatalf("optional Inspect() = %+v, want ok with unknown version", optional)
	}

	resolved.Tree["version_match"] = map[string]any{"contains": "Webd"}
	matched := Inspect(context.Background(), runner, "web", resolved, WithOptionalVersion())
	if matched.Installed || !strings.HasPrefix(matched.Status, "not installed: version ") {
		t.Fatalf("version_match Inspect() = %+v, want identity failure despite optional version", matched)
	}
}

func TestShortVersionForIgnoresFailedCommand(t *testing.T) {
	tree := map[string]any{
		"commands": map[string]any{
			"version_short": map[string]any{"command": []any{"/bin/tool", "--short"}},
		},
	}

	// A version_short command that exits non-zero must NOT have its (garbage)
	// output trusted; fall back to parsing the raw version line.
	failing := testRunner{"/bin/tool": {Stdout: "ERROR: bad usage\n", ExitCode: 1}}
	if got := shortVersionFor(context.Background(), failing, tree, "Webd 1.2.3"); got != "1.2.3" {
		t.Fatalf("shortVersionFor on failed command = %q, want fallback 1.2.3", got)
	}

	// A successful command's first line is trusted verbatim.
	ok := testRunner{"/bin/tool": {Stdout: "2.0.0\n", ExitCode: 0}}
	if got := shortVersionFor(context.Background(), ok, tree, "Webd 1.2.3"); got != "2.0.0" {
		t.Fatalf("shortVersionFor on success = %q, want 2.0.0", got)
	}
}

func TestProbeCommandFor(t *testing.T) {
	tree := map[string]any{
		"commands": map[string]any{
			"version": map[string]any{
				"command":       []any{"/bin/tool", "--version"},
				"user":          "postgres",
				"timeout":       "10s",
				"expect_exit":   3,
				"expect_stdout": "v1.",
				"expect_stderr": map[string]any{"op": "==", "value": ""},
			},
		},
	}
	vc := probeCommandFor(tree, "version")
	if len(vc.argv) != 2 || vc.argv[0] != "/bin/tool" {
		t.Fatalf("argv = %v", vc.argv)
	}
	if vc.user != "postgres" {
		t.Errorf("user = %q, want postgres", vc.user)
	}
	if vc.timeout != 10*time.Second {
		t.Errorf("timeout = %v, want 10s", vc.timeout)
	}
	if !slices.Equal(vc.expectExit, []int{3}) {
		t.Errorf("expectExit = %v, want [3]", vc.expectExit)
	}
	if vc.stdout.Substring != "v1." {
		t.Errorf("stdout matcher = %+v, want substring v1.", vc.stdout)
	}
	if vc.stderr.Op != "==" {
		t.Errorf("stderr matcher = %+v, want op ==", vc.stderr)
	}
}

type timeoutObserver struct {
	timeout time.Duration
}

func (o *timeoutObserver) Run(ctx context.Context, name string, _ ...string) (execx.Result, error) {
	if deadline, ok := ctx.Deadline(); ok {
		o.timeout = time.Until(deadline)
	}
	return execx.Result{Stdout: "tool 1.2.3\n", ExitCode: 0}, nil
}

func TestInspectUsesCatalogProbeTimeout(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "salt-minion")
	if err := os.WriteFile(binary, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved := config.Resolved{Tree: map[string]any{
		"preflight": map[string]any{
			"binary":  map[string]any{"type": "binary", "path": binary},
			"version": map[string]any{"type": "command", "command": []any{binary, "--version"}, "timeout": "10s"},
		},
	}}
	obs := &timeoutObserver{}
	report := Inspect(context.Background(), obs, "salt-minion", resolved)
	if !report.OK || report.VersionShort != "1.2.3" {
		t.Fatalf("Inspect() = %+v, want ok version 1.2.3", report)
	}
	if obs.timeout < 9*time.Second || obs.timeout > 10*time.Second {
		t.Fatalf("probe timeout = %v, want ~10s from catalog entry", obs.timeout)
	}
}

type slowRunner struct{}

func (slowRunner) Run(ctx context.Context, _ string, _ ...string) (execx.Result, error) {
	<-ctx.Done()
	return execx.Result{ExitCode: -1}, fmt.Errorf("run tool: %w", ctx.Err())
}

func TestInspectProbeTimeoutFailureReportsUnderlyingError(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "slow-tool")
	if err := os.WriteFile(binary, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved := config.Resolved{Tree: map[string]any{
		"preflight": map[string]any{
			"binary":  map[string]any{"type": "binary", "path": binary},
			"version": map[string]any{"type": "command", "command": []any{binary, "--version"}, "timeout": "1ms"},
		},
	}}
	report := Inspect(context.Background(), slowRunner{}, "slow-tool", resolved)
	if report.OK {
		t.Fatalf("Inspect() = %+v, want version probe failure", report)
	}
	if !strings.Contains(report.Status, "timeout after 1ms") {
		t.Fatalf("status = %q, want timeout after duration instead of exit -1", report.Status)
	}
}
