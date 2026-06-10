package app

import (
	"testing"
	"time"

	"sermo/internal/execx"
	"sermo/internal/notify"
)

func monitorTestDeps() Deps {
	return Deps{
		Notifiers:   map[string]notify.Notifier{"ops": &fakeNotifier{name: "ops"}},
		ExecxRunner: execx.CommandRunner{},
		Now:         time.Now,
		Emit:        func(Event) {},
	}
}

func TestVersionMonitor(t *testing.T) {
	tree := map[string]any{
		"commands": map[string]any{"version": map[string]any{"command": []any{"apachectl", "-v"}}},
		"version":  map[string]any{"on_change": map[string]any{"notify": []any{"ops"}}},
	}
	w, warn := versionMonitor("apache", tree, monitorTestDeps(), time.Minute)
	if warn != "" || w == nil {
		t.Fatalf("warn=%q w=%v", warn, w)
	}
	if w.Name != "apache:version" || w.CheckType != "command" {
		t.Errorf("watch = %+v", w)
	}
	if len(w.Notifiers) != 1 {
		t.Errorf("notifiers = %v (want ops)", w.Notifiers)
	}

	// version.on_change but no version command in the profile -> warning.
	noCmd := map[string]any{"version": map[string]any{"on_change": map[string]any{}}}
	if _, warn := versionMonitor("x", noCmd, monitorTestDeps(), time.Minute); warn == "" {
		t.Error("a missing version command should warn")
	}

	// No version block -> no watch, no warning.
	if w, warn := versionMonitor("x", map[string]any{}, monitorTestDeps(), time.Minute); w != nil || warn != "" {
		t.Errorf("no version block should yield nil/no-warn, got %v/%q", w, warn)
	}
}

func TestConfigMonitor(t *testing.T) {
	tree := map[string]any{
		"preflight": map[string]any{"config": map[string]any{"type": "command", "command": []any{"apachectl", "configtest"}}},
		"config":    map[string]any{"on_change": map[string]any{"notify": []any{"ops"}}, "path": []any{"/etc/apache2/apache2.conf"}},
	}
	w, warn := configMonitor("apache", tree, monitorTestDeps(), time.Minute)
	if warn != "" || w == nil {
		t.Fatalf("warn=%q w=%v", warn, w)
	}
	if w.Name != "apache:config" || w.CheckType != "config" {
		t.Errorf("watch = %+v", w)
	}

	// config.on_change but neither preflight.config nor a path -> warning.
	bare := map[string]any{"config": map[string]any{"on_change": map[string]any{"notify": []any{"ops"}}}}
	if _, warn := configMonitor("x", bare, monitorTestDeps(), time.Minute); warn == "" {
		t.Error("config monitor with no command/path should warn")
	}

	// path-only (no config test command) is allowed.
	pathOnly := map[string]any{"config": map[string]any{"on_change": map[string]any{}, "path": []any{"/etc/x.conf"}}}
	if w, warn := configMonitor("x", pathOnly, monitorTestDeps(), time.Minute); warn != "" || w == nil {
		t.Errorf("path-only config monitor should build: %q", warn)
	}
}
