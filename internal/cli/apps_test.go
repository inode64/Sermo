package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sermo/internal/execx"
)

func TestAppVersionCommandExpectations(t *testing.T) {
	tree := map[string]any{
		"commands": map[string]any{
			"version": map[string]any{
				"command":       []any{"/bin/tool", "--version"},
				"expect_exit":   3,
				"expect_stdout": "v1.",
				"expect_stderr": map[string]any{"op": "==", "value": ""},
			},
		},
	}
	vc := appVersionCommand(tree)
	if len(vc.argv) != 2 || vc.argv[0] != "/bin/tool" {
		t.Fatalf("argv = %v", vc.argv)
	}
	if vc.expectExit != 3 {
		t.Errorf("expectExit = %d, want 3", vc.expectExit)
	}
	if vc.stdout.Substring != "v1." {
		t.Errorf("stdout matcher = %+v, want substring v1.", vc.stdout)
	}
	if vc.stderr.Op != "==" {
		t.Errorf("stderr matcher = %+v, want op ==", vc.stderr)
	}
}

// fakeRunner answers version-command invocations keyed by the binary path.
type fakeRunner struct{ byPath map[string]execx.Result }

func (f fakeRunner) Run(_ context.Context, name string, _ ...string) (execx.Result, error) {
	if r, ok := f.byPath[name]; ok {
		return r, nil
	}
	return execx.Result{ExitCode: 127}, fmt.Errorf("%s: not found", name)
}

func TestAppsCommand(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(binDir, "good")
	bad := filepath.Join(binDir, "bad")
	missing := filepath.Join(binDir, "missing") // never created
	for _, p := range []string{good, bad} {
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	profilesDir := filepath.Join(root, "profiles")
	appsDir := filepath.Join(profilesDir, "apps") // category derived from the directory
	enabledDir := filepath.Join(root, "enabled")
	for _, d := range []string{appsDir, enabledDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeProfile := func(file, name, display, binary string) {
		body := fmt.Sprintf(`kind: profile
name: %s
display_name: %q
service: { name: %s }
variables:
  binary: %q
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}","--version"], timeout: 10s }
`, name, display, name, binary)
		if err := os.WriteFile(filepath.Join(appsDir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeProfile("good.yml", "goodapp", "GoodApp", good)
	writeProfile("bad.yml", "badapp", "BadApp", bad)
	writeProfile("gone.yml", "goneapp", "GoneApp", missing)

	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths: { profiles: [ %s ], enabled: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, profilesDir, enabledDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := fakeRunner{byPath: map[string]execx.Result{
		good: {Stdout: "GoodApp 1.2.3\n", ExitCode: 0},
		bad:  {Stderr: "boom\n", ExitCode: 3},
	}}

	run := func(args ...string) string {
		var stdout bytes.Buffer
		app := App{
			Env:    func(string) string { return "" },
			Stdout: &stdout,
			Stderr: &bytes.Buffer{},
			Runner: runner,
		}
		if code := app.Run(context.Background(), append([]string{"--config", global}, args...)); code != exitSuccess {
			t.Fatalf("apps %v exit = %d", args, code)
		}
		return stdout.String()
	}

	// Default: only installed apps, with version and status.
	out := run("apps")
	if !strings.Contains(out, "GoodApp") || !strings.Contains(out, "GoodApp 1.2.3") || !strings.Contains(out, "ok") {
		t.Errorf("apps missing good app row:\n%s", out)
	}
	if !strings.Contains(out, "BadApp") || !strings.Contains(out, "exit 3 (want 0): boom") {
		t.Errorf("apps missing bad app error:\n%s", out)
	}
	if strings.Contains(out, "GoneApp") {
		t.Errorf("apps should hide not-installed app by default:\n%s", out)
	}

	// `apps all` also lists the not-installed app.
	outAll := run("apps", "all")
	if !strings.Contains(outAll, "GoneApp") || !strings.Contains(outAll, "not installed") {
		t.Errorf("apps all should list not-installed app:\n%s", outAll)
	}

	// JSON carries the structured fields.
	var jstdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &jstdout, Stderr: &bytes.Buffer{}, Runner: runner}
	if code := app.Run(context.Background(), []string{"--config", global, "--json", "apps"}); code != exitSuccess {
		t.Fatalf("apps --json exit = %d", code)
	}
	js := jstdout.String()
	if !strings.Contains(js, `"version":"GoodApp 1.2.3"`) || !strings.Contains(js, `"installed":true`) || !strings.Contains(js, `"ok":false`) {
		t.Errorf("apps --json unexpected:\n%s", js)
	}
}
