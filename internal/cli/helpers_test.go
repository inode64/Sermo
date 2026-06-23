package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sermo/internal/config"
)

func TestIssueHelpers(t *testing.T) {
	scoped := scopedIssues("svc", []string{"a", "b"})
	if len(scoped) != 2 || scoped[0].Scope != "svc" || scoped[1].Msg != "b" {
		t.Fatalf("scopedIssues = %+v", scoped)
	}

	issues := []config.Issue{
		{Scope: "svc", Msg: "one"},
		{Scope: "svc", Msg: "three"},
	}
	asJSON := issuesJSON(issues)
	if len(asJSON) != 2 || asJSON[0]["scope"] != "svc" || asJSON[0]["error"] != "one" {
		t.Fatalf("issuesJSON = %+v", asJSON)
	}
}

func TestNotifierNamesSorted(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"notifiers": map[string]any{
			"zeta": map[string]any{"type": "slack"},
			"alfa": map[string]any{"type": "email"},
		},
	}}}
	got := notifierNames(cfg)
	if len(got) != 4 || got[0] != "alfa" || got[1] != "tty" || got[2] != "wall" || got[3] != "zeta" {
		t.Fatalf("notifierNames = %v, want sorted [alfa tty wall zeta]", got)
	}
	if got := notifierNames(&config.Config{}); len(got) != 2 || got[0] != "tty" || got[1] != "wall" {
		t.Fatalf("builtin notifiers = %v, want [tty wall]", got)
	}
}

func TestWizardDaemonHelpers(t *testing.T) {
	tree := map[string]any{
		"display_name": "MariaDB",
		"variables":    map[string]any{"port": 3306},
	}
	if got := daemonTitle(tree, "mariadb"); got != "MariaDB" {
		t.Fatalf("daemonTitle = %q", got)
	}
	if got := daemonTitle(map[string]any{}, "mariadb"); got != "mariadb" {
		t.Fatalf("daemonTitle fallback = %q", got)
	}
	if got := daemonPort(tree); got != 3306 {
		t.Fatalf("daemonPort = %d", got)
	}
	if got := daemonPort(map[string]any{}); got != 0 {
		t.Fatalf("daemonPort without variables = %d", got)
	}
	for _, port := range []any{0, -1, 65536, "70000", "not-a-port"} {
		if got := daemonPort(map[string]any{"variables": map[string]any{"port": port}}); got != 0 {
			t.Fatalf("daemonPort(%v) = %d, want 0", port, got)
		}
	}

	set := serviceNameSet(&config.Config{ServiceNames: []string{"a", "b"}})
	if _, ok := set["a"]; !ok || len(set) != 2 {
		t.Fatalf("serviceNameSet = %v", set)
	}

	present := filepath.Join(t.TempDir(), "my.cnf")
	if err := os.WriteFile(present, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !pathExists(present) || pathExists(present+".missing") {
		t.Fatal("pathExists misreports")
	}
	got := existingConfigFiles(map[string]any{"config_files": []any{present, "/nonexistent/none.cnf"}})
	if len(got) != 1 || got[0] != present {
		t.Fatalf("existingConfigFiles = %v", got)
	}
}

func TestWriteServiceFiles(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	if err := os.WriteFile(global, []byte("paths:\n  services: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	docs := map[string]map[string]any{
		"web-main": {"kind": "service", "name": "web-main", "uses": "nginx"},
	}

	target, n, err := writeServiceFiles(global, docs)
	if err != nil || n != 1 {
		t.Fatalf("writeServiceFiles = %q, %d, %v", target, n, err)
	}
	if target != filepath.Join(dir, servicesIncludeDir) {
		t.Fatalf("target dir = %q", target)
	}
	if servicesIncludeDir != "services" {
		t.Fatalf("service wizard must write new service files under services/, got %q", servicesIncludeDir)
	}
	written, err := os.ReadFile(filepath.Join(target, "web-main.yml"))
	if err != nil || !strings.Contains(string(written), "uses: nginx") {
		t.Fatalf("service file = %q, err %v", written, err)
	}
	// The services list was extended, with the original config backed up first.
	updated, err := os.ReadFile(global)
	if err != nil || !strings.Contains(string(updated), "services:") || !strings.Contains(string(updated), servicesIncludeDir) {
		t.Fatalf("global after write = %q, err %v", updated, err)
	}
	if _, err := os.Stat(global + ".bak"); err != nil {
		t.Fatalf("missing .bak backup: %v", err)
	}

	// Re-writing the same service must refuse to overwrite.
	if _, _, err := writeServiceFiles(global, docs); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("overwrite err = %v", err)
	}
}

func TestWriteServiceFilesPreservesAppsPath(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	if err := os.WriteFile(global, []byte("paths:\n  apps: [apps]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	docs := map[string]map[string]any{
		"ssh": {"kind": "service", "name": "ssh", "uses": "ssh"},
	}

	target, _, err := writeServiceFiles(global, docs)
	if err != nil {
		t.Fatalf("writeServiceFiles: %v", err)
	}
	if target != filepath.Join(dir, "services") {
		t.Fatalf("target dir = %q, want services", target)
	}
	if _, err := os.Stat(filepath.Join(dir, "apps")); !os.IsNotExist(err) {
		t.Fatalf("apps dir should not be created or removed, stat err=%v", err)
	}
	data, err := os.ReadFile(global)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, "apps:") || !strings.Contains(body, "services:") {
		t.Fatalf("paths.apps must be preserved while paths.services is added: %s", body)
	}
}
