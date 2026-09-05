package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sermo/internal/cfgval"
)

func TestLoadResolvesRelativePaths(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "examples")
	serviceDir := filepath.Join(configDir, "services")
	catalogDir := filepath.Join(root, "catalog")
	catalogServicesDir := filepath.Join(catalogDir, "services")
	for _, d := range []string{serviceDir, catalogServicesDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(catalogServicesDir, "redis.yml"), []byte(`
name: redis
variables: { port: 6379 }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serviceDir, "redis-main.yml"), []byte(`
name: redis-main
uses: redis
`), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(configDir, "sermo.yml")
	if err := os.WriteFile(global, []byte(`
engine: { backend: auto }
paths:
  services: [services]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
watches:
  disk:
    enabled: false
    check: { type: storage, path: /, used_pct: { op: ">=", value: 90 } }
    then:
      hook: { command: [/bin/true] }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, global, WithCatalogDirs(catalogDir))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.Global.ServicePaths[0].Path; got != serviceDir {
		t.Fatalf("ServicePaths[0].Path = %q, want %q", got, serviceDir)
	}
	if len(cfg.Services) != 1 {
		t.Fatalf("Services = %d, want 1", len(cfg.Services))
	}
	watches, _ := cfg.Global.Raw["watches"].(map[string]any)
	if len(watches) != 1 {
		t.Fatalf("watches in global config = %d, want 1", len(watches))
	}
}

func TestDefaultServiceAndAppDirs(t *testing.T) {
	wantServices := []string{"/etc/sermo/services"}
	gotServices := defaultConfigDirs(DefaultGlobalPath, defaultServiceDirs)
	if strings.Join(gotServices, "\n") != strings.Join(wantServices, "\n") {
		t.Fatalf("default service dirs = %v, want %v", gotServices, wantServices)
	}
	wantApps := []string{"/etc/sermo/apps"}
	gotApps := defaultConfigDirs(DefaultGlobalPath, defaultAppDirs)
	if strings.Join(gotApps, "\n") != strings.Join(wantApps, "\n") {
		t.Fatalf("default app dirs = %v, want %v", gotApps, wantApps)
	}
}

func TestLoadUsesConfigRelativeDefaultServiceDirsWhenServiceDirsOmitted(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  runtime: /run/sermo
`,
		"services/web.yml": `
name: web
`,
	})

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	root := filepath.Dir(global)
	wantServices := []string{filepath.Join(root, "services")}
	if got, want := cfg.Global.ServicePaths, pathSpecsFromPaths(wantServices); !slices.Equal(got, want) {
		t.Fatalf("Global.ServicePaths = %v, want %v", got, want)
	}
	if _, ok := cfg.Services["web"]; !ok {
		t.Fatalf("service from default services include was not loaded")
	}
}

func TestLoadDoesNotUseConfigRelativeDefaultWatchDirsWhenWatchesOmitted(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  runtime: /run/sermo
`,
		"watches/data.yml": `
name: mount-data
check: { type: storage, path: /data, mounted: true }
mount: {}
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	watches, errs := cfg.ResolveWatches()
	if len(errs) != 0 {
		t.Fatalf("ResolveWatches() errors: %v", errs)
	}
	if _, ok := watches["mount-data"]; ok {
		t.Fatalf("watch directory should not be loaded unless paths.watches lists it")
	}
}

func TestLoadPathSpecsRecursiveOptIn(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  services:
    - path: @ROOT@/services-flat
    - path: @ROOT@/services-recursive
      recursive: true
  apps:
    - path: @ROOT@/apps-flat
    - path: @ROOT@/apps-recursive
      recursive: true
  notifiers:
    - path: @ROOT@/notifiers-flat
    - path: @ROOT@/notifiers-recursive
      recursive: true
  watches:
    - path: @ROOT@/storages-flat
    - path: @ROOT@/storages-recursive
      recursive: true
    - path: @ROOT@/networks-flat
    - path: @ROOT@/networks-recursive
      recursive: true
    - path: @ROOT@/watches-flat
    - path: @ROOT@/watches-recursive
      recursive: true
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
notify: [ops]
`,
		"services-flat/direct-service.yml": `
name: direct-service
service: direct-service
`,
		"services-flat/deep/skipped-service.yml": `
name: skipped-service
service: skipped-service
`,
		"services-recursive/deep/recursive-service.yml": `
name: recursive-service
service: recursive-service
`,
		"apps-flat/direct-app.yml": `
name: direct-app
variables: { binary: /bin/true }
`,
		"apps-flat/deep/skipped-app.yml": `
name: skipped-app
variables: { binary: /bin/true }
`,
		"apps-recursive/deep/recursive-app.yml": `
name: recursive-app
variables: { binary: /bin/true }
`,
		"notifiers-flat/ops.yml": `
notifiers:
  ops:
    enabled: false
    type: email
`,
		"notifiers-flat/deep/skipped-notifier.yml": `
notifiers:
  skipped-notifier:
    enabled: false
    type: email
`,
		"notifiers-recursive/deep/team.yml": `
notifiers:
  team:
    enabled: false
    type: email
`,
		"storages-flat/root.yml": `
name: storage-direct
check:
  type: storage
  path: /
  used_pct: { op: ">=", value: "90%" }
then: { notify: [ops] }
`,
		"storages-flat/deep/skipped.yml": `
name: storage-skipped
check:
  type: storage
  path: /tmp
  used_pct: { op: ">=", value: "90%" }
then: { notify: [ops] }
`,
		"storages-recursive/deep/root.yml": `
name: storage-recursive
check:
  type: storage
  path: /var
  used_pct: { op: ">=", value: "90%" }
then: { notify: [ops] }
`,
		"networks-flat/ping.yml": `
name: network-direct
category: network
check: { type: icmp, host: 192.0.2.1 }
metrics:
  state:
    expect: up
    then: { notify: [ops] }
`,
		"networks-flat/deep/skipped.yml": `
name: network-skipped
check: { type: icmp, host: 192.0.2.2 }
metrics:
  state:
    expect: up
    then: { notify: [ops] }
`,
		"networks-recursive/deep/ping.yml": `
name: network-recursive
check: { type: icmp, host: 192.0.2.3 }
metrics:
  state:
    expect: up
    then: { notify: [ops] }
`,
		"watches-flat/load.yml": `
name: load-direct
check: { type: load, load5: { op: ">", value: 2 } }
then: { notify: [ops] }
`,
		"watches-flat/deep/skipped.yml": `
name: load-skipped
check: { type: load, load5: { op: ">", value: 3 } }
then: { notify: [ops] }
`,
		"watches-recursive/deep/load.yml": `
name: load-recursive
check: { type: load, load5: { op: ">", value: 4 } }
then: { notify: [ops] }
`,
		"storages-flat/direct-mount.yml": `
name: direct-mount
check: { type: storage, path: /mnt/direct, mounted: true }
mount: {}
`,
		"storages-flat/deep/skipped-mount.yml": `
name: skipped-mount
check: { type: storage, path: /mnt/skipped, mounted: true }
mount: {}
`,
		"storages-recursive/deep/recursive-mount.yml": `
name: recursive-mount
check: { type: storage, path: /mnt/recursive, mounted: true }
mount: {}
`,
	})

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	for _, name := range []string{"direct-service", "recursive-service"} {
		if _, ok := cfg.Services[name]; !ok {
			t.Fatalf("service %q was not loaded", name)
		}
	}
	if _, ok := cfg.Services["skipped-service"]; ok {
		t.Fatalf("non-recursive services path loaded nested service")
	}
	for _, name := range []string{"direct-app", "recursive-app"} {
		if _, ok := cfg.Apps[name]; !ok {
			t.Fatalf("app %q was not loaded", name)
		}
	}
	if _, ok := cfg.Apps["skipped-app"]; ok {
		t.Fatalf("non-recursive apps path loaded nested app")
	}
	notifiers := cfg.Notifiers()
	for _, name := range []string{"ops", "team"} {
		if _, ok := notifiers[name]; !ok {
			t.Fatalf("notifier %q was not loaded: %v", name, notifiers)
		}
	}
	if _, ok := notifiers["skipped-notifier"]; ok {
		t.Fatalf("non-recursive notifiers path loaded nested notifier")
	}
	watches, errs := cfg.ResolveWatches()
	if len(errs) != 0 {
		t.Fatalf("ResolveWatches() errors: %v", errs)
	}
	for _, name := range []string{"storage-direct", "storage-recursive", "network-direct", "network-recursive", "load-direct", "load-recursive", "direct-mount", "recursive-mount"} {
		if _, ok := watches[name]; !ok {
			t.Fatalf("watch %q was not loaded: %v", name, watches)
		}
	}
	if got := watches["network-direct"].(map[string]any)["category"]; got != "network" {
		t.Fatalf("included network watch category = %v, want network", got)
	}
	for _, name := range []string{"storage-skipped", "network-skipped", "load-skipped", "skipped-mount"} {
		if _, ok := watches[name]; ok {
			t.Fatalf("non-recursive watch path loaded nested watch %q", name)
		}
	}
	for _, name := range []string{"direct-mount", "recursive-mount"} {
		if !slices.Contains(cfg.StorageMountNames(), name) {
			t.Fatalf("mount-capable storage %q was not loaded", name)
		}
	}
	if slices.Contains(cfg.StorageMountNames(), "skipped-mount") {
		t.Fatalf("non-recursive watch path loaded nested mount-capable storage")
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("recursive path config should validate, got %v", issues)
	}
}

func TestLoadRelativeConfigPathResolvesDirsAbsolute(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "conf", "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "conf", "sermo.yml"), []byte(`
paths:
  services: [services]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "conf", "services", "web.yml"), []byte(`
name: web
service: web
`), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(root)
	cfg, err := loadConfig(t, filepath.Join("conf", "sermo.yml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := filepath.Join(root, "conf", "services")
	if got := cfg.Global.ServicePaths; len(got) != 1 || got[0].Path != want {
		t.Fatalf("Global.ServicePaths = %v, want path %s", got, want)
	}
	if _, ok := cfg.Services["web"]; !ok {
		t.Fatalf("relative service directory was not loaded: %v", cfg.ServiceNames)
	}
}

// Included documents the loader must reject outright, before validation: a
// duplicate definition or a malformed fragment aborts Load with the message in
// want. One row per rejection; they share this table because they are one
// concern, include-time rejection, not merely one helper.
func TestLoadIncludedDocumentErrors(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name: "storage watch duplicated by an included document",
			files: map[string]string{
				"sermo.yml": `
paths:
  watches: [ @ROOT@/storages ]
watches:
  storage-root:
    check: { type: storage, path: /, used_pct: { op: ">=", value: 90 } }
    then:
      hook: { command: [/bin/true] }
`,
				"storages/storage-root.yml": `
name: storage-root
check:
  type: storage
  path: /
  used_pct: { op: ">=", value: 95 }
then:
  hook: { command: [/bin/true] }
`,
			},
			want: `watch "storage-root" is already defined`,
		},
		{
			name: "watch document duplicated in one directory",
			files: map[string]string{
				"sermo.yml": `
paths:
  watches: [ @ROOT@/watches ]
defaults:
  policy: { cooldown: 5m }
watches:
  load:
    check: { type: load, load5: { op: ">", value: 2 } }
`,
				"watches/load.yml": `
name: load
check: { type: load, load5: { op: ">", value: 3 } }
`,
			},
			want: `watch "load" is already defined`,
		},
		{
			name: "watch document duplicated across watch directories",
			files: map[string]string{
				"sermo.yml": `
paths:
  watches: [ @ROOT@/networks, @ROOT@/watches ]
defaults:
  policy: { cooldown: 5m }
`,
				"networks/ping-gw.yml": `
name: ping-gw
category: network
check: { type: icmp, host: 192.0.2.1 }
`,
				"watches/ping-gw.yml": `
name: ping-gw
category: host
check: { type: load, load5: { op: ">", value: 3 } }
`,
			},
			want: `watch "ping-gw" is already defined`,
		},
		{
			name: "watch document without a name",
			files: map[string]string{
				"sermo.yml": `
paths:
  watches: [ @ROOT@/watches ]
defaults:
  policy: { cooldown: 5m }
`,
				"watches/load.yml": `
check: { type: load, load5: { op: ">", value: 3 } }
`,
			},
			want: "watch documents must define name",
		},
		{
			name: "notifier fragment duplicating a notifier",
			files: map[string]string{
				"sermo.yml": `
paths:
  notifiers: [ @ROOT@/notifiers ]
defaults:
  policy: { cooldown: 5m }
notifiers:
  ops:
    enabled: false
    type: email
`,
				"notifiers/ops.yml": `
notifiers:
  ops:
    enabled: false
    type: email
`,
			},
			want: `notifier "ops" is already defined`,
		},
		{
			name: "notifier fragment with more than one entry",
			files: map[string]string{
				"sermo.yml": `
paths:
  notifiers: [ @ROOT@/notifiers ]
defaults:
  policy: { cooldown: 5m }
`,
				"notifiers/multi.yml": `
notifiers:
  ops:
    enabled: false
    type: email
  pager:
    enabled: false
    type: email
`,
			},
			want: "notifiers fragments must contain exactly one entry",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertLoadError(t, tt.files, tt.want)
		})
	}
}

func TestLoadIncludedWatchDocumentRejectsGroupedWatchesMap(t *testing.T) {
	for _, tc := range []struct {
		name string
		dir  string
	}{
		{name: "watch-dir", dir: "watches"},
		{name: "network-dir", dir: "networks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			global := writeConfig(t, map[string]string{
				"sermo.yml": `
paths:
  watches: [ @ROOT@/` + tc.dir + ` ]
defaults:
  policy: { cooldown: 5m }
`,
				tc.dir + "/load.yml": `
watches:
  load:
    check: { type: load, load5: { op: ">", value: 3 } }
`,
			})

			if _, err := loadConfig(t, global); err == nil || !strings.Contains(err.Error(), "watch documents use top-level name/check fields, not a watches map") {
				t.Fatalf("Load() error = %v, want grouped watches map rejection", err)
			}
		})
	}
}

func TestLoadIncludedWatchDocumentRejectsInvalidMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "wrong-kind",
			body: `
kind: service
name: load
check: { type: load, load5: { op: ">", value: 3 } }
`,
			want: "located under a watches directory but declares kind: service",
		},
		{
			name: "path-name",
			body: `
name: "../load"
check: { type: load, load5: { op: ">", value: 3 } }
`,
			want: `watch name "../load" must be a simple name without path separators`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertLoadDirError(t, "watches", "watches/load.yml", tc.body, tc.want)
		})
	}
}

func TestStorageWatchMountedFieldIsPreserved(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  watches: [ @ROOT@/storages ]
`,
		"storages/backup.yml": `
name: storage-backup
check:
  type: storage
  path: /mnt/backup
  mounted: true
  used_pct: { op: ">=", value: 90 }
mount:
  refcount: true
`,
		"storages/archive.yml": `
name: storage-archive
check:
  type: storage
  path: /mnt/archive
  mounted: false
mount:
  refcount: true
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	watches, errs := cfg.ResolveWatches()
	if len(errs) != 0 {
		t.Fatalf("ResolveWatches() errors: %v", errs)
	}
	backup := watches["storage-backup"].(map[string]any)["check"].(map[string]any)
	if backup["mounted"] != true {
		t.Fatalf("storage-backup mounted = %v, want true", backup["mounted"])
	}
	archive := watches["storage-archive"].(map[string]any)["check"].(map[string]any)
	if archive["mounted"] != false {
		t.Fatalf("storage-archive mounted = %v, want explicit false", archive["mounted"])
	}
}

func TestStorageWatchKeepsMetadata(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  watches: [ @ROOT@/storages ]
defaults:
  policy: { cooldown: 5m }
`,
		"storages/root.yml": `
name: storage-root
display_name: Root filesystem
description: System volume
category: storage
check:
  type: storage
  path: /
  used_pct: { op: ">=", value: 90 }
`,
	})

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	watches, errs := cfg.ResolveWatches()
	if len(errs) != 0 {
		t.Fatalf("ResolveWatches() errors = %v", errs)
	}
	entry, ok := watches["storage-root"].(map[string]any)
	if !ok {
		t.Fatalf("storage-root watch = %v, want mapping", watches["storage-root"])
	}
	if got := cfgval.String(entry["display_name"]); got != "Root filesystem" {
		t.Fatalf("display_name = %q, want Root filesystem", got)
	}
	if got := cfgval.String(entry["description"]); got != "System volume" {
		t.Fatalf("description = %q, want System volume", got)
	}
	if got := cfgval.String(entry["category"]); got != "storage" {
		t.Fatalf("category = %q, want storage", got)
	}
}

func TestLoadIncludedNotifierFragments(t *testing.T) {
	t.Setenv("SMTP_DSN", "smtp://user:pw@mail.example.com:587")
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  notifiers: [ @ROOT@/notifiers ]
  watches: [ @ROOT@/storages ]
defaults:
  policy: { cooldown: 5m }
notify: [ops]
`,
		"notifiers/ops.yml": `
notifiers:
  ops:
    type: email
    dsn: "${env:SMTP_DSN}"
    from: "Sermo <sermo@example.com>"
    to: [ops@example.com]
`,
		"storages/storage-root.yml": `
name: storage-root
check:
  type: storage
  path: /
  used_pct: { op: ">=", value: "90%" }
then:
  notify: [ops]
`,
	})

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	notifier := cfg.Notifiers()["ops"].(map[string]any)
	if notifier["dsn"] != "smtp://user:pw@mail.example.com:587" {
		t.Fatalf("included notifier env not expanded: %v", notifier["dsn"])
	}
	watches, errs := cfg.ResolveWatches()
	if len(errs) != 0 {
		t.Fatalf("ResolveWatches() errors: %v", errs)
	}
	if _, ok := watches["storage-root"]; !ok {
		t.Fatalf("storage watch not loaded: %v", watches)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("included notifier/watch config should validate, got %v", issues)
	}
}

func TestLoadIncludedNotifierFragmentRejectsInvalidShape(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "not-mapping",
			body: `
notifiers: [ops]
`,
			want: "notifiers must be a mapping",
		},
		{
			name: "extra-top-level-key",
			body: `
notifiers:
  ops:
    enabled: false
    type: email
notify: [ops]
`,
			want: `notifiers fragments only support top-level notifiers, got "notify"`,
		},
		{
			name: "document-shape",
			body: `
name: ops-email
type: email
`,
			want: "notifiers config directories only support top-level notifiers",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertLoadDirError(t, "notifiers", "notifiers/ops.yml", tc.body, tc.want)
		})
	}
}

func TestLoadExplicitTargetDirectories(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  services: [ @ROOT@/services ]
  notifiers: [ @ROOT@/notifiers ]
  watches: [ @ROOT@/storages, @ROOT@/networks, @ROOT@/watches ]
defaults:
  policy: { cooldown: 5m }
notify: [ops]
`,
		"services/web.yml": `
name: web
service: web
checks:
  service: { type: service, expect: active }
`,
		"notifiers/ops.yml": `
notifiers:
  ops:
    enabled: false
    type: email
`,
		"storages/root.yml": `
name: storage-root
check:
  type: storage
  path: /
  used_pct: { op: ">=", value: "90%" }
then: { notify: [ops] }
`,
		"storages/backup.yml": `
name: backup
check: { type: storage, path: /mnt/backup, mounted: true }
mount: {}
`,
		"networks/ping.yml": `
name: ping-gw
category: network
check: { type: icmp, host: 8.8.8.8 }
metrics:
  state:
    expect: up
    then: { notify: [ops] }
`,
		"watches/load.yml": `
name: load
category: host
check: { type: load, load5: { op: ">", value: 2 } }
then: { notify: [ops] }
`,
	})

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := cfg.Services["web"]; !ok {
		t.Fatalf("service directory was not loaded: %v", cfg.ServiceNames)
	}
	if _, ok := cfg.Notifiers()["ops"]; !ok {
		t.Fatalf("notifier directory was not loaded: %v", cfg.Notifiers())
	}
	watches, errs := cfg.ResolveWatches()
	if len(errs) != 0 {
		t.Fatalf("ResolveWatches() errors: %v", errs)
	}
	for _, name := range []string{"storage-root", "backup", "ping-gw", "load"} {
		if _, ok := watches[name]; !ok {
			t.Fatalf("watch %q was not loaded from explicit directories: %v", name, watches)
		}
	}
	if got := cfgval.String(watches["ping-gw"].(map[string]any)["category"]); got != "network" {
		t.Fatalf("included watch category = %q, want network", got)
	}
	if !slices.Contains(cfg.StorageMountNames(), "backup") {
		t.Fatalf("mount-capable storage directory was not loaded: %v", cfg.StorageMountNames())
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("explicit target directory config should validate, got %v", issues)
	}
}

func TestDisplayNameFallsBackToName(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"present", map[string]any{"display_name": "MariaDB"}, "MariaDB"},
		{"absent", map[string]any{}, "mariadb"},
		{"blank", map[string]any{"display_name": "   "}, "mariadb"},
		{"non-string", map[string]any{"display_name": 7}, "mariadb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DisplayName(tc.body, "mariadb"); got != tc.want {
				t.Errorf("DisplayName(%v) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

func TestCategoryLabelFallsBack(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"present", map[string]any{"category": "database"}, "database"},
		{"trimmed", map[string]any{"category": " database "}, "database"},
		{"no-inference-from-name", map[string]any{"name": "nginx"}, "service"},
		{"no-inference-from-display-name", map[string]any{"display_name": "MariaDB"}, "service"},
		{"absent", map[string]any{}, "service"},
		{"blank", map[string]any{"category": "   "}, "service"},
		{"non-string", map[string]any{"category": 7}, "service"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CategoryLabel(tc.body, "service"); got != tc.want {
				t.Errorf("CategoryLabel(%v) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

func TestCatalogCategoryFromDirectory(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml":                   baseGlobal,
		"catalog/services/nginx.yml":  "name: nginx\nservice: nginx\n",
		"catalog/apps/git.yml":        "name: git\nbinary: /usr/bin/git\n",
		"catalog/libs/glibc.yml":      "name: glibc\nbinary: /lib64/libc.so.6\n",
		"catalog/patterns/common.yml": "name: common\n",
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	cases := []struct {
		name, wantCat string
		reg           map[string]*Document
	}{
		{"nginx", CategoryService, cfg.CatalogServices},
		{"git", CategoryApp, cfg.Apps},
		{"glibc", CategoryLibrary, cfg.Libraries},
		{"common", CategoryPatterns, cfg.Patterns},
	}
	for _, tc := range cases {
		doc, ok := tc.reg[tc.name]
		if !ok {
			t.Fatalf("%q not loaded in its registry", tc.name)
		}
		if doc.Category != tc.wantCat {
			t.Errorf("%s category = %q, want %q", tc.name, doc.Category, tc.wantCat)
		}
	}
	if got := cfg.CatalogNamesInCategory(CategoryApp); len(got) != 1 || got[0] != "git" {
		t.Errorf("CatalogNamesInCategory(app) = %v, want [git]", got)
	}
}

func TestCatalogRootFilesRejected(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml":         baseGlobal,
		"catalog/nginx.yml": "name: nginx\nservice: nginx\n",
	})
	_, err := loadConfig(t, global)
	if err == nil || !strings.Contains(err.Error(), "catalog documents must live under services, apps, libs, or patterns") {
		t.Fatalf("Load() error = %v, want catalog root rejection", err)
	}
}
