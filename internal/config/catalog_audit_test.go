package config

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/conn"
	"sermo/internal/process"
	"sermo/internal/rules"
)

// These audits load the real repo artifacts — the packaged catalog, the shipped
// sermo.yml and the examples — and require them to resolve and validate
// cleanly, so a catalog definition that no current service exercises (the way
// kafka's nested variables and rabbitmq's incomplete kill_only_if once shipped
// broken) cannot regress unnoticed.

// repoRoot returns the repository root, skipping the test when the catalog is
// not present (e.g. a vendored build of just this package).
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "catalog")); err != nil {
		t.Skipf("catalog dir not found: %v", err)
	}
	return root
}

func repoCatalogDir(root string) string {
	return filepath.Join(root, "catalog")
}

func readYAMLMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := yaml.Unmarshal(data, &body); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return body
}

// loadRepoCatalog loads the repository's real catalog under an empty-services
// global, failing on any load error.
func loadRepoCatalog(t *testing.T) *Config {
	t.Helper()
	root := repoRoot(t)
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  services: []\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(t, global, WithCatalogDirs(repoCatalogDir(root)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

// catalogSelectorsByRole resolves one catalog service and returns its process
// selectors keyed by role, the shape every assertion about process identity
// needs: which role a profile declares, and what it may or may not signal.
func catalogSelectorsByRole(t *testing.T, catalogService string) map[string]process.Selector {
	t.Helper()
	resolved := resolveCatalogService(t, catalogService, backendSystemd)
	selectors, warnings := process.ParseSelectors(resolved.Tree)
	if len(warnings) > 0 {
		t.Fatalf("ParseSelectors(%s) warnings = %v", catalogService, warnings)
	}
	byRole := make(map[string]process.Selector, len(selectors))
	for _, selector := range selectors {
		byRole[selector.Name] = selector
	}
	return byRole
}

// walkCatalogDocs walks dir and calls fn with each YAML document's path and
// decoded top-level map, skipping directories and non-YAML files.
func walkCatalogDocs(t *testing.T, dir string, fn func(path string, body map[string]any)) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !isYAML(entry.Name()) {
			return nil
		}
		fn(path, readYAMLMap(t, path))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func catalogDocByName(t *testing.T, root, category, name string) map[string]any {
	t.Helper()
	dir := filepath.Join(root, "catalog", category)
	var found map[string]any
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !isYAML(entry.Name()) {
			return nil
		}
		body := readYAMLMap(t, path)
		if cfgval.String(body["name"]) == name {
			found = body
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == nil {
		t.Fatalf("catalog %s document %q not found", category, name)
	}
	return found
}

func catalogWatchCheck(t *testing.T, body map[string]any, name string) map[string]any {
	t.Helper()
	return nested(t, body, "watches", name, "check")
}

func TestCatalogDBusAdvancedProbesStayReadOnlyAndCheckOnly(t *testing.T) {
	found := make(map[string]struct{})
	servicesDir := filepath.Join(repoRoot(t), "catalog", "services")
	walkCatalogDocs(t, servicesDir, func(path string, body map[string]any) {
		watches, _ := body["watches"].(map[string]any)
		for _, name := range slices.Sorted(maps.Keys(watches)) {
			watch, _ := watches[name].(map[string]any)
			check, _ := watch["check"].(map[string]any)
			if cfgval.String(check[checks.CheckKeyType]) != conn.ProtocolNameDBus {
				continue
			}
			probe := cfgval.String(check[checks.CheckKeyDBusProbe])
			switch probe {
			case "", conn.DBusProbePeer:
				continue
			case conn.DBusProbeIntrospect, conn.DBusProbeProperty:
				found[cfgval.String(body["name"])+"/"+name] = struct{}{}
			default:
				t.Errorf("%s D-Bus watch %s uses unsupported advanced probe %q", path, name, probe)
				continue
			}
			if _, present := watch[rules.RuleFieldThen]; present {
				t.Errorf("%s D-Bus watch %s must remain check-only", path, name)
			}
			if err := conn.ValidateDBusTarget(checks.DBusTargetFromEntry(check)); err != nil {
				t.Errorf("%s D-Bus watch %s target: %v", path, name, err)
			}
		}
	})
	required := []string{
		"accounts-daemon/version",
		"bluetooth/dbus",
		"bolt/dbus",
		"colord/version",
		"firewalld/dbus",
		"gdm/version",
		"iio-sensor-proxy/dbus",
		"networkmanager/dbus",
		"systemd-machined/dbus",
		"systemd-networkd/dbus",
		"systemd-resolved/dbus",
		"systemd/manager",
		"tuned-ppd/active-profile",
		"tuned/dbus",
		"wpa-supplicant/dbus",
	}
	for _, id := range required {
		if _, present := found[id]; !present {
			t.Errorf("catalog advanced D-Bus watch %s is missing or no longer advanced", id)
		}
	}
}

func TestCatalogServicesDoNotDeclareVersionsFrom(t *testing.T) {
	walkCatalogDocs(t, filepath.Join(repoRoot(t), "catalog", "services"), func(path string, body map[string]any) {
		versions, _ := body["versions"].(map[string]any)
		if _, ok := versions["from"]; ok {
			t.Fatalf("%s declares versions.from; catalog/services must discover service templates from service:", path)
		}
	})
}

// TestCatalogServicesNoArtifactCheckCollision guards, host-independently, against
// a service that declares both a top-level artifact (pidfile/socket/lockfile, or
// a pidfiles role) — each of which resolution turns into an auto-generated check
// of that name — and a watches.<same-name> entry, which resolution promotes to
// the same check name and then rejects with "would overwrite existing check".
//
// TestRealCatalogAllServicesValidate only materializes version templates for the
// runtimes installed on the test host, so it missed exactly this collision in the
// php-fpm%v%s%i template on hosts without PHP. This static scan catches it on
// every host and CI run.
func TestCatalogServicesNoArtifactCheckCollision(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "catalog", "services")
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !isYAML(entry.Name()) {
			return nil
		}
		body := readYAMLMap(t, path)
		watches, _ := body["watches"].(map[string]any)
		if len(watches) == 0 {
			return nil
		}
		reserved := map[string]struct{}{}
		for _, key := range []string{artifactPidfile, artifactSocket, artifactLockfile} {
			if _, ok := body[key]; ok {
				reserved[key] = struct{}{}
			}
		}
		if pidfiles, ok := body["pidfiles"].(map[string]any); ok {
			for role := range pidfiles {
				reserved[role] = struct{}{}
			}
		}
		for name := range watches {
			if _, clash := reserved[name]; clash {
				t.Errorf("%s: watches.%s collides with the auto-generated %q check from the top-level artifact; rename the watch or drop the redundant one", filepath.Base(path), name, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestRealCatalogAllServicesValidate enables every instantiable catalog service
// as a service and validates the whole set. Version templates (%v/%n/%i) cannot
// be materialized off-host, so only the concrete service names are exercised.
func TestRealCatalogAllServicesValidate(t *testing.T) {
	root := repoRoot(t)
	for _, backend := range []string{"systemd", "openrc"} {
		t.Run(backend, func(t *testing.T) {
			validateAllCatalogServices(t, repoCatalogDir(root), backend)
		})
	}
}

func validateAllCatalogServices(t *testing.T, catalogDir, backend string) {
	t.Helper()
	for _, issue := range Validate(loadAllCatalogServices(t, catalogDir, backend)) {
		t.Errorf("catalog service fails validation: %s", issue)
	}
}

// loadAllCatalogServices enables every instantiable catalog profile for backend
// and returns the loaded config, so audits can inspect resolved trees instead of
// each re-deriving the same fixture.
func loadAllCatalogServices(t *testing.T, catalogDir, backend string) *Config {
	t.Helper()
	probeDir := t.TempDir()
	emptyEnabled := filepath.Join(probeDir, "services")
	if err := os.MkdirAll(emptyEnabled, 0o755); err != nil {
		t.Fatal(err)
	}
	probe, err := Load(writeServicesGlobal(t, probeDir, emptyEnabled, backend), WithCatalogDirs(catalogDir))
	if err != nil {
		t.Fatalf("Load (probe): %v", err)
	}
	dir := t.TempDir()
	enabled := filepath.Join(dir, "services")
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAllCatalogAuditServices(t, enabled, probe.CatalogServiceNames)
	cfg, err := Load(writeServicesGlobal(t, dir, enabled, backend), WithCatalogDirs(catalogDir))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

// writeServicesGlobal writes a minimal sermo.yml with the given backend and
// enabled services directory; shared by the catalog audit and reload tests.
func writeServicesGlobal(t *testing.T, dir, enabled, backend string) string {
	t.Helper()
	global := filepath.Join(dir, "sermo.yml")
	body := "engine: { backend: " + backend + " }\n" +
		"paths:\n  services: [" + enabled + "]\n  runtime: /run/sermo\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return global
}

func writeAllCatalogAuditServices(t *testing.T, enabled string, names []string) {
	t.Helper()
	count := 0
	for _, name := range names {
		if strings.Contains(name, "%") {
			continue
		}
		body := "name: " + name + "-audit\nuses: " + name + "\n"
		if err := os.WriteFile(filepath.Join(enabled, name+".yml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count == 0 {
		t.Fatal("no instantiable catalog services found")
	}
}

func TestApacheCatalogRestartsOnHotWorkerThread(t *testing.T) {
	root := repoRoot(t)
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  services: []\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, global, WithCatalogDirs(repoCatalogDir(root)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	resolved, errs := cfg.ResolveCatalog(CategoryService, "apache")
	if len(errs) > 0 {
		t.Fatalf("ResolveCatalog(apache): %v", errs)
	}
	// The unified watch desugars into a remediation rule referencing an embedded
	// optional cpu_thread metric check (a non-optional metric check would mark the
	// service unavailable when breached).
	rule := nested(t, resolved.Tree, "rules", "restart-if-worker-thread-hot")
	if got := cfgval.String(rule["type"]); got != "remediation" {
		t.Fatalf("rule type = %q, want remediation", got)
	}
	if got := cfgval.String(nested(t, rule, "if", "active")["check"]); got != "restart-if-worker-thread-hot" {
		t.Fatalf("rule if.active.check = %q, want restart-if-worker-thread-hot", got)
	}
	metric := nested(t, resolved.Tree, "checks", "restart-if-worker-thread-hot")
	if got := cfgval.String(metric["scope"]); got != "service" {
		t.Fatalf("metric scope = %q, want service", got)
	}
	if got := cfgval.String(metric["name"]); got != "cpu_thread" {
		t.Fatalf("metric name = %q, want cpu_thread", got)
	}
	if got := cfgval.String(metric["op"]); got != ">" {
		t.Fatalf("metric op = %q, want >", got)
	}
	if got := cfgval.String(metric["value"]); got != "90%" {
		t.Fatalf("metric value = %q, want 90%%", got)
	}
	if !cfgval.Bool(metric["optional"]) {
		t.Fatalf("embedded metric check must be optional to preserve SLA")
	}
	if got := cfgval.String(nested(t, rule, "for")["duration"]); got != "6m" {
		t.Fatalf("for.duration = %q, want 6m", got)
	}
	if got := cfgval.String(nested(t, rule, "then")["action"]); got != "restart" {
		t.Fatalf("then.action = %q, want restart", got)
	}
}

// glusterd, its bricks and its self-heal daemon are all the same executable
// running as root, so the profile has to separate them by cmdline: `main` is the
// only role Sermo may ever stop, and the workload roles are delegated so a stop
// neither counts nor signals them. The negative case carries as much weight as
// the positive one — a brick's cmdline contains `glusterd-uuid`, which a careless
// pattern would match, taking the node's storage down with the daemon.
func TestGlusterdCatalogSeparatesDaemonFromDelegatedWorkload(t *testing.T) {
	const (
		daemonCmdline = "/usr/sbin/glusterd -p /run/glusterd.pid --log-level INFO"
		brickCmdline  = "/usr/sbin/glusterfsd -s sirio --volfile-id images.sirio.srv-cluster-images " +
			"--xlator-option *-posix.glusterd-uuid=70d985fe-711c-49d3-a1dd-dcfec248e3dc " +
			"--process-name brick --brick-port 60759"
		selfHealCmdline = "/usr/sbin/glusterfs -s localhost --volfile-id shd/images " +
			"--process-name glustershd --client-pid=-6"
	)

	byRole := catalogSelectorsByRole(t, "glusterd")

	main, ok := byRole[process.RoleMain]
	if !ok {
		t.Fatalf("glusterd declares no %q process role", process.RoleMain)
	}
	if main.Delegated {
		t.Fatal("the management daemon must stay signallable; only its workload is delegated")
	}
	if main.Cmd == "" {
		t.Fatal("main must narrow by cmd: exe and user alone cannot separate glusterd from its workload")
	}
	mainCmd := regexp.MustCompile(main.Cmd)
	if !mainCmd.MatchString(daemonCmdline) {
		t.Fatalf("main cmd %q does not match the management daemon", main.Cmd)
	}

	for role, cmdline := range map[string]string{"brick": brickCmdline, "selfheal": selfHealCmdline} {
		if mainCmd.MatchString(cmdline) {
			t.Fatalf("main cmd %q also matches the %s cmdline; a stop would signal the workload", main.Cmd, role)
		}
		selector, ok := byRole[role]
		if !ok {
			t.Fatalf("glusterd declares no %q process role", role)
		}
		if !selector.Delegated {
			t.Fatalf("process role %q must be delegated so it is never signalled", role)
		}
		if !regexp.MustCompile(selector.Cmd).MatchString(cmdline) {
			t.Fatalf("process role %q cmd %q does not match its own cmdline", role, selector.Cmd)
		}
	}
}

// sshd's unit is KillMode=process, so a stop leaves every connected session
// alive. Those sessions are the service's own descendants, so unless the profile
// declares them delegated they count as residuals and a restart never reaches its
// start phase while anyone is logged in. The listener has to stay signallable
// though, and its process title shares the `sshd: ` prefix — so the pattern is
// only useful if it tells the two apart.
func TestSSHCatalogDelegatesSessionsButNotTheListener(t *testing.T) {
	const (
		listenerTitle = "sshd: /usr/sbin/sshd -D -e [listener] 0 of 10-100 startups"
		privsepTitle  = "sshd-session: fran [priv]"
		sessionTitle  = "sshd-session: fran@pts/0"
		legacyTitle   = "sshd: fran@pts/1"
	)

	byRole := catalogSelectorsByRole(t, "ssh")

	main, ok := byRole[process.RoleMain]
	if !ok {
		t.Fatalf("ssh declares no %q process role", process.RoleMain)
	}
	if main.Delegated {
		t.Fatal("the listener must stay signallable; only its sessions are delegated")
	}
	session, ok := byRole["session"]
	if !ok {
		t.Fatal("ssh declares no session process role")
	}
	if !session.Delegated {
		t.Fatal("the session role must be delegated so a stop never signals a connected user")
	}

	title := regexp.MustCompile(session.Cmd)
	if title.MatchString(listenerTitle) {
		t.Fatalf("session cmd %q matches the listener; a stop could then never clean it up", session.Cmd)
	}
	for _, connected := range []string{privsepTitle, sessionTitle, legacyTitle} {
		if !title.MatchString(connected) {
			t.Fatalf("session cmd %q does not match %q", session.Cmd, connected)
		}
	}
}

// TestAllCatalogServicesDesugarInPreview locks that the catalog-preview path
// (ResolveCatalog → resolveDocBody) runs the watch desugar like the daemon path,
// so no rule-class watch survives unexpanded and remediation stays wired for
// every catalog service reachable via the wizard/appinspect/web preview.
func TestAllCatalogServicesDesugarInPreview(t *testing.T) {
	cfg := loadRepoCatalog(t)
	for _, name := range cfg.CatalogServiceNames {
		if strings.Contains(name, "%") {
			continue // version templates materialize per instance elsewhere
		}
		resolved, errs := cfg.ResolveCatalog(CategoryService, name)
		if len(errs) > 0 {
			t.Errorf("ResolveCatalog(%s): %v", name, errs)
			continue
		}
		watches, _ := resolved.Tree["watches"].(map[string]any)
		for wn, raw := range watches {
			w, _ := raw.(map[string]any)
			then, _ := w["then"].(map[string]any)
			if then != nil && cfgval.String(then["action"]) != "" {
				t.Errorf("%s: watch %q keeps a rule-class action after preview resolve; resolveDocBody must run the desugar", name, wn)
			}
		}
	}
}

func TestContainerdCatalogRestartsOnVersionChange(t *testing.T) {
	root := repoRoot(t)
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  services: []\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, global, WithCatalogDirs(repoCatalogDir(root)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	resolved, errs := cfg.ResolveCatalog(CategoryService, "containerd")
	if len(errs) > 0 {
		t.Fatalf("ResolveCatalog(containerd): %v", errs)
	}
	rule := nested(t, resolved.Tree, "rules", "restart-on-change-containerd-version")
	if got := cfgval.String(rule["type"]); got != "remediation" {
		t.Fatalf("rule type = %q, want remediation", got)
	}
	changed := nested(t, rule, "if", "changed")
	if got := cfgval.String(changed["app"]); got != "containerd" {
		t.Fatalf("changed.app = %q, want containerd", got)
	}
	if got := cfgval.String(changed["level"]); got != "patch" {
		t.Fatalf("changed.level = %q, want patch", got)
	}
	mustHaveRestartOnChangeActions(t, nested(t, rule, "then"), "")
	// The named app's version command must be present in the resolved tree as
	// preflight["containerd-version"] (merged from the app), since that is what
	// the worker samples for the changed:{app} rule.
	versionCmd := cfgval.StringList(nested(t, resolved.Tree, "preflight", "containerd-version")["command"])
	if len(versionCmd) == 0 {
		t.Fatal("preflight[containerd-version] must carry a command for the changed:{app} rule")
	}
}

func TestCatalogServicesRestartOnLinkedAppVersionChanges(t *testing.T) {
	root := repoRoot(t)
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  services: []\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, global, WithCatalogDirs(repoCatalogDir(root)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	for name := range cfg.CatalogServices {
		t.Run(name, func(t *testing.T) {
			resolved, errs := cfg.ResolveCatalog(CategoryService, name)
			if len(errs) > 0 {
				t.Fatalf("ResolveCatalog(%s): %v", name, errs)
			}
			preflight, _ := resolved.Tree[sectionPreflight].(map[string]any)
			rulesMap, _ := resolved.Tree[rules.SectionRules].(map[string]any)
			for key := range preflight {
				app, ok := strings.CutSuffix(key, ServiceMonitorVersionCheckSuffix)
				if !ok || app == "" {
					continue
				}
				// Restarting on an app's version change is per-service opt-in
				// (restart_on_change.apps): a required linked app can simply be
				// non-restart-worthy, so a version preflight without a generated
				// rule is valid. When the rule exists it must be well-formed.
				ruleName := "restart-on-change-" + app + "-version"
				rule, ok := rulesMap[ruleName].(map[string]any)
				if !ok {
					continue
				}
				changed := nested(t, rule, "if", "changed")
				if got := cfgval.String(changed["app"]); got != app {
					t.Fatalf("%s changed.app = %q, want %q", ruleName, got, app)
				}
				mustHaveRestartOnChangeActions(t, nested(t, rule, "then"), "")
			}
		})
	}
}

// TestShippedGlobalConfigValidates validates the installed sample config as an
// installed config. It deliberately points at /etc/sermo target directories;
// source-tree examples are covered by TestRepoDevConfigLoadsExampleTree.
func TestShippedGlobalConfigValidates(t *testing.T) {
	root := repoRoot(t)

	cfg, err := Load(filepath.Join(root, "examples", "sermo.yml"),
		WithCatalogDirs(repoCatalogDir(root)),
		withPathDirs("services"),
		withPathDirs("apps"),
		withPathDirs("notifiers"),
		withPathDirs("watches"),
	)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Services) != 0 {
		t.Fatalf("installed sample config should not load repo service examples, got %d", len(cfg.Services))
	}
	for _, issue := range Validate(cfg) {
		t.Errorf("shipped sermo.yml fails validation: %s", issue)
	}
}

func TestRepoDevConfigLoadsExampleTree(t *testing.T) {
	root := repoRoot(t)
	cfg, err := Load(filepath.Join(root, "examples", "sermo-dev.yml"), WithCatalogDirs(repoCatalogDir(root)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, issue := range Validate(cfg) {
		t.Errorf("examples/sermo-dev.yml fails validation: %s", issue)
	}

	if _, ok := cfg.Services["apache-main"]; !ok {
		t.Fatalf("dev config did not load examples/services: %v", cfg.ServiceNames)
	}
	if _, ok := cfg.Apps["custom-tool"]; !ok {
		t.Fatalf("dev config did not load examples/apps: %v", cfg.AppNames)
	}
	notifiers, _ := cfg.Global.Raw["notifiers"].(map[string]any)
	if _, ok := notifiers["ops-email"]; !ok {
		t.Fatalf("dev config did not load examples/notifiers: %v", notifiers)
	}
	watches, errs := cfg.ResolveWatches()
	if len(errs) != 0 {
		t.Fatalf("dev config watch resolution failed: %v", errs)
	}
	for _, name := range []string{"storage-root", "mount-backup", "ping-gw", "load"} {
		if _, ok := watches[name]; !ok {
			t.Fatalf("dev config did not load watch %q from example dirs: %v", name, watches)
		}
	}
	if !slices.Contains(cfg.StorageMountNames(), "mount-backup") {
		t.Fatalf("dev config did not load mount-capable storage watch: %v", cfg.StorageMountNames())
	}
}

func TestExampleWatchDocsUseOneTargetPerFile(t *testing.T) {
	root := repoRoot(t)
	for _, relDir := range []string{"examples/watches", "examples/networks", "examples/storages", "examples/mounts"} {
		dir := filepath.Join(root, relDir)
		if !dirExists(dir) {
			t.Fatalf("%s is missing", relDir)
		}
		files, err := yamlFiles(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Fatalf("%s has no watch examples", relDir)
		}
		for _, name := range files {
			path := filepath.Join(dir, name)
			body := readYAMLMap(t, path)
			if _, grouped := body["watches"]; grouped {
				t.Fatalf("%s must be a single watch document, not a grouped watches map", path)
			}
			if cfgval.String(body["name"]) == "" {
				t.Fatalf("%s must declare top-level name", path)
			}
		}
	}
}

func TestExampleNotifierFragmentsUseOneTargetPerFile(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "examples", "notifiers")
	files, err := yamlFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("examples/notifiers has no notifier examples")
	}
	for _, name := range files {
		path := filepath.Join(dir, name)
		body := readYAMLMap(t, path)
		notifiers, ok := body["notifiers"].(map[string]any)
		if !ok {
			t.Fatalf("%s must declare top-level notifiers map", path)
		}
		if len(notifiers) != 1 {
			t.Fatalf("%s must contain exactly one notifier entry, got %d", path, len(notifiers))
		}
	}
}

func TestExampleTargetDocsUseOneTargetPerFile(t *testing.T) {
	root := repoRoot(t)
	for _, tc := range []struct {
		relDir     string
		groupedKey string
	}{
		{relDir: "examples/services", groupedKey: "services"},
		{relDir: "examples/apps", groupedKey: "apps"},
	} {
		t.Run(tc.relDir, func(t *testing.T) {
			dir := filepath.Join(root, tc.relDir)
			files, err := yamlFiles(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(files) == 0 {
				t.Fatalf("%s has no examples", tc.relDir)
			}
			for _, name := range files {
				path := filepath.Join(dir, name)
				body := readYAMLMap(t, path)
				if _, grouped := body[tc.groupedKey]; grouped {
					t.Fatalf("%s must be a single target document, not a grouped %s map", path, tc.groupedKey)
				}
				if cfgval.String(body["name"]) == "" {
					t.Fatalf("%s must declare top-level name", path)
				}
			}
		})
	}
}

func TestShippedServiceConfigsLiveUnderServices(t *testing.T) {
	root := repoRoot(t)
	servicesDir := filepath.Join(root, "examples", "services")
	if !dirExists(servicesDir) {
		t.Fatalf("examples/services is missing")
	}
	services, err := yamlFiles(servicesDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) == 0 {
		t.Fatalf("examples/services has no service examples")
	}

	assertExampleDocsHaveKind(t, filepath.Join(root, "examples", "apps"), kindApp)
}

func TestShippedServiceConfigExamplesValidate(t *testing.T) {
	root := repoRoot(t)
	servicesDir := filepath.Join(root, "examples", "services")
	if !dirExists(servicesDir) {
		t.Fatalf("examples/services is missing")
	}

	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  services: [" + servicesDir + "]\n  runtime: /run/sermo\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(t, global, WithCatalogDirs(repoCatalogDir(root)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Services) == 0 {
		t.Fatalf("examples/services has no loadable service examples")
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("examples/services examples must validate cleanly, got: %v", issues)
	}

	tests := []struct {
		service   string
		check     string
		preflight string
		binaries  []string
	}{
		{
			service:   "mariadb-backup-guard",
			check:     "mariadb-backup",
			preflight: "mariadb-backup-binary",
			binaries:  []string{"/usr/bin/mariadb-backup", "/usr/bin/mariadbbackup"},
		},
		{
			service:   "mysql-wal-g-backup-guard",
			check:     "wal-g-mysql",
			preflight: "wal-g-mysql-binary",
			binaries:  []string{"/usr/bin/wal-g-mysql", "/usr/local/bin/wal-g-mysql", "/usr/bin/wal-g", "/usr/local/bin/wal-g"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.service, func(t *testing.T) {
			resolved, errs := cfg.Resolve(tt.service)
			if len(errs) != 0 {
				t.Fatalf("Resolve(%s): %v", tt.service, errs)
			}
			exe := cfgval.String(valueAt(t, resolved.Tree, "checks", tt.check, "exe"))
			if !slices.Contains(tt.binaries, exe) {
				t.Fatalf("%s %s exe = %q, want one of %v", tt.service, tt.check, exe, tt.binaries)
			}
			preflight := nested(t, resolved.Tree, "preflight")
			entry, ok := preflight[tt.preflight].(map[string]any)
			if !ok {
				t.Fatalf("%s lacks app preflight %q: %v", tt.service, tt.preflight, preflight)
			}
			if got := cfgval.Bool(entry["optional"]); got {
				t.Fatalf("%s preflight %q optional = %v, want false", tt.service, tt.preflight, got)
			}
		})
	}
}

func TestGentooCatalogPidfileOverrides(t *testing.T) {
	old := detectedOS
	detectedOS = "gentoo"
	defer func() { detectedOS = old }()

	root := repoRoot(t)
	dir := t.TempDir()
	enabled := filepath.Join(dir, "services")
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(dir, "sermo.yml")
	body := "engine: { backend: openrc }\n" +
		"paths:\n  services: [" + enabled + "]\n  runtime: /run/sermo\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"clamd", "mariadb", "mysql", "upsd", "upsmon"} {
		svc := "name: " + name + "\nuses: " + name + "\n"
		if err := os.WriteFile(filepath.Join(enabled, name+".yml"), []byte(svc), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cfg, err := loadConfig(t, global, WithCatalogDirs(repoCatalogDir(root)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tests := []struct {
		name string
		want []string
	}{
		{name: "clamd", want: []string{"/run/clamd.pid", "/run/clamav/clamd.pid"}},
		{name: "mariadb", want: []string{"/run/mysqld/mariadb.pid", "/run/mysqld/mysqld.pid"}},
		// The `mysql` unit is what Gentoo's dev-db/mariadb installs, and that
		// MariaDB writes mariadb.pid. Without the second candidate the required
		// pidfile check fails on a perfectly healthy database.
		{name: "mysql", want: []string{"/run/mysqld/mysqld.pid", "/run/mysqld/mariadb.pid"}},
		// NUT's PIDPATH is a build-time default; a package that leaves STATEPATH
		// unset writes into /run instead of /run/nut.
		{name: "upsd", want: []string{"/run/nut/upsd.pid", "/run/upsd.pid"}},
		{name: "upsmon", want: []string{"/run/nut/upsmon.pid", "/run/upsmon.pid"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolved, errs := cfg.Resolve(tc.name)
			if len(errs) != 0 {
				t.Fatalf("Resolve() errors = %v", errs)
			}
			if got := cfgval.StringList(resolved.Tree["pidfile"]); !slices.Equal(got, tc.want) {
				t.Fatalf("pidfile = %q, want %q", got, tc.want)
			}
			check := nested(t, resolved.Tree, "checks", "pidfile")
			if got := cfgval.StringList(check["path"]); !slices.Equal(got, tc.want) {
				t.Fatalf("check pidfile = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCatalogAppsDoNotDeclareServiceProcessSelectors(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "catalog", "apps")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		doc := readYAMLMap(t, path)
		var found []string
		collectForbiddenKeys(doc, "", map[string]struct{}{"pidfile": {}, "processes": {}}, &found)
		if len(found) > 0 {
			t.Errorf("%s declares service process selector keys in catalog/apps: %s", path, strings.Join(found, ", "))
		}
	}
}

// TestSyslogNGCtlCheckUsesCtlBinary pins the `stats` probe to syslog-ng-ctl.
// The daemon binary does not take subcommands: `syslog-ng stats` exits 1 with
// "Excess number of arguments", so pointing the check at ${syslog_ng_binary}
// makes it fail on every host that runs syslog-ng at all.
func TestSyslogNGCtlCheckUsesCtlBinary(t *testing.T) {
	root := repoRoot(t)
	doc := catalogDocByName(t, root, "services", "syslog-ng")
	ctl := nested(t, doc, "watches", "ctl", "check")
	command := cfgval.StringList(ctl["command"])
	if len(command) == 0 {
		t.Fatalf("syslog-ng ctl check has no command: %v", ctl)
	}
	if command[0] == "${syslog_ng_binary}" {
		t.Fatalf("syslog-ng ctl check runs the daemon binary %q; `stats` is a syslog-ng-ctl subcommand", command[0])
	}
	if command[0] != "${ctl_binary}" {
		t.Fatalf("syslog-ng ctl command[0] = %q, want ${ctl_binary}", command[0])
	}

	cfg := loadRepoCatalog(t)
	resolved, errs := cfg.ResolveCatalog(CategoryService, "syslog-ng")
	if len(errs) > 0 {
		t.Fatalf("ResolveCatalog(syslog-ng): %v", errs)
	}
	resolvedCtl := nested(t, resolved.Tree, "checks", "ctl")
	got := cfgval.StringList(resolvedCtl["command"])
	if len(got) < 2 || !strings.HasSuffix(got[0], "/syslog-ng-ctl") || got[1] != "stats" {
		t.Fatalf("resolved syslog-ng ctl command = %q, want <path>/syslog-ng-ctl stats", got)
	}
}

// TestNetworkManagerStatusGatedOnNmcli pins the nmcli gate. NetworkManager is
// routinely active on hosts built without nmcli, where exec'ing it produced a
// permanently failing "no such file or directory" check and pinned the service
// to warning. The gate must be a verdictless sensor so absence is not a fault.
func TestNetworkManagerStatusGatedOnNmcli(t *testing.T) {
	root := repoRoot(t)
	doc := catalogDocByName(t, root, "services", "networkmanager")

	gate := nested(t, doc, "watches", "nmcli", "check")
	if got := cfgval.String(gate[checks.CheckKeyType]); got != checks.CheckTypeBinary {
		t.Fatalf("nmcli gate type = %q, want %q", got, checks.CheckTypeBinary)
	}
	if got := cfgval.String(gate[checks.CheckKeyReports]); got != checks.ReportsState {
		t.Fatalf("nmcli gate reports = %q, want %q so a missing client is not a fault", got, checks.ReportsState)
	}

	status := nested(t, doc, "watches", "status", "check")
	if got := cfgval.StringList(status[checks.CheckKeyRequires]); !slices.Equal(got, []string{"nmcli"}) {
		t.Fatalf("status requires = %q, want [nmcli]", got)
	}
}

func TestCatalogUnifiUsesMongodAppBinary(t *testing.T) {
	root := repoRoot(t)
	doc := catalogDocByName(t, root, "services", "unifi")
	if apps := strings.Join(cfgval.StringList(doc["apps"]), ","); apps != "java,mongod" {
		t.Fatalf("unifi apps = %q, want java,mongod", apps)
	}
	processes, ok := doc["processes"].(map[string]any)
	if !ok {
		t.Fatalf("unifi processes missing or invalid: %v", doc["processes"])
	}
	mongo, ok := processes["mongo"].(map[string]any)
	if !ok {
		t.Fatalf("unifi mongo process selector missing or invalid: %v", processes["mongo"])
	}
	if got := cfgval.String(mongo["exe"]); got != "${mongod_binary}" {
		t.Fatalf("unifi mongo exe = %q, want app variable ${mongod_binary}", got)
	}

	cfg := loadRepoCatalog(t)
	resolved, errs := cfg.ResolveCatalog(CategoryService, "unifi")
	if len(errs) > 0 {
		t.Fatalf("ResolveCatalog(unifi): %v", errs)
	}
	resolvedProcesses, ok := resolved.Tree["processes"].(map[string]any)
	if !ok {
		t.Fatalf("resolved unifi processes missing or invalid: %v", resolved.Tree["processes"])
	}
	resolvedMongo, ok := resolvedProcesses["mongo"].(map[string]any)
	if !ok {
		t.Fatalf("resolved unifi mongo process selector missing or invalid: %v", resolvedProcesses["mongo"])
	}
	if got := cfgval.String(resolvedMongo["exe"]); got != "/usr/bin/mongod" {
		t.Fatalf("resolved unifi mongo exe = %q, want /usr/bin/mongod", got)
	}
	preflight, ok := resolved.Tree["preflight"].(map[string]any)
	if !ok {
		t.Fatalf("resolved unifi preflight missing or invalid: %v", resolved.Tree["preflight"])
	}
	if _, ok := preflight["mongod-binary"]; !ok {
		t.Fatalf("resolved unifi preflight lacks mongod-binary: %v", preflight)
	}
}

func TestNebulaMeshCatalogProfiles(t *testing.T) {
	root := repoRoot(t)
	cfg := loadRepoCatalog(t)

	tests := []struct {
		name          string
		app           string
		user          string
		pid           string
		verifyService bool
	}{
		{
			name:          "nebula-agent",
			app:           "nebula-agent",
			user:          "root",
			pid:           "/run/supervise-nebula-agent.pid",
			verifyService: true,
		},
		{
			name: "nebula-mgmt",
			app:  "nebula-mgmt",
			user: "nebula-mgmt",
			pid:  "/run/supervise-nebula-mgmt.pid",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertNebulaMeshCatalogProfile(t, cfg, root, tt.name, tt.app, tt.user, tt.pid, tt.verifyService)
		})
	}

	mgmt, errs := cfg.ResolveCatalog(CategoryService, "nebula-mgmt")
	if len(errs) > 0 {
		t.Fatalf("ResolveCatalog(nebula-mgmt): %v", errs)
	}
	ready := nested(t, mgmt.Tree, "checks", "ready")
	if got := cfgval.String(ready["url"]); got != "http://127.0.0.1:8080/readyz" {
		t.Fatalf("ready URL = %q, want default Nebula Mesh readiness endpoint", got)
	}
	if !cfgval.Bool(ready["verify"]) {
		t.Fatal("management ready check must verify start")
	}
}

func assertNebulaMeshCatalogProfile(t *testing.T, cfg *Config, root, name, app, user, pid string, verifyService bool) {
	t.Helper()
	doc := catalogDocByName(t, root, "services", name)
	for _, backend := range []string{"systemd", "openrc"} {
		candidates, trust := ServiceCandidates(doc, backend, name)
		if trust || !slices.Equal(candidates, []string{name}) {
			t.Fatalf("%s service candidates = %v, trust = %v", backend, candidates, trust)
		}
	}
	if apps := cfgval.StringList(doc["apps"]); !slices.Equal(apps, []string{app}) {
		t.Fatalf("apps = %v, want [%s]", apps, app)
	}
	pidfile := nested(t, doc, "pidfile")
	if got := cfgval.String(pidfile["path"]); got != pid || !cfgval.Bool(pidfile["optional"]) {
		t.Fatalf("pidfile = %v, want optional %q", pidfile, pid)
	}

	resolved, errs := cfg.ResolveCatalog(CategoryService, name)
	if len(errs) > 0 {
		t.Fatalf("ResolveCatalog(%s): %v", name, errs)
	}
	process := nested(t, resolved.Tree, "processes", "main")
	if got := cfgval.String(process["user"]); got != user {
		t.Fatalf("process user = %q, want %q", got, user)
	}
	if got := cfgval.String(process["exe"]); got == "" {
		t.Fatal("process executable is empty")
	}
	if verifyService && !cfgval.Bool(nested(t, resolved.Tree, "checks", "service")["verify"]) {
		t.Fatal("service check must verify start")
	}
}

func TestSMBCatalogUsesSMBDPidfile(t *testing.T) {
	cfg := loadRepoCatalog(t)
	resolved, errs := cfg.ResolveCatalog(CategoryService, "smb")
	if len(errs) > 0 {
		t.Fatalf("ResolveCatalog(smb): %v", errs)
	}
	pidfiles := nested(t, resolved.Tree, "pidfiles")
	if got := cfgval.String(pidfiles["smbd"]); got == "" {
		t.Fatalf("pidfiles.smbd missing in %v", pidfiles)
	}
	process := nested(t, resolved.Tree, "processes", "smbd")
	if cfgval.String(process["exe"]) == "" || cfgval.String(process["user"]) == "" {
		t.Fatalf("processes.smbd lacks exact identity: %v", process)
	}
	check := nested(t, resolved.Tree, "checks", "pidfile-smbd")
	if cfgval.String(check["type"]) != "pidfile" || cfgval.String(check["path"]) == "" {
		t.Fatalf("checks.pidfile-smbd = %v, want pidfile check", check)
	}
	if _, found := pidfiles["nmbd"]; found {
		t.Fatalf("smb must not require nmbd pidfile: %v", pidfiles)
	}
	if processes := nested(t, resolved.Tree, "processes"); processes["nmbd"] != nil {
		t.Fatalf("smb must not own nmbd process: %v", processes)
	}
	if _, hasLegacy := resolved.Tree["pidfile"]; hasLegacy {
		t.Fatalf("smb must use pidfiles, not pidfile: %v", resolved.Tree["pidfile"])
	}
}

func TestCockpitCatalogMonitorsSocketActivationUnit(t *testing.T) {
	cfg := loadRepoCatalog(t)
	resolved, errs := cfg.ResolveCatalog(CategoryService, "cockpit")
	if len(errs) > 0 {
		t.Fatalf("ResolveCatalog(cockpit): %v", errs)
	}
	candidates, trust := ServiceCandidates(resolved.Tree, "systemd", "cockpit")
	if trust || !slices.Equal(candidates, []string{"cockpit.socket"}) {
		t.Fatalf("Cockpit systemd candidates = %v, trust = %v, want [cockpit.socket] and false", candidates, trust)
	}
	if units := AdditionalUnits(resolved.Tree, "systemd"); len(units) != 0 {
		t.Fatalf("Cockpit must not operate static cockpit.service as an auxiliary unit: %v", units)
	}
	if _, found := resolved.Tree["socket"]; found {
		t.Fatalf("Cockpit must not monitor session-only runtime sockets: %v", resolved.Tree["socket"])
	}
	if processes, found := resolved.Tree["processes"]; found {
		t.Fatalf("Cockpit must not require a transient socket-activated worker: %v", processes)
	}
	check := nested(t, resolved.Tree, "checks", "service")
	if cfgval.String(check["type"]) != "service" || cfgval.String(check["expect"]) != "active" {
		t.Fatalf("Cockpit service check = %v, want active socket service check", check)
	}
}

func TestCatalogServicesUseCanonicalServiceNames(t *testing.T) {
	// Pin the OS so the audit is deterministic regardless of the host running
	// CI: otherwise an Ubuntu runner collapses os: ubuntu overrides (e.g.
	// dhcpd's systemd unit becomes isc-dhcp-server) and the expectations below,
	// which assume the un-overridden catalog, would not match.
	old := detectedOS
	detectedOS = "gentoo"
	defer func() { detectedOS = old }()

	cfg := loadRepoCatalog(t)

	want := map[string][]string{
		"automount":    {"autofs", "automount"},
		"atftp":        {"atftp"},
		"avahi":        {"avahi", "avahi-daemon"},
		"cups":         {"cupsd"},
		"dbus":         {"dbus", "dbus-daemon"},
		"fail2ban":     {"fail2ban", "fail2ban-server"},
		"in.tftpd":     {"in.tftpd", "in-tftpd"},
		"keydb":        {"keydb", "keydb-server"},
		"lm_sensors":   {"lm_sensors", "lm-sensors"},
		"qemu-ga":      {"qemu-guest-agent", "qemu-ga"},
		"rpc-mountd":   {"rpc-mountd", "nfs-mountd"},
		"rsync":        {"rsyncd", "rsync"},
		"smb":          {"samba", "smb"},
		"spamassassin": {"spamd", "spamassassin"},
	}
	for name, openrcCandidates := range want {
		resolved, errs := cfg.ResolveCatalog(CategoryService, name)
		if len(errs) > 0 {
			t.Fatalf("ResolveCatalog(%s): %v", name, errs)
		}
		if resolved.Name != name {
			t.Fatalf("ResolveCatalog(%s) resolved name = %q", name, resolved.Name)
		}
		candidates, trust := ServiceCandidates(resolved.Tree, "openrc", name)
		if trust {
			t.Fatalf("ServiceCandidates(%s) trust = true, want explicit candidates", name)
		}
		if strings.Join(candidates, ",") != strings.Join(openrcCandidates, ",") {
			t.Fatalf("ServiceCandidates(%s) = %v, want %v", name, candidates, openrcCandidates)
		}
	}

	systemdAliases := map[string][]string{
		"clamd":      {"clamd", "clamav-daemon"},
		"dhcpd":      {"dhcpd", "dhcpd4"},
		"qemu-ga":    {"qemu-ga", "qemu-guest-agent"},
		"rpc-mountd": {"nfs-mountd", "rpc-mountd"},
		"smb":        {"smb"},
	}
	for name, wantSystemdCandidates := range systemdAliases {
		resolved, errs := cfg.ResolveCatalog(CategoryService, name)
		if len(errs) > 0 {
			t.Fatalf("ResolveCatalog(%s): %v", name, errs)
		}
		systemdCandidates, trust := ServiceCandidates(resolved.Tree, "systemd", name)
		if trust {
			t.Fatalf("ServiceCandidates(%s systemd) trust = true, want explicit candidates", name)
		}
		if strings.Join(systemdCandidates, ",") != strings.Join(wantSystemdCandidates, ",") {
			t.Fatalf("ServiceCandidates(%s systemd) = %v, want %v", name, systemdCandidates, wantSystemdCandidates)
		}
	}
}

func TestCatalogAppsDeclareVersionSource(t *testing.T) {
	cfg := loadRepoCatalog(t)

	noLocalVersion := map[string]string{
		"colord":           "colord has no version option; its D-Bus service exposes DaemonVersion",
		"iio-sensor-proxy": "iio-sensor-proxy has no version option or version property",
		"libvirt-dbus":     "upstream documents no version option for libvirt-dbus",
		"udisks2":          "upstream documents no version option for udisksd or udisksctl",
	}
	for _, name := range cfg.CatalogNamesInCategory(CategoryApp) {
		doc := cfg.Apps[name]
		if hasVersionProbe(doc.Body) {
			continue
		}
		if source := cfgval.String(doc.Body["version_from"]); source != "" {
			if !catalogAppProvidesVersion(cfg, source, map[string]bool{name: true}) {
				t.Errorf("%s version_from %q does not resolve to an app with a version probe", name, source)
			}
			continue
		}
		if reason := noLocalVersion[name]; reason == "" {
			t.Errorf("%s has no version probe, version_from, or documented exception", name)
		}
	}
}

func TestCatalogAppsDeclareHealthOrVersionSource(t *testing.T) {
	cfg := loadRepoCatalog(t)

	noSafeHealth := map[string]string{
		"nfsdcld": "upstream documents no help/version option; version comes from rpc-mountd",
		"rpcbind": "upstream documents version output but no separate help/health option; version comes from rpc-mountd",
	}
	for _, name := range cfg.CatalogNamesInCategory(CategoryApp) {
		doc := cfg.Apps[name]
		if hasHealthProbe(doc.Body) || hasVersionProbe(doc.Body) {
			continue
		}
		if source := cfgval.String(doc.Body["version_from"]); source != "" {
			if reason := noSafeHealth[name]; reason == "" {
				t.Errorf("%s has version_from %q but no local health probe", name, source)
			}
			continue
		}
		t.Errorf("%s has no health probe, version probe, or version_from", name)
	}
}

func TestCatalogOptionalAppVersionsRequireHealth(t *testing.T) {
	cfg := loadRepoCatalog(t)

	for _, name := range cfg.CatalogNamesInCategory(CategoryApp) {
		doc := cfg.Apps[name]
		if !versionProbeOptional(doc.Body) {
			continue
		}
		if !hasHealthProbe(doc.Body) {
			t.Errorf("%s has optional version but no health probe", name)
		}
	}
}

func TestCatalogAppsUseSharedVersionProviders(t *testing.T) {
	cfg := loadRepoCatalog(t)

	sharedVersions := map[string]string{
		"pmcd":          "pcp",
		"pmie":          "pcp",
		"pmie_farm":     "pcp",
		"pmlogger":      "pcp",
		"pmlogger_farm": "pcp",
		"rpcbind":       "rpc-mountd",
	}
	for app, provider := range sharedVersions {
		doc, ok := cfg.Apps[app]
		if !ok {
			t.Fatalf("shared-version app %q missing", app)
		}
		if got := cfgval.String(doc.Body["version_from"]); got != provider {
			t.Fatalf("%s version_from = %q, want %q", app, got, provider)
		}
		if hasVersionProbe(doc.Body) {
			t.Fatalf("%s duplicates provider %s with a local version probe", app, provider)
		}
		providerDoc, ok := cfg.Apps[provider]
		if !ok {
			t.Fatalf("version provider %q for %s missing", provider, app)
		}
		if !hasVersionProbe(providerDoc.Body) {
			t.Fatalf("version provider %q for %s has no version probe", provider, app)
		}
	}
}

func TestCatalogCupsUsesSingleCupsdApp(t *testing.T) {
	cfg := loadRepoCatalog(t)

	resolved, errs := cfg.ResolveCatalog(CategoryService, "cups")
	if len(errs) != 0 {
		t.Fatalf("ResolveCatalog(cups): %v", errs)
	}
	preflight := resolved.Tree["preflight"].(map[string]any)
	config := preflight["config"].(map[string]any)
	command := config["command"].([]any)
	if got := command[0]; got != "/usr/bin/cupsd" {
		t.Fatalf("cups config command = %v, want cupsd app binary", command)
	}
	tool := preflight["cupsd-cups-config"].(map[string]any)
	if got := tool["path"]; got != "/usr/bin/cups-config" {
		t.Fatalf("cupsd cups-config path = %v, want /usr/bin/cups-config", got)
	}
	if got := cfgval.Bool(tool["optional"]); !got {
		t.Fatalf("cupsd cups-config optional = %v, want true", got)
	}
	health := preflight["cupsd-health"].(map[string]any)
	healthCommand := health["command"].([]any)
	if len(healthCommand) != 2 || healthCommand[0] != "/usr/bin/cupsd" || healthCommand[1] != "-t" {
		t.Fatalf("cupsd health command = %v, want /usr/bin/cupsd -t", healthCommand)
	}
	version := preflight["cupsd-version"].(map[string]any)
	versionCommand := version["command"].([]any)
	if len(versionCommand) != 2 || versionCommand[0] != "/usr/bin/cups-config" || versionCommand[1] != "--version" {
		t.Fatalf("cupsd version command = %v, want /usr/bin/cups-config --version", versionCommand)
	}
	if got := cfgval.Bool(version["optional"]); !got {
		t.Fatalf("cupsd version optional = %v, want true", got)
	}
}

func TestCatalogConfigPreflightsUseResolvedAppTools(t *testing.T) {
	root := repoRoot(t)
	cfg := loadRepoCatalog(t)

	nebula := catalogDocByName(t, root, "services", "nebula-%i")
	nebulaCommand := cfgval.StringList(nested(t, nebula, "preflight", "config")["command"])
	if len(nebulaCommand) == 0 || nebulaCommand[0] != "${nebula_binary}" {
		t.Fatalf("nebula config command = %v, want app binary token first", nebulaCommand)
	}

	// cloudflared's config preflight is gated: `tunnel ingress validate` only
	// validates locally declared ingress rules, so it fails on a remotely-managed
	// tunnel whose config.yml carries just a token — and a failed preflight blocks
	// the restart. The gate prunes the entry on any host without the file, this
	// one included, so assert the token form from the raw document the way the
	// instanced nebula profile is asserted, plus the gate that has to track the
	// `config` variable it validates.
	cloudflared := catalogDocByName(t, root, "services", "cloudflared")
	cloudflaredConfig := nested(t, cloudflared, "preflight", "config")
	cloudflaredCommand := cfgval.StringList(cloudflaredConfig["command"])
	if len(cloudflaredCommand) == 0 || cloudflaredCommand[0] != "${cloudflared_binary}" {
		t.Fatalf("cloudflared config command = %v, want app binary token first", cloudflaredCommand)
	}
	if joined := strings.Join(cloudflaredCommand, " "); !strings.Contains(joined, "tunnel ingress validate") {
		t.Fatalf("cloudflared config command = %v, want the ingress validation subcommand", cloudflaredCommand)
	}
	cloudflaredGate := nested(t, cloudflaredConfig, "enable_if")
	if got := cfgval.String(cloudflaredGate["key"]); got != "ingress" {
		t.Fatalf("cloudflared config preflight gate key = %q, want ingress", got)
	}
	// enable_if is evaluated before ${var} expansion, so the gate repeats the
	// literal default of `config` and must not drift from it.
	wantGateFile := cfgval.String(nested(t, cloudflared, "variables")["config"])
	if got := cfgval.String(cloudflaredGate["file"]); got != wantGateFile {
		t.Fatalf("cloudflared config preflight gate file = %q, want the config variable default %q", got, wantGateFile)
	}

	// wantBase lists the acceptable resolved-tool basenames. The catalog binary
	// lists span several standard directories (and ${bindir} expands them
	// further), and an app may legitimately resolve to a fallback binary present
	// on the test host (e.g. mariadb -> mysqld), so the assertion is on the
	// program name, not its exact path — that path is verified host-independently
	// by the command-uses-resolved-tool check below.
	tests := []struct {
		service      string
		appToolCheck string
		toolArgIndex int
		wantBase     []string
		wantContains []string
	}{
		{
			service:      "docker",
			appToolCheck: "docker-daemon",
			toolArgIndex: 3,
			wantBase:     []string{"dockerd"},
			wantContains: []string{"--validate", "--config-file"},
		},
		{
			service:      "firewalld",
			appToolCheck: "firewalld-binary_offline",
			toolArgIndex: 0,
			wantBase:     []string{"firewall-offline-cmd"},
			wantContains: []string{"--check-config", "--system-config", "/etc/firewalld"},
		},
		{
			service:      "fetchmail",
			appToolCheck: "fetchmail-binary",
			toolArgIndex: 3,
			wantBase:     []string{"fetchmail"},
			wantContains: []string{"--configdump", "-f"},
		},
		{
			service:      "nmbd",
			appToolCheck: "nmbd-testparm",
			toolArgIndex: 0,
			wantBase:     []string{"testparm"},
			wantContains: []string{"-s"},
		},
		{
			service:      "slapd",
			appToolCheck: "slapd-slaptest",
			toolArgIndex: 3,
			wantBase:     []string{"slaptest"},
			wantContains: []string{"-Q", "-u"},
		},
		{
			service:      "loki",
			appToolCheck: "loki-binary",
			toolArgIndex: 0,
			wantBase:     []string{"loki"},
			wantContains: []string{"-verify-config", "-config.file"},
		},
		{
			service:      "influxdb",
			appToolCheck: "influxdb-binary",
			toolArgIndex: 0,
			wantBase:     []string{"influxd"},
			wantContains: []string{"config", "validate", "--config"},
		},
		{
			service:      "mysql",
			appToolCheck: "mysql-binary",
			toolArgIndex: 0,
			wantBase:     []string{"mysqld"},
			wantContains: []string{"--help", "--verbose"},
		},
		{
			service:      "mariadb",
			appToolCheck: "mariadb-binary",
			toolArgIndex: 0,
			wantBase:     []string{"mariadbd", "mysqld"}, // catalog falls back to mysqld
			wantContains: []string{"--help", "--verbose"},
		},
		{
			service:      "nginx",
			appToolCheck: "nginx-binary",
			toolArgIndex: 0,
			wantBase:     []string{"nginx"},
			wantContains: []string{"-t"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.service, func(t *testing.T) {
			resolved, errs := cfg.ResolveCatalog(CategoryService, tc.service)
			if len(errs) != 0 {
				t.Fatalf("ResolveCatalog(%s): %v", tc.service, errs)
			}
			preflight := nested(t, resolved.Tree, "preflight")
			tool := cfgval.String(nested(t, preflight, tc.appToolCheck)["path"])
			if !slices.Contains(tc.wantBase, filepath.Base(tool)) {
				t.Fatalf("%s app tool = %q (base %q), want one of %v", tc.service, tool, filepath.Base(tool), tc.wantBase)
			}
			command := nested(t, preflight, "config")["command"].([]any)
			if tc.toolArgIndex >= len(command) {
				t.Fatalf("%s config command = %v, missing tool arg index %d", tc.service, command, tc.toolArgIndex)
			}
			if got := cfgval.String(command[tc.toolArgIndex]); got != tool {
				t.Fatalf("%s config command tool = %q, want resolved app tool %q in %v", tc.service, got, tool, command)
			}
			joined := strings.Join(cfgval.StringList(command), " ")
			for _, want := range tc.wantContains {
				if !strings.Contains(joined, want) {
					t.Fatalf("%s config command = %v, want token %q", tc.service, command, want)
				}
			}
		})
	}
}

func TestCatalogNamedDNSCheckIsHostOverrideFriendly(t *testing.T) {
	root := repoRoot(t)
	body := catalogDocByName(t, root, "services", "named")

	vars := nested(t, body, "variables")
	for _, key := range []string{"host", "port", "query"} {
		if cfgval.String(vars[key]) == "" {
			t.Fatalf("named variables must include %q so host-specific listeners can be overridden: %v", key, vars)
		}
	}
	check := catalogWatchCheck(t, body, "port")
	if got := cfgval.String(check["host"]); got != "${host}" {
		t.Fatalf("named DNS check host = %q, want ${host}", got)
	}
	if got := cfgval.String(check["port"]); got != "${port}" {
		t.Fatalf("named DNS check port = %q, want ${port}", got)
	}
	if got := cfgval.String(check["query"]); got != "${query}" {
		t.Fatalf("named DNS check query = %q, want ${query}", got)
	}
}

// hostHardwareCheckTypes observe the machine, not the process being monitored.
// They belong in host watches, where the generated configuration already places
// them once per device or array.
var hostHardwareCheckTypes = []string{
	checks.CheckTypeRAID,
	checks.CheckTypeSmart,
	checks.CheckTypeSensors,
	checks.CheckTypeEDAC,
	checks.CheckTypeDiskIO,
	checks.CheckTypeHdparm,
}

// TestCatalogServicesDoNotJudgeTheirSubject pins the "Catalog service scope"
// rule in AGENTS.md: a service's checks describe that service, not what it
// observes. It replaces an audit that pinned the degraded-array predicate of a
// raid check inside mdadm/mdmonitor — a monitoring daemon that carries its
// subject's verdict reads failed for a fault it did not cause, hides which of
// the two is broken, and writes the subject's outage into its own availability
// archive. A host-hardware check may still appear in a service when it asserts
// nothing (reports: state/value).
func TestCatalogServicesDoNotJudgeTheirSubject(t *testing.T) {
	servicesDir := filepath.Join(repoRoot(t), "catalog", "services")
	walkCatalogDocs(t, servicesDir, func(path string, body map[string]any) {
		watches, _ := body["watches"].(map[string]any)
		for _, name := range slices.Sorted(maps.Keys(watches)) {
			watch, _ := watches[name].(map[string]any)
			check, _ := watch["check"].(map[string]any)
			checkType := cfgval.String(check[checks.CheckKeyType])
			if !slices.Contains(hostHardwareCheckTypes, checkType) {
				continue
			}
			if checks.VerdictlessMode(cfgval.String(check[checks.CheckKeyReports])) {
				continue
			}
			t.Errorf("%s: watch %q judges host hardware (%s) as service health; move it to a host watch or declare reports: state/value",
				filepath.Base(path), name, checkType)
		}
	})
}

func TestRequestedHostProfilesExist(t *testing.T) {
	cfg := loadRepoCatalog(t)

	tests := []struct {
		name        string
		app         string
		binaryVar   string
		processRole string
		wantProcess bool
	}{
		{name: "atftp", app: "atftp", binaryVar: "${atftp_binary}", wantProcess: true},
		{name: "clamd", app: "clamd", binaryVar: "${clamd_binary}", wantProcess: true},
		{name: "containerd", app: "containerd", binaryVar: "${containerd_binary}", wantProcess: true},
		{name: "dcc", app: "dcc", binaryVar: "${dcc_binary}", wantProcess: true},
		{name: "libvirt-dbus", app: "libvirt-dbus", binaryVar: "${libvirt_dbus_binary}", wantProcess: true},
		{name: "nfsdcld", app: "nfsdcld", binaryVar: "${nfsdcld_binary}", wantProcess: true},
		{name: "lm_sensors", app: "lm_sensors", wantProcess: false},
		{name: "qemu-ga", app: "qemu-ga", binaryVar: "${qemu_ga_binary}", wantProcess: true},
		{name: "smb", app: "smbd", binaryVar: "${smbd_binary}", processRole: "smbd", wantProcess: true},
		{name: "upower", app: "upower", binaryVar: "${upower_binary}", wantProcess: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertRequestedHostProfile(t, cfg, tc.name, tc.app, tc.binaryVar, tc.processRole, tc.wantProcess)
		})
	}
}

func assertRequestedHostProfile(t *testing.T, cfg *Config, name, app, binaryVar, processRole string, wantProcess bool) {
	t.Helper()
	doc, ok := cfg.CatalogServices[name]
	if !ok {
		t.Fatalf("service catalog %q not found", name)
	}
	if _, ok := cfg.Apps[app]; !ok {
		t.Fatalf("app catalog %q not found", app)
	}
	if !slices.Contains(cfgval.StringList(doc.Body["apps"]), app) {
		t.Fatalf("%s apps = %v, want %s", name, doc.Body["apps"], app)
	}
	resolved, errs := cfg.ResolveCatalog(CategoryService, name)
	if len(errs) > 0 {
		t.Fatalf("ResolveCatalog(%s): %v", name, errs)
	}
	check := nested(t, resolved.Tree, "checks", "service")
	if got := cfgval.String(check["type"]); got != "service" {
		t.Fatalf("%s service check type = %q, want service", name, got)
	}
	if got := cfgval.String(check["expect"]); got != "active" {
		t.Fatalf("%s service check expect = %q, want active", name, got)
	}
	processes, hasProcesses := doc.Body["processes"].(map[string]any)
	if !wantProcess {
		if !hasProcesses || len(processes) != 0 {
			t.Fatalf("%s processes = %v, want empty map for oneshot service", name, doc.Body["processes"])
		}
		return
	}
	if !hasProcesses {
		t.Fatalf("%s missing process selector", name)
	}
	if processRole == "" {
		processRole = "main"
	}
	main := nested(t, doc.Body, "processes", processRole)
	if got := cfgval.String(main["exe"]); got != binaryVar {
		t.Fatalf("%s process exe = %q, want %q", name, got, binaryVar)
	}
	if got := cfgval.String(main["user"]); got != "${user}" {
		t.Fatalf("%s process user = %q, want ${user}", name, got)
	}
}

func TestCatalogPHPFPMVersionedConfigTestUsesConfigFile(t *testing.T) {
	root := repoRoot(t)
	body := catalogDocByName(t, root, "services", "php-fpm%v%s%i")
	if _, ok := body["versions"]; ok {
		t.Fatalf("php-fpm must discover service instances from service:, got versions = %v", body["versions"])
	}
	service := nested(t, body, "service")
	systemdCandidates := cfgval.StringList(service["systemd"])
	openrcCandidates := cfgval.StringList(service["openrc"])
	for _, want := range []string{
		"php-fpm@${version}${sep}${instance}",
		"php-fpm@php${version}${sep}${instance}",
		"php-fpm-php${version}${sep}${instance}",
		"php${version}${sep}${instance}-fpm",
		"php-fpm${version}",
	} {
		if !slices.Contains(systemdCandidates, want) {
			t.Fatalf("php-fpm service.systemd = %v, want %q", systemdCandidates, want)
		}
	}
	for _, want := range []string{
		"php-fpm-php${version}${sep}${instance}",
		"php${version}${sep}${instance}",
		"php-fpm${version}${sep}${instance}",
		"php-fpm${version}",
	} {
		if !slices.Contains(openrcCandidates, want) {
			t.Fatalf("php-fpm service.openrc = %v, want %q", openrcCandidates, want)
		}
	}
	if got := cfgval.String(nested(t, body, "variables")["config"]); got != "/etc/php/fpm-php${version}${sep}${instance}/php-fpm.conf" {
		t.Fatalf("php-fpm config variable = %q", got)
	}
	wantPidfiles := []string{
		"/run/php-fpm-${version}${sep}${instance}.pid",
		"/run/php-fpm/php-fpm-${version}${sep}${instance}.pid",
		"/run/php-fpm/php-fpm-php${version}${sep}${instance}.pid",
		"/run/php-fpm-php${version}${sep}${instance}.pid",
	}
	// The top-level pidfile uses the optional mapping form so its auto-generated
	// service-gated pidfile check does not hard-fail php-fpm setups without a
	// pidfile, and does not collide with a separate watches.pidfile.
	pidfile := nested(t, body, "pidfile")
	if !cfgval.Bool(pidfile["optional"]) {
		t.Fatalf("php-fpm pidfile optional = %v, want true", pidfile["optional"])
	}
	if got := cfgval.StringList(pidfile["path"]); !slices.Equal(got, wantPidfiles) {
		t.Fatalf("php-fpm pidfile paths = %v, want %v", got, wantPidfiles)
	}
	config := nested(t, body, "preflight", "config")
	command, _ := config["command"].([]any)
	want := []any{"${binary}", "--test", "--fpm-config", "${config}", "--pid", "${config_test_pidfile}"}
	if len(command) != len(want) {
		t.Fatalf("php-fpm config command = %v, want %v", command, want)
	}
	for i := range want {
		if command[i] != want[i] {
			t.Fatalf("php-fpm config command = %v, want %v", command, want)
		}
	}
	if ruleEntries, ok := body["rules"].(map[string]any); ok {
		if _, ok := ruleEntries["restart-if-tcp-failed"]; ok {
			t.Fatal("php-fpm must not remediate on the optional tcp check by default")
		}
	}
}

func TestCatalogNetworkManagerStatusIsAuxiliary(t *testing.T) {
	root := repoRoot(t)
	app := catalogDocByName(t, root, "apps", "networkmanager")
	body := catalogDocByName(t, root, "services", "networkmanager")

	appVariables := nested(t, app, "variables")
	if got := cfgval.String(appVariables["nmcli"]); got != "${bindir}/nmcli" {
		t.Fatalf("networkmanager nmcli = %v, want ${bindir}/nmcli", appVariables["nmcli"])
	}
	nmcliPreflight := nested(t, app, "preflight", "nmcli")
	if !cfgval.Bool(nmcliPreflight["optional"]) {
		t.Fatalf("networkmanager nmcli preflight optional = %v, want true", nmcliPreflight["optional"])
	}

	status := catalogWatchCheck(t, body, "status")
	if !cfgval.Bool(status["optional"]) {
		t.Fatalf("networkmanager checks.status optional = %v, want true", status["optional"])
	}
	// The status check doubles as the post-operation verification (verify: true),
	// replacing the old duplicated postflight.status entry.
	if !cfgval.Bool(status["verify"]) {
		t.Fatalf("networkmanager checks.status verify = %v, want true", status["verify"])
	}
	command := cfgval.StringList(status["command"])
	want := []string{"${networkmanager_nmcli}", "general", "status"}
	if !slices.Equal(command, want) {
		t.Fatalf("networkmanager checks.status command = %v, want %v", command, want)
	}
	if ruleEntries, ok := body["rules"].(map[string]any); ok {
		if _, ok := ruleEntries["restart-if-status-failed"]; ok {
			t.Fatal("networkmanager must not remediate on the auxiliary nmcli status check")
		}
	}
	if watches, ok := body["watches"].(map[string]any); ok {
		if _, ok := watches["restart-if-status-failed"]; ok {
			t.Fatal("networkmanager must not remediate on the auxiliary nmcli status check")
		}
	}
}

func TestCatalogServiceProcessChecksUseLinkedAppBinaries(t *testing.T) {
	cfg := loadRepoCatalog(t)

	tests := []struct {
		name         string
		app          string
		preflight    string
		raw          string
		resolved     string
		rawPaths     [][]any
		resolvedPath []any
	}{
		{
			name:      "bluetooth",
			app:       "bluetoothd",
			preflight: "bluetoothd-binary",
			raw:       "${bluetoothd_binary}",
			resolved:  "/usr/libexec/bluetooth/bluetoothd",
			rawPaths: [][]any{
				{"watches", "process", "check", "exe"},
			},
			resolvedPath: []any{"checks", "process", "exe"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := cfg.CatalogServices[tt.name]
			if doc == nil {
				t.Fatalf("service catalog %q not found", tt.name)
			}
			if !slices.Contains(cfgval.StringList(doc.Body["apps"]), tt.app) {
				t.Fatalf("%s apps = %v, want %s", tt.name, doc.Body["apps"], tt.app)
			}
			for _, path := range tt.rawPaths {
				if got := cfgval.String(valueAt(t, doc.Body, path...)); got != tt.raw {
					t.Fatalf("%s raw %v = %q, want %q", tt.name, path, got, tt.raw)
				}
			}
			resolved, errs := cfg.ResolveCatalog(CategoryService, tt.name)
			if len(errs) > 0 {
				t.Fatalf("ResolveCatalog(%s): %v", tt.name, errs)
			}
			preflight := nested(t, resolved.Tree, "preflight")
			if _, ok := preflight[tt.preflight]; !ok {
				t.Fatalf("%s resolved preflight lacks %q: %v", tt.name, tt.preflight, preflight)
			}
			if len(tt.resolvedPath) > 0 {
				if got := cfgval.String(valueAt(t, resolved.Tree, tt.resolvedPath...)); got != tt.resolved {
					t.Fatalf("%s resolved %v = %q, want %q", tt.name, tt.resolvedPath, got, tt.resolved)
				}
			}
		})
	}
}

func TestCatalogOptionalOperationDependenciesAreNotLinkedApps(t *testing.T) {
	root := repoRoot(t)
	tests := []struct {
		service string
		app     string
	}{
		{service: "backrest", app: "restic"},
		{service: "smb", app: "winbindd"},
	}
	for _, tt := range tests {
		t.Run(tt.service, func(t *testing.T) {
			body := catalogDocByName(t, root, "services", tt.service)
			if slices.Contains(cfgval.StringList(body["apps"]), tt.app) {
				t.Fatalf("%s must not link optional app %q", tt.service, tt.app)
			}
		})
	}
}

func TestCatalogOpenVPNSystemdInstancesAreSystemdOnly(t *testing.T) {
	root := repoRoot(t)
	for _, name := range []string{"openvpn-client-%i", "openvpn-server-%i"} {
		t.Run(name, func(t *testing.T) {
			body := catalogDocByName(t, root, "services", name)
			service, ok := body["service"].(map[string]any)
			if !ok {
				t.Fatalf("%s service = %v, want per-init map", name, body["service"])
			}
			if got := cfgval.StringList(service["systemd"]); len(got) != 1 {
				t.Fatalf("%s service.systemd = %v, want one candidate", name, got)
			}
			if got := cfgval.StringList(service["openrc"]); len(got) != 0 {
				t.Fatalf("%s service.openrc = %v, want no OpenRC candidates", name, got)
			}
		})
	}

	body := catalogDocByName(t, root, "services", "openvpn%s%i")
	service, ok := body["service"].(map[string]any)
	if !ok {
		t.Fatalf("openvpn%%s%%i service = %v, want per-init map", body["service"])
	}
	if got := cfgval.StringList(service["systemd"]); len(got) != 0 {
		t.Fatalf("openvpn%%s%%i service.systemd = %v, want no systemd candidates", got)
	}
	if got := cfgval.StringList(service["openrc"]); !slices.Equal(got, []string{"openvpn.${instance}"}) {
		t.Fatalf("openvpn%%s%%i service.openrc = %v, want OpenRC legacy candidate", got)
	}
}

func TestCatalogPHPFPMInstancedCandidatesPreferInstance(t *testing.T) {
	root := repoRoot(t)
	body := catalogDocByName(t, root, "services", "php-fpm%v%s%i")
	service := nested(t, body, "service")
	for _, backend := range []string{"systemd", "openrc"} {
		candidates := cfgval.StringList(service[backend])
		if len(candidates) == 0 {
			t.Fatalf("php-fpm service.%s is empty", backend)
		}
		if !strings.Contains(candidates[0], "${sep}${instance}") {
			t.Fatalf("php-fpm service.%s first candidate = %q, want instance-specific candidate first", backend, candidates[0])
		}
	}

	systemdCandidates := cfgval.StringList(service["systemd"])
	if got, want := systemdCandidates[0], "php-fpm@${version}${sep}${instance}"; got != want {
		t.Fatalf("php-fpm service.systemd first candidate = %q, want %q", got, want)
	}
	if slices.Contains(systemdCandidates, "php-fpm") {
		t.Fatalf("php-fpm service.systemd includes generic php-fpm fallback: %v", systemdCandidates)
	}
}

func TestCatalogTomcatVersionDiscoveryUsesServiceCandidates(t *testing.T) {
	root := repoRoot(t)
	body := catalogDocByName(t, root, "services", "tomcat-%v%s%i")
	if _, ok := body["versions"]; ok {
		t.Fatalf("tomcat must discover service instances from service:, got versions = %v", body["versions"])
	}
	service := nested(t, body, "service")
	if got := cfgval.StringList(service["systemd"]); !slices.Equal(got, []string{"tomcat@${version}${sep}${instance}"}) {
		t.Fatalf("tomcat service.systemd = %v", got)
	}
	if got := cfgval.StringList(service["openrc"]); !slices.Equal(got, []string{"tomcat-${version}${sep}${instance}"}) {
		t.Fatalf("tomcat service.openrc = %v", got)
	}
}

func TestCatalogVarnishAdminChecksAreOptional(t *testing.T) {
	root := repoRoot(t)
	body := catalogDocByName(t, root, "services", "varnishd")
	for _, name := range []string{"port", "varnish"} {
		check := catalogWatchCheck(t, body, name)
		if !cfgval.Bool(check["optional"]) {
			t.Fatalf("varnishd check %q optional = %v, want true", name, check["optional"])
		}
	}
}

func TestCatalogDaemonProcessChecksAreAuxiliary(t *testing.T) {
	root := repoRoot(t)
	for _, service := range []string{"lldpd", "rngd", "rpc-idmapd", "smartd"} {
		body := catalogDocByName(t, root, "services", service)
		process := catalogWatchCheck(t, body, "process")
		if !cfgval.Bool(process["optional"]) {
			t.Fatalf("%s process check optional = %v, want true", service, process["optional"])
		}
	}
}

// TestCatalogCandidatePathsCoverPackagedLayouts pins the two catalog paths that
// have to name what a host really runs. Both were found by restarting the
// services on a live Gentoo host, and both failed the same way: the catalog
// named one packaged layout and the host used another. A resolved tree collapses
// a candidate list to whichever path exists there, which is host-dependent, so
// the invariant is asserted on the raw documents.
// TestCatalogEximCertGatedOnImplicitTLS pins the gate that stopped a healthy
// Exim reading warning forever. Asserted on the raw document because a resolved
// tree prunes the gate on any host without /etc/exim/exim.conf, this one
// included.
func TestCatalogEximCertGatedOnImplicitTLS(t *testing.T) {
	root := repoRoot(t)
	exim := catalogDocByName(t, root, "services", "exim")
	gate := nested(t, exim, "watches", "cert", "enable_if")
	if got := cfgval.String(gate["key"]); got != "tls_on_connect_ports" {
		t.Fatalf("exim cert gate key = %q, want tls_on_connect_ports", got)
	}
	// enable_if is evaluated before ${var} expansion, so the gate repeats the
	// literal default of `config` and must not drift from it.
	wantFile := cfgval.String(nested(t, exim, "variables")["config"])
	if got := cfgval.String(gate["file"]); got != wantFile {
		t.Fatalf("exim cert gate file = %q, want the config variable default %q", got, wantFile)
	}
	port := nested(t, exim, "variables", "tls_port")
	if got := cfgval.String(port["from_file"]); got != "${config}" {
		t.Fatalf("exim tls_port from_file = %q, want ${config}", got)
	}
	if got := cfgval.String(port["default"]); got != "465" {
		t.Fatalf("exim tls_port default = %q, want 465", got)
	}
}

// TestCatalogServiceIOAlertsShareOneCeiling stops the sampled-telemetry
// thresholds coming back. They were one host's live readings — 451 B/s for ssh
// against 32 MB/s for ceph-osd — so nearly every service alerted on normal work.
func TestCatalogServiceIOAlertsShareOneCeiling(t *testing.T) {
	const ceiling = "104857600"
	walkCatalogDocs(t, filepath.Join(repoRoot(t), "catalog", "services"), func(path string, body map[string]any) {
		watches, _ := body["watches"].(map[string]any)
		for name, raw := range watches {
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			check, _ := entry["check"].(map[string]any)
			if cfgval.String(check["name"]) != "io" || cfgval.String(check["scope"]) != "service" {
				continue
			}
			if got := cfgval.String(check["value"]); got != ceiling {
				t.Errorf("%s watch %s: io threshold = %q, want the shared ceiling %s", path, name, got, ceiling)
			}
		}
	})
}

// TestCatalogDelegatedWorkloadServicesDoNotSumFDs keeps a summed fd count out of
// the services whose control group holds workload the daemon does not own, where
// the sum describes the workload rather than the daemon.
func TestCatalogDelegatedWorkloadServicesDoNotSumFDs(t *testing.T) {
	root := repoRoot(t)
	for _, service := range []string{"libvirtd", "virtnetworkd", "docker", "containerd"} {
		body := catalogDocByName(t, root, "services", service)
		watches, _ := body["watches"].(map[string]any)
		for name, raw := range watches {
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			check, _ := entry["check"].(map[string]any)
			if cfgval.String(check["name"]) == "fds" && cfgval.String(check["scope"]) == "service" {
				t.Errorf("%s: watch %s sums fds over a control group holding delegated workload", service, name)
			}
		}
	}
}

func TestCatalogCandidatePathsCoverPackagedLayouts(t *testing.T) {
	root := repoRoot(t)

	// /usr/bin/grafana is a shell wrapper and the kernel reports the real binary
	// under /usr/share/grafana/bin as the process exe. With only the wrapper as a
	// candidate the service's exact exe selector matches nothing, and the
	// operation engine refuses to act on a service it cannot identify — the
	// restart is blocked, not merely unmonitored.
	grafana := cfgval.StringList(nested(t, catalogDocByName(t, root, "apps", "grafana"), "variables")["binary"])
	if len(grafana) < 2 || grafana[0] != "/usr/share/grafana/bin/grafana" {
		t.Fatalf("grafana app binary candidates = %v, want the real binary first", grafana)
	}
	if !slices.Contains(grafana, "${bindir}/grafana") {
		t.Fatalf("grafana app binary candidates = %v, want the ${bindir} fallback kept", grafana)
	}

	// A config preflight naming an absent file fails, and a failed preflight
	// blocks every operation, so each packaged location has to be a candidate.
	loki := cfgval.StringList(nested(t, catalogDocByName(t, root, "services", "loki"), "variables")["config"])
	for _, want := range []string{
		"/etc/loki/config.yml",             // Grafana's own deb/rpm
		"/etc/loki/loki-local-config.yaml", // Gentoo's unit default for LOKI_CONF
		"/etc/loki/local-config.yaml",      // upstream tarball and container image
	} {
		if !slices.Contains(loki, want) {
			t.Fatalf("loki config candidates = %v, want %q", loki, want)
		}
	}
}

func TestCatalogForegroundPidfilesAreOptional(t *testing.T) {
	cfg := loadRepoCatalog(t)
	for _, service := range []string{"rngd", "smartd"} {
		resolved, errs := cfg.ResolveCatalog(CategoryService, service)
		if len(errs) > 0 {
			t.Fatalf("ResolveCatalog(%s): %v", service, errs)
		}
		pidfile := nested(t, resolved.Tree, "checks", "pidfile")
		if !cfgval.Bool(pidfile["optional"]) {
			t.Fatalf("%s pidfile check optional = %v, want true", service, pidfile["optional"])
		}
	}
}

func assertCatalogUnixSocketHealth(t *testing.T, service, forbiddenCheck, socketPath string) {
	t.Helper()
	cfg := loadRepoCatalog(t)
	resolved, errs := cfg.ResolveCatalog(CategoryService, service)
	if len(errs) > 0 {
		t.Fatalf("ResolveCatalog(%s): %v", service, errs)
	}
	checks := nested(t, resolved.Tree, "checks")
	if _, ok := checks[forbiddenCheck]; ok {
		t.Fatalf("%s must not require %s by default: %v", service, forbiddenCheck, checks[forbiddenCheck])
	}
	socket := nested(t, checks, "restart-if-socket-missing")
	if got := cfgval.String(socket["path"]); got != socketPath {
		t.Fatalf("%s socket check path = %q, want %q", service, got, socketPath)
	}
	rule := nested(t, resolved.Tree, "rules", "restart-if-socket-missing", "if", "failed")
	if got := cfgval.String(rule["check"]); got != "restart-if-socket-missing" {
		t.Fatalf("%s remediation check = %q, want restart-if-socket-missing", service, got)
	}
}

func TestCatalogRRDCachedUsesUnixSocketHealth(t *testing.T) {
	assertCatalogUnixSocketHealth(t, "rrdcached", "tcp", "/run/rrdcached.sock")
}

func TestCatalogPCPFarmsDoNotCrossAttributeSharedPMPause(t *testing.T) {
	root := repoRoot(t)
	for _, service := range []string{"pmlogger_farm", "pmie_farm"} {
		body := catalogDocByName(t, root, "services", service)
		if _, found := body["processes"]; found {
			t.Fatalf("%s must not select the shared pmpause helper outside its unit cgroup", service)
		}
		watches := nested(t, body, "watches")
		if _, found := watches["process"]; found {
			t.Fatalf("%s must not publish a generic pmpause process watch", service)
		}
		svc := catalogWatchCheck(t, body, "service")
		if !cfgval.Bool(svc["verify"]) {
			t.Fatalf("%s start-verification must remain on checks.service", service)
		}
	}
}

// TestNoPostflightSectionRemains asserts the retired postflight: section is gone
// from every catalog and example document. Post-operation start-verification now
// lives on checks flagged verify: true, so a stray postflight: block would be
// silently ignored — this catches it.
func TestNoPostflightSectionRemains(t *testing.T) {
	root := repoRoot(t)
	for _, base := range []string{"catalog", "examples"} {
		walkCatalogDocs(t, filepath.Join(root, base), func(path string, body map[string]any) {
			if _, ok := body["postflight"]; ok {
				t.Errorf("%s still has a postflight: section — migrate its check to verify: true", path)
			}
		})
	}
}

func TestCatalogVirtlogdUsesSocketHealth(t *testing.T) {
	assertCatalogUnixSocketHealth(t, "virtlogd", "libvirt", "/run/libvirt/virtlogd-sock")
}

func TestCatalogServicesUseAppVariablesForBinaryRefs(t *testing.T) {
	cfg := loadRepoCatalog(t)

	tests := []struct {
		name              string
		service           string
		path              []any
		wantRaw           string
		wantResolved      string
		preflight         string
		preflightOptional bool
	}{
		{
			name:         "rspamd config uses rspamadm from app",
			service:      "rspamd",
			path:         []any{"preflight", "config", "command", 0},
			wantRaw:      "${rspamd_rspamadm}",
			wantResolved: "/usr/bin/rspamadm",
			preflight:    "rspamd-rspamadm",
		},
		{
			name:         "smbd config uses testparm from app",
			service:      "smbd",
			path:         []any{"preflight", "config", "command", 0},
			wantRaw:      "${smbd_testparm}",
			wantResolved: "/usr/bin/testparm",
			preflight:    "smbd-testparm",
		},
		{
			name:         "dovecot config uses doveconf from app",
			service:      "dovecot",
			path:         []any{"preflight", "config", "command", 0},
			wantRaw:      "${dovecot_doveconf}",
			wantResolved: "/usr/bin/doveconf",
			preflight:    "dovecot-doveconf",
		},
		{
			name:         "rpcbind process uses app binary",
			service:      "rpcbind",
			path:         []any{"processes", "main", "exe"},
			wantRaw:      "${rpcbind_binary}",
			wantResolved: "/usr/bin/rpcbind",
			preflight:    "rpcbind-binary",
		},
		{
			name:         "rpc idmapd process uses app binary",
			service:      "rpc-idmapd",
			path:         []any{"processes", "main", "exe"},
			wantRaw:      "${rpc_idmapd_binary}",
			wantResolved: "/usr/bin/rpc.idmapd",
			preflight:    "rpc-idmapd-binary",
		},
		{
			name:         "rpc mountd process uses app binary",
			service:      "rpc-mountd",
			path:         []any{"processes", "main", "exe"},
			wantRaw:      "${rpc_mountd_binary}",
			wantResolved: "/usr/bin/rpc.mountd",
			preflight:    "rpc-mountd-binary",
		},
		{
			name:         "alloy config validation uses app binary",
			service:      "alloy",
			path:         []any{"preflight", "config", "command", 0},
			wantRaw:      "${alloy_binary}",
			wantResolved: "/usr/bin/alloy",
			preflight:    "alloy-binary",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, ok := cfg.CatalogServices[tt.service]
			if !ok {
				t.Fatalf("service %q not found", tt.service)
			}
			if got := cfgval.String(valueAt(t, doc.Body, tt.path...)); got != tt.wantRaw {
				t.Fatalf("raw %s = %q, want %q", tt.service, got, tt.wantRaw)
			}
			resolved, errs := cfg.ResolveCatalog(CategoryService, tt.service)
			if len(errs) > 0 {
				t.Fatalf("ResolveCatalog(%s): %v", tt.service, errs)
			}
			if got := cfgval.String(valueAt(t, resolved.Tree, tt.path...)); got != tt.wantResolved {
				t.Fatalf("resolved %s = %q, want %q", tt.service, got, tt.wantResolved)
			}
			if tt.preflight == "" {
				return
			}
			preflight := nested(t, resolved.Tree, "preflight")
			entry, ok := preflight[tt.preflight].(map[string]any)
			if !ok {
				t.Fatalf("resolved %s lacks preflight %q: %v", tt.service, tt.preflight, preflight)
			}
			if got := cfgval.Bool(entry["optional"]); got != tt.preflightOptional {
				t.Fatalf("%s preflight %q optional = %v, want %v", tt.service, tt.preflight, got, tt.preflightOptional)
			}
		})
	}
}

func TestDatabaseCatalogServicesBlockRestartDuringBackup(t *testing.T) {
	root := repoRoot(t)
	cfg := loadRepoCatalog(t)

	section := func(tree map[string]any, key string) map[string]any {
		out, _ := tree[key].(map[string]any)
		return out
	}

	assertBackupGuard := func(name, label string, body map[string]any) {
		t.Helper()
		if apps := cfgval.StringList(body["apps"]); slices.Contains(apps, "mariadb-backup") {
			t.Fatalf("%s links mariadb-backup by default: %v", name, apps)
		}
		backup, ok := section(body, "checks")["backup"].(map[string]any)
		if !ok {
			if watch, hasWatch := section(body, "watches")["backup"].(map[string]any); hasWatch {
				backup, ok = watch["check"].(map[string]any)
			}
		}
		if !ok {
			t.Fatalf("%s %s catalog must define backup process check", name, label)
		}
		if !cfgval.Bool(backup["optional"]) {
			t.Fatalf("%s %s backup check must be optional", name, label)
		}
		if cfgval.String(backup["user"]) == "" {
			t.Fatalf("%s %s backup check must declare user", name, label)
		}
		if len(cfgval.StringList(backup["exe_any"])) == 0 {
			t.Fatalf("%s %s backup check must declare exe_any", name, label)
		}
		guard, ok := section(body, "rules")["block-restart-during-backup"].(map[string]any)
		if !ok {
			t.Fatalf("%s %s catalog must define backup restart guard", name, label)
		}
		if !slices.Contains(cfgval.StringList(guard["blocks"]), "restart") {
			t.Fatalf("%s %s backup guard blocks = %v, want restart", name, label, guard["blocks"])
		}
		if _, ok := section(body, "preflight")["mariadb-backup-binary"]; ok {
			t.Fatalf("%s %s catalog still has mariadb-backup preflight", name, label)
		}
	}

	for _, name := range []string{"mysql", "mariadb", "postgres-%v"} {
		raw := catalogDocByName(t, root, "services", name)
		assertBackupGuard(name, "raw", raw)

		doc, ok := cfg.CatalogServices[name]
		if !ok {
			continue
		}
		resolved, errs := cfg.ResolveCatalog(CategoryService, name)
		if len(errs) > 0 {
			t.Fatalf("ResolveCatalog(%s): %v", name, errs)
		}
		for _, tree := range []struct {
			label string
			body  map[string]any
		}{
			{label: "loaded", body: doc.Body},
			{label: "resolved", body: resolved.Tree},
		} {
			assertBackupGuard(name, tree.label, tree.body)
		}
	}
}

func TestPostgresCatalogDeclaresPostmasterPidfile(t *testing.T) {
	body := catalogDocByName(t, repoRoot(t), "services", "postgres-%v")
	if got := cfgval.String(body["pidfile"]); got != "${data_dir}/postmaster.pid" {
		t.Fatalf("postgres pidfile = %q, want ${data_dir}/postmaster.pid", got)
	}
}

func TestUbuntuCatalogOverrides(t *testing.T) {
	old := detectedOS
	detectedOS = "ubuntu"
	defer func() { detectedOS = old }()

	root := repoRoot(t)
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "engine: { backend: systemd }\n" +
		"paths:\n  services: []\n  runtime: /run/sermo\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(t, global, WithCatalogDirs(repoCatalogDir(root)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	assertSystemdUnit := func(service, want string) {
		t.Helper()
		body := cfg.CatalogServices[service].Body
		units := cfgval.StringList(nested(t, body, "service")["systemd"])
		if !slices.Contains(units, want) {
			t.Fatalf("%s systemd units = %v, want %s", service, units, want)
		}
	}
	assertSystemdUnit("dhcpd", "isc-dhcp-server")
	assertSystemdUnit("smartd", "smartmontools")
	assertSystemdUnit("upsd", "nut-server")
	assertSystemdUnit("upsmon", "nut-monitor")

	if got := cfgval.String(nested(t, cfg.CatalogServices["dhcpd"].Body, "variables")["user"]); got != "dhcpd" {
		t.Fatalf("dhcpd user = %q, want dhcpd", got)
	}
	if got := cfgval.String(cfg.CatalogServices["dhcpd"].Body["pidfile"]); got != "/run/dhcp-server/dhcpd.pid" {
		t.Fatalf("dhcpd pidfile = %q, want /run/dhcp-server/dhcpd.pid", got)
	}
	if got := cfgval.String(nested(t, cfg.CatalogServices["named"].Body, "variables")["user"]); got != "bind" {
		t.Fatalf("named user = %q, want bind", got)
	}

	pmcdBinaries := cfgval.StringList(nested(t, cfg.Apps["pmcd"].Body, "variables")["binary"])
	if !slices.Contains(pmcdBinaries, "/usr/lib/pcp/bin/pmcd") {
		t.Fatalf("pmcd binary candidates = %v, want /usr/lib/pcp/bin/pmcd", pmcdBinaries)
	}
	for _, tc := range []struct {
		app  string
		want string
	}{
		{app: "upsd", want: "/usr/lib/nut/upsd"},
		{app: "upsmon", want: "/usr/lib/nut/upsmon"},
	} {
		candidates := cfgval.StringList(nested(t, cfg.Apps[tc.app].Body, "variables")["binary"])
		if !slices.Contains(candidates, tc.want) {
			t.Fatalf("%s binary candidates = %v, want %q", tc.app, candidates, tc.want)
		}
	}

	cupsPreflight := nested(t, cfg.Apps["cupsd"].Body, "preflight")
	if !cfgval.Bool(nested(t, cupsPreflight, "cups-config")["optional"]) {
		t.Fatal("cups-config preflight must be optional for Ubuntu hosts without cups-config")
	}
	healthCommand := cfgval.StringList(nested(t, cupsPreflight, "health")["command"])
	if len(healthCommand) == 0 || healthCommand[0] != "${binary}" {
		t.Fatalf("cups health command = %v, want ${binary} -t", healthCommand)
	}
}

func TestHighRiskCatalogServicesHaveConservativeRemediationPolicy(t *testing.T) {
	root := repoRoot(t)

	for _, name := range []string{"mysql", "mariadb", "postgres-%v", "redis", "kafka-broker", "kafka-controller"} {
		t.Run(name, func(t *testing.T) {
			body := catalogDocByName(t, root, "services", name)
			assertConservativeRemediationPolicy(t, name, body)
		})
	}
}

func TestInstalledAutomationCatalogServicesHaveLocalRemediationPolicy(t *testing.T) {
	root := repoRoot(t)

	for _, name := range []string{
		"apache", "containerd", "dnsmasq", "docker", "firewalld", "monit",
		"libvirtd", "networkmanager", "node", "pm2", "polkit", "rsync",
		"smb", "smbd", "pmcd", "pppd", "syncthing", "tuned", "udisks2",
		"upower", "virtlockd", "virtlogd", "virtnetworkd", "xinetd",
		"zigbee2mqtt",
	} {
		t.Run(name, func(t *testing.T) {
			body := catalogDocByName(t, root, "services", name)
			assertConservativeRemediationPolicy(t, name, body)
		})
	}
}

func assertConservativeRemediationPolicy(t *testing.T, name string, body map[string]any) {
	t.Helper()

	policy := nested(t, body, "policy")
	cooldown, err := time.ParseDuration(cfgval.String(policy["cooldown"]))
	if err != nil {
		t.Fatalf("%s policy.cooldown does not parse: %v", name, err)
	}
	if cooldown < 15*time.Minute {
		t.Fatalf("%s policy.cooldown = %v, want at least 15m", name, cooldown)
	}
	maxActions, ok := cfgval.Int(policy["max_actions"])
	if !ok || maxActions <= 0 || maxActions > 2 {
		t.Fatalf("%s policy.max_actions = %v, want 1..2", name, policy["max_actions"])
	}
	window, err := time.ParseDuration(cfgval.String(policy["max_actions_window"]))
	if err != nil {
		t.Fatalf("%s policy.max_actions_window does not parse: %v", name, err)
	}
	if window < time.Hour {
		t.Fatalf("%s policy.max_actions_window = %v, want at least 1h", name, window)
	}
	backoff := nested(t, policy, "backoff")
	initial, err := time.ParseDuration(cfgval.String(backoff["initial"]))
	if err != nil {
		t.Fatalf("%s policy.backoff.initial does not parse: %v", name, err)
	}
	limit, err := time.ParseDuration(cfgval.String(backoff["max"]))
	if err != nil {
		t.Fatalf("%s policy.backoff.max does not parse: %v", name, err)
	}
	if initial < cooldown || limit < initial {
		t.Fatalf("%s backoff initial/max = %v/%v, want initial >= cooldown and max >= initial", name, initial, limit)
	}
}

// TestCatalogConfigInvalidHandledByPreflight asserts config validity is enforced
// by a REQUIRED config preflight (which aborts start/restart/reload/resume on an
// invalid config) — not by the retired block-restart-if-config-invalid guard,
// which was redundant with and unreachable behind that preflight.
func TestCatalogConfigInvalidHandledByPreflight(t *testing.T) {
	root := repoRoot(t)

	for _, name := range []string{
		"mysql", "mariadb", "postgres-%v", "dnsmasq", "monit", "nebula-%i",
		"named", "nginx", "cloudflared", "influxdb", "mongod", "slapd",
		"mosquitto", "supervisord",
	} {
		t.Run(name, func(t *testing.T) {
			body := catalogDocByName(t, root, "services", name)
			cfg, ok := nested(t, body, "preflight")["config"]
			if !ok {
				t.Fatalf("%s missing config preflight", name)
			}
			if m, ok := cfg.(map[string]any); ok && cfgval.Bool(m["optional"]) {
				t.Fatalf("%s config preflight must be required (it replaces the removed guard)", name)
			}
			if ruleEntries, ok := body["rules"].(map[string]any); ok {
				if _, present := ruleEntries["block-restart-if-config-invalid"]; present {
					t.Fatalf("%s still carries the redundant block-restart-if-config-invalid guard", name)
				}
			}
		})
	}
}

// TestCatalogHasNoConfigInvalidGuard asserts the redundant guard is gone from the
// whole catalog, not just the representative services above.
func TestCatalogHasNoConfigInvalidGuard(t *testing.T) {
	walkCatalogDocs(t, filepath.Join(repoRoot(t), "catalog", "services"), func(path string, body map[string]any) {
		if ruleSection, ok := body["rules"].(map[string]any); ok {
			if _, present := ruleSection["block-restart-if-config-invalid"]; present {
				t.Errorf("%s still carries block-restart-if-config-invalid (redundant with required config preflight)", path)
			}
		}
	})
}

func TestNamedCatalogUsesBackendNeutralConfigPreflight(t *testing.T) {
	root := repoRoot(t)
	app := catalogDocByName(t, root, "apps", "named")
	body := catalogDocByName(t, root, "services", "named")

	appVariables := nested(t, app, "variables")
	for name, want := range map[string]string{
		"binary":    "${bindir}/named",
		"checkconf": "${bindir}/named-checkconf",
	} {
		if got := cfgval.String(appVariables[name]); got != want {
			t.Fatalf("named app variable %s = %v, want %s", name, appVariables[name], want)
		}
	}
	appPreflight := nested(t, app, "preflight")
	if _, ok := appPreflight["checkconf"]; !ok {
		t.Fatalf("named app missing checkconf binary preflight")
	}

	entry := nested(t, body, "preflight", "config")
	command := cfgval.StringList(entry["command"])
	want := []string{"${named_checkconf}", "-z"}
	if !slices.Equal(command, want) {
		t.Fatalf("named config command = %v, want %v", command, want)
	}
	if _, ok := nested(t, body, "preflight")["zones"]; ok {
		t.Fatalf("named service should use named-checkconf -z instead of an init-specific zones check")
	}
}

func TestRedisCatalogAlertsOnPersistenceFailure(t *testing.T) {
	root := repoRoot(t)
	body := catalogDocByName(t, root, "services", "redis")

	if _, ok := nested(t, body, "watches")["persistence"]; ok {
		t.Fatal("redis persistence check should be embedded in the alert watch, not a standalone watch")
	}
	watch := nested(t, body, "watches", "alert-if-persistence-failed")
	check := nested(t, watch, "check")
	if got := cfgval.String(check["type"]); got != "redis" {
		t.Fatalf("redis persistence watch check type = %q, want redis", got)
	}
	if got := cfgval.String(nested(t, check, "expect")["rdb_last_bgsave_status"]); got != "ok" {
		t.Fatalf("redis persistence watch expectation = %q, want ok", got)
	}
	then := nested(t, watch, "then")
	if got := cfgval.String(then["action"]); got != "alert" {
		t.Fatalf("redis persistence watch action = %q, want alert", got)
	}
}

func TestWALGBackupAppsResolveRequiredBinaryPreflight(t *testing.T) {
	root := repoRoot(t)
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  services: []\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(t, global, WithCatalogDirs(repoCatalogDir(root)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	tests := []struct {
		name     string
		binaries []string
	}{
		{
			name:     "wal-g-mysql",
			binaries: []string{"/usr/bin/wal-g-mysql", "/usr/local/bin/wal-g-mysql", "/usr/bin/wal-g", "/usr/local/bin/wal-g"},
		},
		{
			name:     "wal-g-pg",
			binaries: []string{"/usr/bin/wal-g-pg", "/usr/local/bin/wal-g-pg", "/usr/bin/wal-g", "/usr/local/bin/wal-g"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertWALGRequiredBinaryPreflight(t, cfg, tt.name, tt.binaries)
		})
	}
}

func assertWALGRequiredBinaryPreflight(t *testing.T, cfg *Config, name string, binaries []string) {
	t.Helper()
	doc, ok := cfg.Apps[name]
	if !ok {
		t.Fatalf("app %q not found", name)
	}
	vars, _ := doc.Body["variables"].(map[string]any)
	candidates := cfgval.StringList(vars["binary"])
	for _, binary := range binaries {
		if !slices.Contains(candidates, binary) {
			t.Fatalf("%s binary candidates = %v, missing %s", name, candidates, binary)
		}
	}
	resolved, errs := cfg.ResolveCatalog(CategoryApp, name)
	if len(errs) > 0 {
		t.Fatalf("ResolveCatalog(%s): %v", name, errs)
	}
	binary := cfgval.String(valueAt(t, resolved.Tree, "variables", "binary"))
	if !slices.Contains(binaries, binary) {
		t.Fatalf("%s resolved binary = %q, want one of %v", name, binary, binaries)
	}
	preflight := nested(t, resolved.Tree, "preflight")
	assertRequiredWALGPreflight(t, name, preflight, "binary")
	version := assertRequiredWALGPreflight(t, name, preflight, "version")
	command, ok := version["command"].([]any)
	if !ok || len(command) == 0 {
		t.Fatalf("%s version command missing: %v", name, version)
	}
	if got := cfgval.String(command[0]); got != binary {
		t.Fatalf("%s version command binary = %q, want %q", name, got, binary)
	}
}

func assertRequiredWALGPreflight(t *testing.T, name string, preflight map[string]any, key string) map[string]any {
	t.Helper()
	entry, ok := preflight[key].(map[string]any)
	if !ok {
		t.Fatalf("%s lacks preflight %q: %v", name, key, preflight)
	}
	if cfgval.Bool(entry["optional"]) {
		t.Fatalf("%s preflight %q optional = true, want false", name, key)
	}
	return entry
}

func TestCatalogServicesReuseLinkedAppBinaries(t *testing.T) {
	cfg := loadRepoCatalog(t)

	for _, name := range cfg.CatalogServiceNames {
		doc := cfg.CatalogServices[name]
		serviceBinary := catalogBinary(doc)
		if serviceBinary == "" {
			continue
		}
		for _, appName := range cfgval.StringList(doc.Body["apps"]) {
			appDoc, ok := cfg.Apps[appName]
			if !ok {
				continue
			}
			if serviceBinary != catalogBinary(appDoc) {
				continue
			}
			t.Errorf("%s defines binary %q already owned by app %s; use ${%s_binary} instead", name, serviceBinary, appName, appVariablePrefix(appName))
			if hasVersionProbe(doc.Body) {
				t.Errorf("%s defines a service-level version probe already owned by app %s", name, appName)
			}
		}
	}
}

func TestCatalogServicesDoNotOwnRuntimeResourcePreflight(t *testing.T) {
	root := repoRoot(t)
	files, err := yamlFiles(filepath.Join(root, "catalog", "services"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		path := filepath.Join(root, "catalog", "services", file)
		doc := readYAMLMap(t, path)
		preflight, _ := doc["preflight"].(map[string]any)
		for name, raw := range preflight {
			entry, _ := raw.(map[string]any)
			switch cfgval.String(entry["type"]) {
			case "binary", "libraries":
				t.Errorf("%s preflight.%s uses runtime resource type %q; move it to catalog/apps", path, name, entry["type"])
			}
		}
	}
}

func TestCatalogVersionedServicesHaveDiscoverySource(t *testing.T) {
	root := repoRoot(t)
	catalogDir := repoCatalogDir(root)
	apps := catalogAppsByName(t, catalogDir)
	serviceFiles, err := yamlFiles(filepath.Join(catalogDir, "services"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range serviceFiles {
		path := filepath.Join(catalogDir, "services", file)
		validateVersionedCatalogService(t, path, readYAMLMap(t, path), apps)
	}
}

func catalogAppsByName(t *testing.T, catalogDir string) map[string]map[string]any {
	t.Helper()
	files, err := yamlFiles(filepath.Join(catalogDir, "apps"))
	if err != nil {
		t.Fatal(err)
	}
	apps := make(map[string]map[string]any, len(files))
	for _, file := range files {
		path := filepath.Join(catalogDir, "apps", file)
		doc := readYAMLMap(t, path)
		if name := cfgval.String(doc["name"]); name != "" {
			apps[name] = doc
		}
	}
	return apps
}

func validateVersionedCatalogService(t *testing.T, path string, doc map[string]any, apps map[string]map[string]any) {
	t.Helper()
	tokens := tokensFor(cfgval.String(doc["name"]))
	if len(tokens) == 0 {
		if _, hasVersions := doc["versions"]; hasVersions {
			t.Errorf("%s declares versions but its name carries no template token", path)
		}
		return
	}
	if serviceTemplateDiscoversTokens(doc, tokens) || discoverySourcesHaveTokens(versionDiscoverySources(doc), tokens) {
		return
	}
	if linkedAppTemplateDiscoversTokens(doc, apps, tokens) {
		return
	}
	t.Errorf("%s is a template but neither has token-bearing service candidates nor links an app template that can discover its tokens", path)
}

func serviceTemplateDiscoversTokens(doc map[string]any, tokens []tmplToken) bool {
	for _, backend := range []string{"systemd", "openrc"} {
		candidates, _ := ServiceCandidates(doc, backend, "")
		if len(serviceUnitPatternsForBackend(backend, candidates, tokens)) > 0 {
			return true
		}
	}
	return false
}

func discoverySourcesHaveTokens(sources []string, tokens []tmplToken) bool {
	for _, source := range sources {
		if containsAllMarkers(source, tokens) {
			return true
		}
	}
	return false
}

// versionDiscoverySources is the catalog-audit view of discovery globs: every
// backend branch of versions.from plus binary candidates. Production materializes
// with the active backend only; audit needs both so OpenRC-only templates still
// validate on a systemd host (and vice versa).
func versionDiscoverySources(body map[string]any) []string {
	if from := allVersionsFromPaths(body); len(from) > 0 {
		return from
	}
	return documentBinaryCandidates(body)
}

func allVersionsFromPaths(body map[string]any) []string {
	if v, ok := body[keyVersions].(map[string]any); ok {
		return allVersionFromPaths(v[keyVersionsFrom])
	}
	return nil
}

func allVersionFromPaths(raw any) []string {
	if m, ok := raw.(map[string]any); ok {
		systemd := cfgval.StringList(m[backendSystemd])
		openrc := cfgval.StringList(m[backendOpenRC])
		out := make([]string, 0, len(systemd)+len(openrc))
		out = append(out, systemd...)
		out = append(out, openrc...)
		return out
	}
	return cfgval.StringList(raw)
}

func linkedAppTemplateDiscoversTokens(doc map[string]any, apps map[string]map[string]any, tokens []tmplToken) bool {
	for _, appName := range cfgval.StringList(doc["apps"]) {
		app, ok := apps[linkedAppTemplateNameMulti(appName, tokens)]
		if ok && discoverySourcesHaveTokens(versionDiscoverySources(app), tokens) {
			return true
		}
	}
	return false
}

func TestCatalogCommandEntriesDoNotUseArgumentKeys(t *testing.T) {
	root := repoRoot(t)
	catalogDir := repoCatalogDir(root)
	err := filepath.WalkDir(catalogDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !isYAML(entry.Name()) {
			return nil
		}
		doc := readYAMLMap(t, path)
		checkCommandArgumentKeys(t, path, doc, "")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCatalogServicePreflightCommandsAvoidInitBackendTools(t *testing.T) {
	root := repoRoot(t)
	servicesDir := filepath.Join(root, "catalog", "services")
	forbidden := []string{"/etc/init.d/", "rc-service", "systemctl"}

	err := filepath.WalkDir(servicesDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !isYAML(entry.Name()) {
			return nil
		}
		doc := readYAMLMap(t, path)
		preflight, _ := doc["preflight"].(map[string]any)
		for name, raw := range preflight {
			entry, _ := raw.(map[string]any)
			if cfgval.String(entry["type"]) != "command" {
				continue
			}
			command := strings.Join(cfgval.StringList(entry["command"]), " ")
			for _, token := range forbidden {
				if strings.Contains(command, token) {
					t.Errorf("%s preflight.%s command %q uses init-backend tool %q; use service-native validation instead", path, name, command, token)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func checkCommandArgumentKeys(t *testing.T, file string, node any, keyPath string) {
	t.Helper()
	switch v := node.(type) {
	case map[string]any:
		if cfgval.String(v["type"]) == "command" {
			for key := range v {
				if strings.HasPrefix(key, "-") {
					t.Errorf("%s %s has command argument key %q outside command list", file, keyPath, key)
				}
			}
		}
		for key, child := range v {
			next := key
			if keyPath != "" {
				next = keyPath + "." + key
			}
			checkCommandArgumentKeys(t, file, child, next)
		}
	case []any:
		for _, child := range v {
			checkCommandArgumentKeys(t, file, child, keyPath+"[]")
		}
	}
}

func catalogBinary(doc *Document) string {
	if doc == nil {
		return ""
	}
	return DocumentBinary(doc.Body)
}

func hasVersionProbe(body map[string]any) bool {
	if preflight, _ := body["preflight"].(map[string]any); preflight != nil {
		if _, ok := preflight["version"]; ok {
			return true
		}
	}
	if commands, _ := body["commands"].(map[string]any); commands != nil {
		if _, ok := commands["version"]; ok {
			return true
		}
	}
	return false
}

func hasHealthProbe(body map[string]any) bool {
	preflight, _ := body["preflight"].(map[string]any)
	if preflight == nil {
		return false
	}
	_, ok := preflight["health"]
	return ok
}

func versionProbeOptional(body map[string]any) bool {
	preflight, _ := body["preflight"].(map[string]any)
	if preflight == nil {
		return false
	}
	version, _ := preflight["version"].(map[string]any)
	if version == nil {
		return false
	}
	return cfgval.Bool(version["optional"])
}

func catalogAppProvidesVersion(cfg *Config, name string, seen map[string]bool) bool {
	if seen[name] {
		return false
	}
	seen[name] = true
	doc, ok := cfg.Apps[name]
	if !ok {
		return false
	}
	if hasVersionProbe(doc.Body) {
		return true
	}
	source := cfgval.String(doc.Body["version_from"])
	if source == "" {
		return false
	}
	return catalogAppProvidesVersion(cfg, source, seen)
}

func valueAt(t *testing.T, tree map[string]any, path ...any) any {
	t.Helper()
	var cur any = tree
	for _, elem := range path {
		switch key := elem.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				t.Fatalf("path %v: expected map before key %q, got %T", path, key, cur)
			}
			var found bool
			cur, found = m[key]
			if !found {
				t.Fatalf("path %v: key %q not found", path, key)
			}
		case int:
			a, ok := cur.([]any)
			if !ok {
				t.Fatalf("path %v: expected array before index %d, got %T", path, key, cur)
			}
			if key < 0 || key >= len(a) {
				t.Fatalf("path %v: index %d out of range", path, key)
			}
			cur = a[key]
		default:
			t.Fatalf("path %v: unsupported path element %T", path, elem)
		}
	}
	return cur
}

func yamlFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if !entry.IsDir() && isYAML(entry.Name()) {
			out = append(out, entry.Name())
		}
	}
	return out, nil
}

func assertExampleDocsHaveKind(t *testing.T, dir, kind string) {
	t.Helper()
	if !dirExists(dir) {
		return
	}
	files, err := yamlFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		doc, err := loadDocument(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		// The kind is derived from the directory; a `kind:` is optional but, when
		// present, must not contradict the location.
		if err := assignKind(doc, kind); err != nil {
			t.Fatalf("%s: %v", doc.Path, err)
		}
	}
}

func collectForbiddenKeys(node any, keyPath string, forbidden map[string]struct{}, found *[]string) {
	switch v := node.(type) {
	case map[string]any:
		for key, child := range v {
			next := key
			if keyPath != "" {
				next = keyPath + "." + key
			}
			if _, ok := forbidden[key]; ok {
				*found = append(*found, next)
			}
			collectForbiddenKeys(child, next, forbidden, found)
		}
	case []any:
		for _, child := range v {
			collectForbiddenKeys(child, keyPath, forbidden, found)
		}
	}
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// Generated alert text is built in Go, not through ${display_name}, so a field
// that is a mapping rather than a scalar reaches the operator as a Go literal.
// A profile naming its unit per init backend (`service: {systemd: [...],
// openrc: [...]}`) and carrying no display_name did exactly that, shipping
// "map[openrc:[rsyncd rsync] systemd:[rsync rsyncd]] is running a binary that
// was replaced on disk" to the fleet. Assert no profile can regress into it.
func TestRealCatalogGeneratedMessagesHaveNoGoLiterals(t *testing.T) {
	root := repoRoot(t)
	for _, backend := range []string{"systemd", "openrc"} {
		t.Run(backend, func(t *testing.T) {
			cfg := loadAllCatalogServices(t, repoCatalogDir(root), backend)
			for _, name := range cfg.ServiceNames {
				resolved, _ := cfg.Resolve(name)
				for _, message := range generatedRuleMessages(resolved.Tree) {
					if strings.Contains(message, "map[") || strings.Contains(message, "[]interface {}") {
						t.Errorf("%s: generated message contains a Go literal: %q", name, message)
					}
				}
			}
		})
	}
}

// generatedRuleMessages returns every alert message of every rule in a resolved
// service tree.
func generatedRuleMessages(tree map[string]any) []string {
	section, _ := tree[rules.SectionRules].(map[string]any)
	var out []string
	for _, raw := range section {
		rule, _ := raw.(map[string]any)
		then, _ := rule[rules.RuleFieldThen].(map[string]any)
		actions, _ := then[rules.RuleFieldActions].([]any)
		for _, action := range actions {
			entry, _ := action.(map[string]any)
			if message, ok := entry[rules.RuleFieldMessage].(string); ok {
				out = append(out, message)
			}
		}
	}
	return out
}
