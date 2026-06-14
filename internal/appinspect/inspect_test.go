package appinspect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sermo/internal/config"
	"sermo/internal/execx"
)

// fakeRunner returns a canned result per command path; commands without an
// entry fail like a missing binary would.
type fakeRunner struct {
	byPath    map[string]execx.Result
	byCommand map[string]execx.Result
	err       error
}

func (f fakeRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	if f.byCommand != nil {
		if res, ok := f.byCommand[commandKey(name, args...)]; ok {
			return res, f.err
		}
	}
	res, ok := f.byPath[name]
	if !ok {
		return execx.Result{ExitCode: 127, Stderr: "not found"}, f.err
	}
	return res, f.err
}

func commandKey(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), "\x00")
}

// tree builds a resolved daemon tree around one binary path and optional
// version-command entry.
func tree(binary string, version map[string]any) map[string]any {
	t := map[string]any{"variables": map[string]any{"binary": binary}}
	if version != nil {
		t["commands"] = map[string]any{"version": version}
	}
	return t
}

func inspect(t *testing.T, runner execx.Runner, tr map[string]any) Report {
	t.Helper()
	return Inspect(context.Background(), runner, "app", config.Resolved{Name: "app", Tree: tr})
}

func writeBinary(t *testing.T, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(path, []byte("#!/bin/true\n"), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInspectBinaryStates(t *testing.T) {
	runner := fakeRunner{}

	if r := inspect(t, runner, tree("", nil)); r.Installed || r.Status != "no binary configured" {
		t.Errorf("no binary: %+v", r)
	}
	if r := inspect(t, runner, tree("/nonexistent/tool", nil)); r.Installed || r.Status != "not installed" {
		t.Errorf("missing binary: %+v", r)
	}
	if r := inspect(t, runner, tree(t.TempDir(), nil)); r.Installed || !strings.Contains(r.Status, "is a directory") {
		t.Errorf("directory binary: %+v", r)
	}
	if r := inspect(t, runner, tree(writeBinary(t, 0o644), nil)); !r.Installed || r.OK || !strings.Contains(r.Status, "not executable") || r.Permissions == "" {
		t.Errorf("non-executable binary: %+v", r)
	}
	// Executable with no version command: installed and ok without running anything.
	r := inspect(t, runner, tree(writeBinary(t, 0o755), nil))
	if !r.Installed || !r.OK || r.Status != "ok" || !strings.Contains(r.Permissions, "0755") {
		t.Errorf("plain executable: %+v", r)
	}
}

func TestInspectReportsCategory(t *testing.T) {
	bin := writeBinary(t, 0o755)
	tr := tree(bin, nil)
	tr["category"] = "database"
	r := inspect(t, fakeRunner{}, tr)
	if r.Category != "database" {
		t.Fatalf("category = %q, want database", r.Category)
	}
}

func TestInspectVersionCommandOutcomes(t *testing.T) {
	bin := writeBinary(t, 0o755)
	version := func(extra map[string]any) map[string]any {
		v := map[string]any{"command": []any{bin, "--version"}}
		for k, val := range extra {
			v[k] = val
		}
		return v
	}

	ok := inspect(t, fakeRunner{byPath: map[string]execx.Result{
		bin: {Stdout: "\nTool v2.8.4.1-extra\n", ExitCode: 0},
	}}, tree(bin, version(nil)))
	if !ok.OK || ok.Version != "Tool v2.8.4.1-extra" || ok.VersionShort != "2.8.4" {
		t.Errorf("success: %+v", ok)
	}

	// Tools that print their version to stderr (a classic) still report one.
	stderrVer := inspect(t, fakeRunner{byPath: map[string]execx.Result{
		bin: {Stderr: "Tool 1.2\n", ExitCode: 0},
	}}, tree(bin, version(nil)))
	if !stderrVer.OK || stderrVer.Version != "Tool 1.2" {
		t.Errorf("stderr version: %+v", stderrVer)
	}

	wrongExit := inspect(t, fakeRunner{byPath: map[string]execx.Result{
		bin: {Stderr: "boom\n", ExitCode: 2},
	}}, tree(bin, version(nil)))
	if wrongExit.OK || !strings.Contains(wrongExit.Status, "exit 2 (want 0)") || !strings.Contains(wrongExit.Status, "boom") {
		t.Errorf("wrong exit: %+v", wrongExit)
	}

	expectedExit := inspect(t, fakeRunner{byPath: map[string]execx.Result{
		bin: {Stdout: "Tool 3.0\n", ExitCode: 3},
	}}, tree(bin, version(map[string]any{"expect_exit": 3})))
	if !expectedExit.OK || expectedExit.Version != "Tool 3.0" {
		t.Errorf("expect_exit: %+v", expectedExit)
	}

	mosquittoHelp := inspect(t, fakeRunner{byPath: map[string]execx.Result{
		bin: {Stdout: "\nmosquitto version 2.0.22\n\nmosquitto is an MQTT broker\n", ExitCode: 1},
	}}, tree(bin, version(map[string]any{"expect_exit": 1})))
	if !mosquittoHelp.OK || mosquittoHelp.Version != "mosquitto version 2.0.22" || mosquittoHelp.VersionShort != "2.0.22" {
		t.Errorf("mosquitto-style help version: %+v", mosquittoHelp)
	}

	badStdout := inspect(t, fakeRunner{byPath: map[string]execx.Result{
		bin: {Stdout: "garbage\n", ExitCode: 0},
	}}, tree(bin, version(map[string]any{"expect_stdout": "Tool"})))
	if badStdout.OK || !strings.Contains(badStdout.Status, "stdout") {
		t.Errorf("stdout mismatch: %+v", badStdout)
	}

	failed := inspect(t, fakeRunner{
		byPath: map[string]execx.Result{bin: {ExitCode: 0}},
		err:    errors.New("exec format error"),
	}, tree(bin, version(nil)))
	if failed.OK || !strings.Contains(failed.Status, "exec format error") {
		t.Errorf("runner error: %+v", failed)
	}
}

func TestInspectHealthCommandTakesPriority(t *testing.T) {
	bin := writeBinary(t, 0o755)
	version := map[string]any{"command": []any{bin, "--version"}}
	health := map[string]any{
		"type":          "command",
		"command":       []any{bin, "-h"},
		"expect_stdout": "this matcher is ignored for health",
	}
	tr := tree(bin, version)
	tr["preflight"] = map[string]any{"health": health}

	r := inspect(t, fakeRunner{byCommand: map[string]execx.Result{
		commandKey(bin, "-h"):        {Stdout: "usage without a version\n", Stderr: "noise\n", ExitCode: 0},
		commandKey(bin, "--version"): {Stderr: "not supported\n", ExitCode: 2},
	}}, tr)
	if !r.OK || r.Status != "ok" {
		t.Fatalf("health success should make app ok regardless of output/version failure: %+v", r)
	}
	if r.Version != "" || r.VersionShort != "" {
		t.Fatalf("failing version command should not populate version when health succeeds: %+v", r)
	}

	r = inspect(t, fakeRunner{byCommand: map[string]execx.Result{
		commandKey(bin, "-h"):        {Stdout: "usage without a version\n", Stderr: "health details\n", ExitCode: 1},
		commandKey(bin, "--version"): {Stdout: "Tool 9.9.9\n", ExitCode: 0},
	}}, tr)
	if r.OK || !strings.Contains(r.Status, "exit 1 (want 0)") {
		t.Fatalf("health failure should take priority over version success: %+v", r)
	}
	if strings.Contains(r.Status, "health details") || r.Version != "" {
		t.Fatalf("health must ignore command output and not run version after failure: %+v", r)
	}
}

func TestListFiltersMissingBinaries(t *testing.T) {
	root := t.TempDir()
	installed := writeBinary(t, 0o755)
	for dir, content := range map[string]string{
		"catalog/apps/present.yml": "kind: app\nname: present\nvariables:\n  binary: " + installed + "\n",
		"catalog/apps/absent.yml":  "kind: app\nname: absent\nvariables:\n  binary: /nonexistent/absent\n",
		"enabled/.keep":            "",
		"sermo.yml": "engine: { backend: systemd }\n" +
			"paths:\n  catalog: [" + filepath.Join(root, "catalog") + "]\n  includes: [" + filepath.Join(root, "enabled") + "]\n  runtime: /run/sermo\n" +
			"defaults:\n  policy: { cooldown: 5m }\n",
	} {
		path := filepath.Join(root, dir)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(filepath.Join(root, "sermo.yml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	only := List(context.Background(), fakeRunner{}, cfg, config.CategoryApp, false)
	if len(only) != 1 || only[0].Name != "present" {
		t.Fatalf("installed-only = %+v, want [present]", only)
	}
	all := List(context.Background(), fakeRunner{}, cfg, config.CategoryApp, true)
	if len(all) != 2 {
		t.Fatalf("includeMissing = %+v, want 2 reports", all)
	}
	if List(context.Background(), fakeRunner{}, nil, config.CategoryApp, true) != nil {
		t.Fatal("nil config must yield nil")
	}
}
