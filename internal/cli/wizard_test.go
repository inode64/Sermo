package cli

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sermo/internal/assist"
	"sermo/internal/config"
)

func fakeWizardEnv(*config.Config) assist.Env {
	return assist.Env{
		Notifiers: []string{"ops-email"},
		Volumes: func() ([]assist.Volume, error) {
			return []assist.Volume{{Mountpoint: "/mnt/backup", FSType: "ext4", Device: "/dev/mapper/vg0-data"}}, nil
		},
		Ifaces: func() ([]assist.Iface, error) { return nil, nil },
	}
}

func dockerWizardEnv(*config.Config) assist.Env {
	return assist.Env{
		ServiceNames: map[string]struct{}{},
		DockerContainers: func() ([]assist.DockerCandidate, error) {
			return []assist.DockerCandidate{{
				Name:      "docker-web",
				Title:     "web",
				Container: "web",
				Status:    "running",
				Socket:    "/run/docker.sock",
			}}, nil
		},
	}
}

func mountWizardEnv(*config.Config) assist.Env {
	return assist.Env{
		Mounts: func() ([]assist.MountCandidate, error) {
			return []assist.MountCandidate{{
				Path:    "/mnt/backup",
				Source:  "UUID=backup",
				FSType:  "ext4",
				Mounted: false,
			}}, nil
		},
	}
}

func TestIfaceHasUsableAddress(t *testing.T) {
	addr := func(cidr string) net.Addr {
		ip, n, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		n.IP = ip
		return n
	}
	if ifaceHasUsableAddress([]net.Addr{addr("127.0.0.1/8"), addr("fe80::1/64")}) {
		t.Fatal("loopback/link-local addresses must not count as usable")
	}
	if !ifaceHasUsableAddress([]net.Addr{addr("192.168.2.254/24")}) {
		t.Fatal("private global-unicast IPv4 should count as usable")
	}
}

func TestRunWizardVolumeMergesConfig(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	if err := os.WriteFile(cfgPath, []byte("engine:\n  interval: 30s\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// volume assistant: select vol 1; monitor enabled; inherit interval; free<10;
	// for 3; notifier ops-email; no expand; no dry-run. then runWizard:
	// confirm merge with "y".
	script := strings.Join([]string{"1", "1", "", "1", "10", "3", "1", "n", "n", "y"}, "\n") + "\n"

	var out bytes.Buffer
	app := App{
		Stdin:         strings.NewReader(script),
		Stdout:        &out,
		Stderr:        &bytes.Buffer{},
		LoadConfig:    config.Load,
		wizardEnvFunc: fakeWizardEnv,
	}
	code := app.Run(context.Background(), []string{"--config", cfgPath, "wizard", "volume"})
	if code != exitSuccess {
		t.Fatalf("exit = %d, want success; out=%s", code, out.String())
	}

	// The generated block was printed.
	if !strings.Contains(out.String(), "storage-mnt-backup") || !strings.Contains(out.String(), "free_pct") {
		t.Fatalf("generated YAML not shown: %s", out.String())
	}
	// The global config only points paths.includes at the watch-type directory; the
	// watch itself is written as a separate enabled fragment.
	merged, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(merged), "storage-mnt-backup") {
		t.Fatalf("watch should not be in global config: %s", merged)
	}
	if !strings.Contains(string(merged), "includes:") || !strings.Contains(string(merged), "storage") {
		t.Fatalf("paths.includes not updated: %s", merged)
	}
	if !strings.Contains(string(merged), "interval: 30s") {
		t.Fatalf("merge dropped existing config: %s", merged)
	}
	watchPath := filepath.Join(tmp, "storage", "storage-mnt-backup.yml")
	watchFile, err := os.ReadFile(watchPath)
	if err != nil {
		t.Fatalf("watch file not written: %v", err)
	}
	if !strings.Contains(string(watchFile), "watches:") || !strings.Contains(string(watchFile), "storage-mnt-backup") || !strings.Contains(string(watchFile), "free_pct") {
		t.Fatalf("watch fragment wrong: %s", watchFile)
	}
	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load merged config: %v", err)
	}
	watches, _ := loaded.Global.Raw["watches"].(map[string]any)
	if _, ok := watches["storage-mnt-backup"]; !ok {
		t.Fatalf("loaded config did not include generated watch: %v", watches)
	}
	bak, err := os.ReadFile(cfgPath + ".bak")
	if err != nil {
		t.Fatalf("backup not written: %v", err)
	}
	if strings.Contains(string(bak), "paths:") || strings.Contains(string(bak), "watches:") {
		t.Fatalf("backup should be the original (pre-merge): %s", bak)
	}
}

func TestRunWizardDockerWritesService(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	if err := os.WriteFile(cfgPath, []byte("engine:\n  interval: 30s\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	script := strings.Join([]string{
		"1", // select docker-web
		"1", // monitor enabled
		"",  // interval inherit
		"n", // no shadow
		"y", // write service file
	}, "\n") + "\n"

	var out bytes.Buffer
	app := App{
		Stdin:         strings.NewReader(script),
		Stdout:        &out,
		Stderr:        &bytes.Buffer{},
		LoadConfig:    config.Load,
		wizardEnvFunc: dockerWizardEnv,
	}
	code := app.Run(context.Background(), []string{"--config", cfgPath, "wizard", "docker"})
	if code != exitSuccess {
		t.Fatalf("exit = %d, want success; out=%s", code, out.String())
	}
	servicePath := filepath.Join(tmp, servicesIncludeDir, "docker-web.yml")
	data, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatalf("service file not written: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"kind: service",
		"name: docker-web",
		"type: docker",
		"container: web",
		"socket: /run/docker.sock",
		"container.status",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("service file missing %q:\n%s", want, text)
		}
	}
	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load merged config: %v", err)
	}
	if _, ok := loaded.Services["docker-web"]; !ok {
		t.Fatalf("loaded config did not include docker-web: %v", loaded.ServiceNames)
	}
}

func TestRunWizardMountWritesMountUnit(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	if err := os.WriteFile(cfgPath, []byte("engine:\n  interval: 30s\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	script := strings.Join([]string{
		"1", // select /mnt/backup
		"y", // use refcounting
		"y", // write mount file
	}, "\n") + "\n"

	var out bytes.Buffer
	app := App{
		Stdin:         strings.NewReader(script),
		Stdout:        &out,
		Stderr:        &bytes.Buffer{},
		LoadConfig:    config.Load,
		wizardEnvFunc: mountWizardEnv,
	}
	code := app.Run(context.Background(), []string{"--config", cfgPath, "wizard", "mount"})
	if code != exitSuccess {
		t.Fatalf("exit = %d, want success; out=%s", code, out.String())
	}
	mountPath := filepath.Join(tmp, mountsConfigDir, "mount-mnt-backup.yml")
	data, err := os.ReadFile(mountPath)
	if err != nil {
		t.Fatalf("mount file not written: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"kind: mount",
		"name: mount-mnt-backup",
		"path: /mnt/backup",
		"refcount: true",
		"allow_sigkill: false",
		"allow_lazy: false",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("mount file missing %q:\n%s", want, text)
		}
	}
	merged, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(merged), "mounts:") {
		t.Fatalf("paths.mounts not updated: %s", merged)
	}
	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load merged config: %v", err)
	}
	if _, ok := loaded.Mounts["mount-mnt-backup"]; !ok {
		t.Fatalf("loaded config did not include mount-mnt-backup: %v", loaded.MountNames)
	}
}

func TestWriteMountFilesRejectsExistingFileBeforeUpdatingConfig(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	original := []byte("engine:\n  interval: 30s\n")
	if err := os.WriteFile(cfgPath, original, 0o644); err != nil {
		t.Fatal(err)
	}
	mountDir := filepath.Join(tmp, mountsConfigDir)
	if err := os.Mkdir(mountDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(mountDir, "mount-mnt-backup.yml")
	if err := os.WriteFile(existing, []byte("kind: mount\nname: old\npath: /old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := writeMountFiles(cfgPath, map[string]map[string]any{
		"mount-mnt-backup": {
			"kind": "mount",
			"name": "mount-mnt-backup",
			"path": "/mnt/backup",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("writeMountFiles error = %v, want existing-file error", err)
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("global config changed after rejected mount write:\n%s", after)
	}
	if _, err := os.Stat(cfgPath + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("backup should not be written when mount file preflight fails, stat err=%v", err)
	}
}

func TestRunWizardUnknownAssistant(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	_ = os.WriteFile(cfgPath, []byte("engine: {}\n"), 0o644)
	app := App{
		Stdin:         strings.NewReader(""),
		Stdout:        &bytes.Buffer{},
		Stderr:        &bytes.Buffer{},
		LoadConfig:    config.Load,
		wizardEnvFunc: fakeWizardEnv,
	}
	if code := app.Run(context.Background(), []string{"--config", cfgPath, "wizard", "nope"}); code != exitUsage {
		t.Fatalf("unknown assistant exit = %d, want usage error", code)
	}
}

func TestWizardRejectsLoadedWatchCollision(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	if err := os.WriteFile(cfgPath, []byte("watches:\n  storage-root: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureNoWatchCollisions(cfg, map[string]any{"storage-root": map[string]any{}}); err == nil {
		t.Fatal("merging a watch that already exists must error")
	}
}

func TestMergeWizardWatchesRejectsExistingFile(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	if err := os.WriteFile(cfgPath, []byte("paths:\n  includes: [storage]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(tmp, "storage"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "storage", "storage-root.yml"), []byte("watches: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := mergeWizardWatches(cfgPath, "volume", map[string]any{"storage-root": map[string]any{"check": map[string]any{"type": "storage"}}}); err == nil {
		t.Fatal("existing watch file must not be overwritten")
	}
}

func TestMergeWizardWatchesMigratesLegacyEnabledPath(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	if err := os.WriteFile(cfgPath, []byte("paths:\n  enabled: [apps]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	merged, err := mergeWizardWatches(cfgPath, "volume", map[string]any{"storage-root": map[string]any{"check": map[string]any{"type": "storage"}, "then": map[string]any{"notify": []any{"ops"}}}})
	if err != nil {
		t.Fatalf("mergeWizardWatches: %v", err)
	}
	if merged.Backup == "" {
		t.Fatal("legacy enabled path migration should rewrite global config")
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "enabled:") || !strings.Contains(string(data), "includes:") || !strings.Contains(string(data), "apps") || !strings.Contains(string(data), "storage") {
		t.Fatalf("legacy path not migrated to includes: %s", data)
	}
}

func TestWizardConfigDirNameUsesWatchType(t *testing.T) {
	tests := []struct {
		name     string
		wizard   string
		fragment map[string]any
		want     string
	}{
		{
			name:   "volume assistant writes storage directory",
			wizard: "volume",
			fragment: map[string]any{
				"storage-root": map[string]any{"check": map[string]any{"type": "storage"}},
			},
			want: "storage",
		},
		{
			name:   "net assistant writes network directory",
			wizard: "net",
			fragment: map[string]any{
				"net-eth0": map[string]any{"check": map[string]any{"type": "net"}},
			},
			want: "network",
		},
		{
			name:   "missing type falls back to wizard token",
			wizard: "custom wizard",
			fragment: map[string]any{
				"watch": map[string]any{},
			},
			want: "custom-wizard",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wizardConfigDirName(tt.wizard, tt.fragment); got != tt.want {
				t.Fatalf("wizardConfigDirName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWizardCleanupDirsIncludesLegacyAssistantDir(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	tests := []struct {
		name     string
		wizard   string
		fragment map[string]any
		want     []string
	}{
		{
			name:   "volume checks storage and legacy volume",
			wizard: "volume",
			fragment: map[string]any{
				"storage-root": map[string]any{"check": map[string]any{"type": "storage"}},
			},
			want: []string{filepath.Join(tmp, "storage"), filepath.Join(tmp, "volume")},
		},
		{
			name:   "net checks network and legacy net",
			wizard: "net",
			fragment: map[string]any{
				"net-eth0": map[string]any{"check": map[string]any{"type": "net"}},
			},
			want: []string{filepath.Join(tmp, "network"), filepath.Join(tmp, "net")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wizardCleanupDirs(cfgPath, tt.wizard, tt.fragment)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Fatalf("wizardCleanupDirs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRunWizardVolumeCanDeleteExistingWatchFilesIndividually(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	storageDir := filepath.Join(tmp, "storage")
	if err := os.Mkdir(storageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("paths:\n  includes: [storage]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An existing managed watch for /old — a mountpoint the env no longer detects
	// (fakeWizardEnv only reports /mnt/backup), so it is offered as stale.
	oldFile := filepath.Join(storageDir, "storage-old.yml")
	if err := os.WriteFile(oldFile, []byte("watches:\n  storage-old:\n    check: { type: storage, path: /old, free_pct: { op: \"<\", value: 5 } }\n    then: { notify: [ops-email] }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// volume assistant answers (monitor enabled, inherit interval), then: confirm
	// merge, review stale files, delete the orphaned /old file.
	script := strings.Join([]string{"1", "1", "", "1", "10", "3", "1", "n", "n", "y", "y", "y"}, "\n") + "\n"

	var out bytes.Buffer
	app := App{
		Stdin:         strings.NewReader(script),
		Stdout:        &out,
		Stderr:        &bytes.Buffer{},
		LoadConfig:    config.Load,
		wizardEnvFunc: fakeWizardEnv,
	}
	code := app.Run(context.Background(), []string{"--config", cfgPath, "wizard", "volume"})
	if code != exitSuccess {
		t.Fatalf("exit = %d, want success; out=%s", code, out.String())
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatalf("old watch file should be deleted, stat err=%v", err)
	}
	newFile := filepath.Join(storageDir, "storage-mnt-backup.yml")
	if _, err := os.Stat(newFile); err != nil {
		t.Fatalf("new watch file not written: %v", err)
	}
	if !strings.Contains(out.String(), "Deleted 1 existing watch file(s)") {
		t.Fatalf("delete summary not shown: %s", out.String())
	}
}

func TestTargetsStale(t *testing.T) {
	detected := map[string]bool{"/mnt/backup": true, "eth0": true}
	cases := []struct {
		name     string
		targets  []string
		detected map[string]bool
		want     bool
	}{
		{"orphaned target", []string{"/old"}, detected, true},
		{"still detected", []string{"/mnt/backup"}, detected, false},
		{"mixed keeps the file", []string{"/old", "eth0"}, detected, false},
		{"no targets is never stale", nil, detected, false},
		{"no detection never deletes", []string{"/old"}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := targetsStale(tc.targets, tc.detected); got != tc.want {
				t.Fatalf("targetsStale(%v) = %v, want %v", tc.targets, got, tc.want)
			}
		})
	}
}

func TestPlanStaleMountDeletes(t *testing.T) {
	dir := t.TempDir()
	oldFile := filepath.Join(dir, "old.yml")
	if err := os.WriteFile(oldFile, []byte("kind: mount\nname: mount-old\npath: /old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	currentFile := filepath.Join(dir, "current.yml")
	if err := os.WriteFile(currentFile, []byte("kind: mount\nname: mount-current\npath: /mnt/current\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := assist.NewPrompt(strings.NewReader("y\ny\n"), &strings.Builder{})
	deletes, err := planStaleMountDeletes(p, dir, map[string]bool{"/mnt/current": true})
	if err != nil {
		t.Fatalf("planStaleMountDeletes: %v", err)
	}
	if len(deletes) != 1 || deletes[0] != oldFile {
		t.Fatalf("deletes = %v, want [%s]", deletes, oldFile)
	}
}

func TestParseWatchFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "w.yml")
	body := "watches:\n" +
		"  storage-data:\n    check: { type: storage, path: /data }\n" +
		"  net-eth0:\n    check: { type: net, interface: eth0 }\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	names, targets := parseWatchFile(path)
	if strings.Join(names, ",") != "net-eth0,storage-data" {
		t.Fatalf("names = %v, want sorted [net-eth0 storage-data]", names)
	}
	gotTargets := map[string]bool{}
	for _, x := range targets {
		gotTargets[x] = true
	}
	if !gotTargets["/data"] || !gotTargets["eth0"] || len(targets) != 2 {
		t.Fatalf("targets = %v, want /data and eth0", targets)
	}
	// A missing file yields no names/targets rather than erroring.
	if n, tg := parseWatchFile(filepath.Join(dir, "absent.yml")); n != nil || tg != nil {
		t.Fatalf("missing file = (%v, %v), want (nil, nil)", n, tg)
	}
}

func TestConfirmStaleDeletes(t *testing.T) {
	stale := []staleFile{{path: "/a.yml", label: "/a.yml (a)"}, {path: "/b.yml", label: "/b.yml (b)"}}

	// Declining the review deletes nothing.
	p := assist.NewPrompt(strings.NewReader("n\n"), &strings.Builder{})
	if got := confirmStaleDeletes(p, "/dir", "watch", stale); got != nil {
		t.Fatalf("declining review must delete nothing, got %v", got)
	}

	// Review yes; delete the first, keep the second.
	p = assist.NewPrompt(strings.NewReader("y\ny\nn\n"), &strings.Builder{})
	if got := confirmStaleDeletes(p, "/dir", "watch", stale); len(got) != 1 || got[0] != "/a.yml" {
		t.Fatalf("got %v, want [/a.yml]", got)
	}

	// No stale files: no prompt is issued (safe to pass a nil prompt), nil result.
	if got := confirmStaleDeletes(nil, "/dir", "service", nil); got != nil {
		t.Fatalf("no stale files must return nil, got %v", got)
	}
}

func TestRunWizardAbortsOnTruncatedInput(t *testing.T) {
	// A truncated pipe used to spin the re-prompt loop forever at 100% CPU;
	// now the wizard must abort cleanly with a usage error. The test itself is
	// the regression guard: a hang here fails on the test timeout.
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	if err := os.WriteFile(cfgPath, []byte("engine:\n  interval: 30s\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	app := App{
		// One unusable answer for the volume menu, then EOF.
		Stdin:         strings.NewReader("zzz\n"),
		Stdout:        &out,
		Stderr:        &errOut,
		LoadConfig:    config.Load,
		wizardEnvFunc: fakeWizardEnv,
	}
	code := app.Run(context.Background(), []string{"--config", cfgPath, "wizard", "volume"})
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d (usage); out=%s err=%s", code, exitUsage, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "wizard aborted") {
		t.Fatalf("stderr = %q, want a wizard-aborted message", errOut.String())
	}
}
