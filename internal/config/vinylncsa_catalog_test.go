package config

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"sermo/internal/cfgval"
	"sermo/internal/process"
)

// Every vinylncsa instance runs the same binary as the same user, so each one
// must select only the process started with its own pidfile: a broader
// selector would let stopping one instance treat the other's logger as its
// residual.
func TestCatalogVinylncsaInstancesSelectTheirOwnProcess(t *testing.T) {
	root := repoRoot(t)
	dir := t.TempDir()
	enabled := filepath.Join(dir, "services")
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	docs := map[string]string{
		"vinylncsa":         "name: vinylncsa\nuses: vinylncsa\nmonitor: enabled\n",
		"vinylncsa.backend": "name: vinylncsa.backend\nuses: vinylncsa\nservice: vinylncsa.backend\nmonitor: enabled\n",
	}
	for name, body := range docs {
		if err := os.WriteFile(filepath.Join(enabled, name+".yml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := loadConfig(t, writeServicesGlobal(t, dir, enabled, "openrc"), WithCatalogDirs(repoCatalogDir(root)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("Validate issues = %v, want none", issues)
	}
	// The argv the OpenRC script starts each instance with.
	argv := func(name string) string {
		return "/usr/bin/vinylncsa -D -P /run/" + name + ".pid -a -w /var/log/vinyl-cache/" + name + ".log"
	}
	for name := range docs {
		resolved, errs := cfg.Resolve(name)
		if len(errs) != 0 {
			t.Fatalf("Resolve(%s): %v", name, errs)
		}
		if got, want := cfgval.String(resolved.Tree["pidfile"]), "/run/"+name+".pid"; got != want {
			t.Errorf("%s pidfile = %q, want %q", name, got, want)
		}
		main := nested(t, resolved.Tree, "processes", "main")
		if exe := cfgval.String(main[process.SelectorKeyExe]); filepath.Base(exe) != "vinylncsa" {
			t.Errorf("%s main exe = %q, want the vinylncsa binary", name, exe)
		}
		cmd, err := regexp.Compile(cfgval.String(main[process.SelectorKeyCmd]))
		if err != nil {
			t.Fatalf("%s main cmd: %v", name, err)
		}
		for other := range docs {
			if got, want := cmd.MatchString(argv(other)), other == name; got != want {
				t.Errorf("%s cmd %q matches %s's process = %v, want %v", name, cmd, other, got, want)
			}
		}
	}
}
