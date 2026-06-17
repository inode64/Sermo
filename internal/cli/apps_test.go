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

// TestAppsVersionShortCommand checks how version_short is sourced: a daemon that
// configures a `version_short` command has its bare output trusted verbatim (no
// regex), while one without falls back to parsing the raw version line, and a
// configured command that prints nothing also falls back.
func TestAppsVersionShortCommand(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// One binary per app; the version_short programs are separate paths the
	// fakeRunner keys on and need not exist on disk (they are never stat'd).
	native := filepath.Join(binDir, "native")
	fallback := filepath.Join(binDir, "fallback")
	empty := filepath.Join(binDir, "empty")
	for _, p := range []string{native, fallback, empty} {
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	daemonsDir := filepath.Join(root, "daemons")
	appsDir := filepath.Join(daemonsDir, "apps")
	enabledDir := filepath.Join(root, "enabled")
	for _, d := range []string{appsDir, enabledDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// nativeapp: a version_short command prints the bare version directly.
	if err := os.WriteFile(filepath.Join(appsDir, "native.yml"), []byte(fmt.Sprintf(`kind: app
name: nativeapp
display_name: "NativeApp"
binary: %q
variables: { shortprog: %q }
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}","--version"], timeout: 10s }
  version_short: { type: command, command: ["${shortprog}"], timeout: 10s }
`, native, native+"-vs")), 0o644); err != nil {
		t.Fatal(err)
	}
	// fallbackapp: no version_short command — parse the raw version line.
	if err := os.WriteFile(filepath.Join(appsDir, "fallback.yml"), []byte(fmt.Sprintf(`kind: app
name: fallbackapp
display_name: "FallbackApp"
binary: %q
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}","--version"], timeout: 10s }
`, fallback)), 0o644); err != nil {
		t.Fatal(err)
	}
	// emptyapp: version_short command runs but prints nothing — fall back.
	if err := os.WriteFile(filepath.Join(appsDir, "empty.yml"), []byte(fmt.Sprintf(`kind: app
name: emptyapp
display_name: "EmptyApp"
binary: %q
variables: { shortprog: %q }
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}","--version"], timeout: 10s }
  version_short: { type: command, command: ["${shortprog}"], timeout: 10s }
`, empty, empty+"-vs")), 0o644); err != nil {
		t.Fatal(err)
	}

	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths: { catalog: [ %s ], includes: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, daemonsDir, enabledDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := fakeRunner{byPath: map[string]execx.Result{
		// Raw version lines whose regex-short would differ from the command,
		// so the assertions distinguish the two sources.
		native:         {Stdout: "NativeApp 4.5.6-7-extra\n", ExitCode: 0},
		native + "-vs": {Stdout: "4.5\n", ExitCode: 0}, // verbatim, != regex "4.5.6"
		fallback:       {Stdout: "FallbackApp 9.8.7.6\n", ExitCode: 0},
		empty:          {Stdout: "EmptyApp 3.2.1.0\n", ExitCode: 0},
		empty + "-vs":  {Stdout: "", ExitCode: 0}, // prints nothing -> fall back
	}}

	var jstdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &jstdout, Stderr: &bytes.Buffer{}, Runner: runner}
	if code := app.Run(context.Background(), []string{"--config", global, "--json", "apps"}); code != exitSuccess {
		t.Fatalf("apps --json exit = %d", code)
	}
	js := jstdout.String()
	// nativeapp: trusts the command output (4.5), not the regex (4.5.6).
	if !strings.Contains(js, `"version_short":"4.5"`) {
		t.Errorf("nativeapp should use version_short command output 4.5:\n%s", js)
	}
	if strings.Contains(js, `"version_short":"4.5.6"`) {
		t.Errorf("nativeapp must not fall back to regex when version_short command is set:\n%s", js)
	}
	// fallbackapp: regex on "9.8.7.6" keeps at most the patchlevel.
	if !strings.Contains(js, `"version_short":"9.8.7"`) {
		t.Errorf("fallbackapp should parse short version 9.8.7:\n%s", js)
	}
	// emptyapp: empty command output falls back to regex on "3.2.1.0".
	if !strings.Contains(js, `"version_short":"3.2.1"`) {
		t.Errorf("emptyapp should fall back to regex 3.2.1:\n%s", js)
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

	daemonsDir := filepath.Join(root, "daemons")
	appsDir := filepath.Join(daemonsDir, "apps") // category derived from the directory
	enabledDir := filepath.Join(root, "enabled")
	for _, d := range []string{appsDir, enabledDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeDaemon := func(file, name, display, binary string) {
		body := fmt.Sprintf(`kind: daemon
name: %s
display_name: %q
service: { name: %s }
binary: %q
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}","--version"], timeout: 10s }
`, name, display, name, binary)
		if err := os.WriteFile(filepath.Join(appsDir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeDaemon("good.yml", "goodapp", "GoodApp", good)
	writeDaemon("bad.yml", "badapp", "BadApp", bad)
	writeDaemon("gone.yml", "goneapp", "GoneApp", missing)

	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths: { catalog: [ %s ], includes: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, daemonsDir, enabledDir)), 0o644); err != nil {
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

	// Default: only installed apps, the short version, and status.
	out := run("apps")
	if !strings.Contains(out, "GoodApp") || !strings.Contains(out, "1.2.3") || !strings.Contains(out, "ok") {
		t.Errorf("apps missing good app row:\n%s", out)
	}
	if strings.Contains(out, "GoodApp 1.2.3") {
		t.Errorf("apps should show the short version by default, not the raw string:\n%s", out)
	}
	if !strings.Contains(out, "BadApp") || !strings.Contains(out, "exit 3 (want 0): boom") {
		t.Errorf("apps missing bad app error:\n%s", out)
	}
	if strings.Contains(out, "GoneApp") {
		t.Errorf("apps should hide not-installed app by default:\n%s", out)
	}

	// `apps --long` shows the full raw version string instead.
	outLong := run("apps", "--long")
	if !strings.Contains(outLong, "GoodApp 1.2.3") {
		t.Errorf("apps --long should show the full version string:\n%s", outLong)
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
	if !strings.Contains(js, `"version":"GoodApp 1.2.3"`) || !strings.Contains(js, `"version_short":"1.2.3"`) || !strings.Contains(js, `"permissions":"-rwxr-xr-x (0755)"`) || !strings.Contains(js, `"installed":true`) || !strings.Contains(js, `"ok":false`) {
		t.Errorf("apps --json unexpected:\n%s", js)
	}
}
