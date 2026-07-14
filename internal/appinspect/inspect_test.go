package appinspect

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/process"
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

type countingRunner struct {
	byCommand map[string]execx.Result
	calls     map[string]int
}

func (r *countingRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	key := commandKey(name, args...)
	r.calls[key]++
	if res, ok := r.byCommand[key]; ok {
		return res, nil
	}
	return execx.Result{ExitCode: 127, Stderr: "not found"}, fmt.Errorf("%s: not found", name)
}

// tree builds a resolved service tree around one binary path and optional
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

	if r := inspect(t, runner, tree("", nil)); r.Installed || r.Status != StatusNoBinaryConfigured {
		t.Errorf("no binary: %+v", r)
	}
	if r := inspect(t, runner, tree("/nonexistent/tool", nil)); r.Installed || r.Status != StatusNotInstalled {
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
	if !r.Installed || !r.OK || r.Status != StatusOK || !strings.Contains(r.Permissions, "0755") {
		t.Errorf("plain executable: %+v", r)
	}
}

func TestInspectUsesConfiguredUserLookupForOwners(t *testing.T) {
	path := writeBinary(t, 0o755)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("stat did not return syscall.Stat_t")
	}
	runner := fakeRunner{byCommand: map[string]execx.Result{
		commandKey("getent", "passwd", strconv.FormatUint(uint64(st.Uid), 10)): {Stdout: "ldap-owner:x:4242:4243::/home/ldap-owner:/bin/bash\n"},
		commandKey("getent", "group", strconv.FormatUint(uint64(st.Gid), 10)):  {Stdout: "ldap-group:x:4243:ldap-owner\n"},
	}}
	lookup := process.NewUserLookup(process.UserLookupConfig{
		Mode:   process.UserLookupGetent,
		Runner: runner,
	})

	report := Inspect(context.Background(), fakeRunner{}, "app", config.Resolved{Name: "app", Tree: tree(path, nil)}, WithUserLookup(lookup))
	if report.User != "ldap-owner" || report.Group != "ldap-group" {
		t.Fatalf("owner = %q/%q, want ldap-owner/ldap-group", report.User, report.Group)
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
		maps.Copy(v, extra)
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

	expectedExitList := inspect(t, fakeRunner{byPath: map[string]execx.Result{
		bin: {Stdout: "Tool 3.1\n", ExitCode: 1},
	}}, tree(bin, version(map[string]any{"expect_exit": []any{0, 1}})))
	if !expectedExitList.OK || expectedExitList.Version != "Tool 3.1" {
		t.Errorf("expect_exit list: %+v", expectedExitList)
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

	optionalFailure := inspect(t, fakeRunner{byPath: map[string]execx.Result{
		bin: {Stderr: "wrapper requires a login shell\n", ExitCode: 126},
	}}, tree(bin, version(map[string]any{"optional": true})))
	if !optionalFailure.OK || optionalFailure.Status != StatusOK || optionalFailure.Version != "" {
		t.Errorf("optional version failure should not fail the app: %+v", optionalFailure)
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
	if !r.OK || r.Status != StatusOK {
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
		"catalog/apps/present.yml": "name: present\nvariables:\n  binary: " + installed + "\n",
		"catalog/apps/absent.yml":  "name: absent\nvariables:\n  binary: /nonexistent/absent\n",
		"services/.keep":           "",
		"sermo.yml": "engine: { backend: systemd }\n" +
			"paths:\n  services: [" + filepath.Join(root, "services") + "]\n  runtime: /run/sermo\n" +
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
	cfg, err := config.Load(filepath.Join(root, "sermo.yml"), config.WithCatalogDirs(filepath.Join(root, "catalog")))
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

func TestListVersionMatchDistinguishesMySQLAndMariaDB(t *testing.T) {
	tests := []struct {
		name         string
		versionByBin map[string]string
		wantApps     string
		wantServices string
		wantRejected string
	}{
		{
			name:         "old MariaDB only has mysqld",
			versionByBin: map[string]string{"mysqld": "/usr/sbin/mysqld Ver 10.11.11-MariaDB for Linux\n"},
			wantApps:     "mariadb",
			wantServices: "mariadb",
			wantRejected: "mysql",
		},
		{
			name:         "new MariaDB has mariadbd and compatibility mysqld",
			versionByBin: map[string]string{"mariadbd": "/usr/sbin/mariadbd Ver 11.8.5-MariaDB for Linux\n", "mysqld": "/usr/sbin/mysqld Ver 11.8.5-MariaDB for Linux\n"},
			wantApps:     "mariadb",
			wantServices: "mariadb",
			wantRejected: "mysql",
		},
		{
			name:         "Oracle MySQL mysqld is not MariaDB",
			versionByBin: map[string]string{"mysqld": "/usr/sbin/mysqld  Ver 8.4.6 for Linux on x86_64 (MySQL Community Server - GPL)\n"},
			wantApps:     "mysql",
			wantServices: "mysql",
			wantRejected: "mariadb",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			binDir := filepath.Join(root, "bin")
			catalogDir := filepath.Join(root, "catalog")
			servicesDir := filepath.Join(root, "services")
			for _, dir := range []string{binDir, filepath.Join(catalogDir, "apps"), filepath.Join(catalogDir, "services"), servicesDir} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			pathFor := func(name string) string { return filepath.Join(binDir, name) }
			for name := range tc.versionByBin {
				if err := os.WriteFile(pathFor(name), []byte("x"), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			files := map[string]string{
				"catalog/apps/mariadb.yml": fmt.Sprintf(`
name: mariadb
display_name: "MariaDB"
version_match: { contains: MariaDB }
variables:
  binary: [%q, %q]
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "--version"] }
`, pathFor("mariadbd"), pathFor("mysqld")),
				"catalog/apps/mysql.yml": fmt.Sprintf(`
name: mysql
display_name: "MySQL"
version_match: { excludes: MariaDB }
variables:
  binary: %q
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "--version"] }
`, pathFor("mysqld")),
				"catalog/services/mariadb.yml": `
name: mariadb
display_name: "MariaDB"
service: { systemd: [mariadb] }
apps: [mariadb]
`,
				"catalog/services/mysql.yml": `
name: mysql
display_name: "MySQL"
service: { systemd: [mysql] }
apps: [mysql]
`,
				"sermo.yml": "engine: { backend: systemd }\n" +
					"paths:\n  services: [" + servicesDir + "]\n  runtime: /run/sermo\n" +
					"defaults:\n  policy: { cooldown: 5m }\n",
			}
			for rel, content := range files {
				path := filepath.Join(root, rel)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := config.Load(filepath.Join(root, "sermo.yml"), config.WithCatalogDirs(catalogDir))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}

			runner := fakeRunner{byCommand: map[string]execx.Result{}}
			for name, output := range tc.versionByBin {
				runner.byCommand[commandKey(pathFor(name), "--version")] = execx.Result{Stdout: output, ExitCode: 0}
			}

			apps := List(context.Background(), runner, cfg, config.CategoryApp, false)
			if got := strings.Join(reportNames(apps), ","); got != tc.wantApps {
				t.Fatalf("apps = %s, want %s; reports=%+v", got, tc.wantApps, apps)
			}
			services := List(context.Background(), runner, cfg, config.CategoryService, false, WithOptionalVersion())
			if got := strings.Join(reportNames(services), ","); got != tc.wantServices {
				t.Fatalf("services = %s, want %s; reports=%+v", got, tc.wantServices, services)
			}

			allApps := reportsByName(List(context.Background(), runner, cfg, config.CategoryApp, true))
			rejected := allApps[tc.wantRejected]
			if rejected.Installed || !strings.HasPrefix(rejected.Status, "not installed: version ") {
				t.Fatalf("rejected app %q report = %+v, want version identity rejection", tc.wantRejected, rejected)
			}
			if tc.name == "new MariaDB has mariadbd and compatibility mysqld" {
				if got, want := allApps["mariadb"].Binary, pathFor("mariadbd"); got != want {
					t.Fatalf("new MariaDB binary = %q, want official %q", got, want)
				}
			}
		})
	}
}

func reportNames(reports []Report) []string {
	names := make([]string, 0, len(reports))
	for _, report := range reports {
		names = append(names, report.Name)
	}
	return names
}

func reportsByName(reports []Report) map[string]Report {
	out := make(map[string]Report, len(reports))
	for _, report := range reports {
		out[report.Name] = report
	}
	return out
}

func TestListAppVersionFromProvider(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string) string {
		t.Helper()
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/true\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	rpcbind := write("rpcbind")
	nfsdcld := write("nfsdcld")
	rpcmountd := write("rpc.mountd")
	local := write("local")

	for dir, content := range map[string]string{
		"catalog/apps/rpcbind.yml": fmt.Sprintf(`
name: rpcbind
variables:
  binary: %q
version_from: rpc-mountd
`, rpcbind),
		"catalog/apps/nfsdcld.yml": fmt.Sprintf(`
name: nfsdcld
variables:
  binary: %q
version_from: rpc-mountd
`, nfsdcld),
		"catalog/apps/rpc-mountd.yml": fmt.Sprintf(`
name: rpc-mountd
variables:
  binary: %q
preflight:
  version: { type: command, command: [%q, "-v"] }
`, rpcmountd, rpcmountd),
		"catalog/apps/local.yml": fmt.Sprintf(`
name: local
variables:
  binary: %q
version_from: rpc-mountd
preflight:
  version: { type: command, command: [%q, "--version"] }
`, local, local),
		"services/.keep": "",
		"sermo.yml": "engine: { backend: systemd }\n" +
			"paths:\n  services: [" + filepath.Join(root, "services") + "]\n  runtime: /run/sermo\n" +
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
	cfg, err := config.Load(filepath.Join(root, "sermo.yml"), config.WithCatalogDirs(filepath.Join(root, "catalog")))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	runner := &countingRunner{
		byCommand: map[string]execx.Result{
			commandKey(rpcmountd, "-v"):    {Stdout: "rpc.mountd version 2.8.5\n", ExitCode: 0},
			commandKey(local, "--version"): {Stdout: "Local 9.1.0\n", ExitCode: 0},
		},
		calls: map[string]int{},
	}

	reports := List(context.Background(), runner, cfg, config.CategoryApp, false)
	byName := map[string]Report{}
	for _, report := range reports {
		byName[report.Name] = report
	}
	for _, name := range []string{"rpcbind", "nfsdcld"} {
		report := byName[name]
		if report.Version != "rpc.mountd version 2.8.5" || report.VersionShort != "2.8.5" || report.VersionSource != "rpc-mountd" {
			t.Fatalf("%s inherited version = %+v, want rpc-mountd 2.8.5", name, report)
		}
	}
	if report := byName["local"]; report.Version != "Local 9.1.0" || report.VersionSource != "" {
		t.Fatalf("local version should win over version_from: %+v", report)
	}
	if got := runner.calls[commandKey(rpcmountd, "-v")]; got != 1 {
		t.Fatalf("provider version command ran %d times, want 1", got)
	}
}

func TestListIncludesUnversionedTemplateApp(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"php", "php8.4"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for dir, content := range map[string]string{
		"catalog/apps/php.yml": fmt.Sprintf(`
name: php%%v
display_name: "PHP ${version}"
variables:
  binary: %q
preflight:
  binary: { type: binary, path: "${binary}" }
`, filepath.Join(binDir, "php${version}")),
		"services/.keep": "",
		"sermo.yml": "engine: { backend: systemd }\n" +
			"paths:\n  services: [" + filepath.Join(root, "services") + "]\n  runtime: /run/sermo\n" +
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
	cfg, err := config.Load(filepath.Join(root, "sermo.yml"), config.WithCatalogDirs(filepath.Join(root, "catalog")))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	reports := List(context.Background(), fakeRunner{}, cfg, config.CategoryApp, false)
	var names []string
	for _, report := range reports {
		names = append(names, report.Name)
		if !report.Installed || !report.OK {
			t.Fatalf("report %+v should be installed and ok", report)
		}
	}
	if strings.Join(names, ",") != "php,php8.4" {
		t.Fatalf("listed apps = %v, want php,php8.4", names)
	}
}
