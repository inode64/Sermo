package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sermo/internal/assist"
	"sermo/internal/cfgval"
	"sermo/internal/config"
	"sermo/internal/dockerctl"
	"sermo/internal/servicemgr"
)

func TestParseProcSocketTableTCPListen(t *testing.T) {
	const table = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
   1: 0100007F:1FBB 00000000:0000 01 00000000:00000000 00:00000000 00000000     0        0 12346 1 0000000000000000 100 0 0 10 0
`
	ok, err := parseProcSocketTable(strings.NewReader(table), 80, map[string]bool{"0A": true})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("TCP LISTEN port 80 should be detected")
	}
	ok, err = parseProcSocketTable(strings.NewReader(table), 8123, map[string]bool{"0A": true})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("established TCP socket must not count as listening")
	}
}

func TestParseProcSocketTableUDP(t *testing.T) {
	const table = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
 1576: 00000000:0043 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 37159 2 0000000000000000 0
`
	ok, err := parseProcSocketTable(strings.NewReader(table), 67, map[string]bool{"07": true})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("UDP port 67 should be detected")
	}
}

// assertParseHosts parses table with parseProcSocketTableHosts (port 9104, state
// 0A) and asserts the extracted hosts equal want.
func assertParseHosts(t *testing.T, table string, ipv6 bool, want ...string) {
	t.Helper()
	got, err := parseProcSocketTableHosts(strings.NewReader(table), 9104, map[string]bool{"0A": true}, ipv6)
	if err != nil {
		t.Fatal(err)
	}
	assertStringsEqual(t, got, want)
}

func TestParseProcSocketTableHostsIPv4(t *testing.T) {
	const table = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 160200C0:2390 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
   1: 0100007F:2390 00000000:0000 01 00000000:00000000 00:00000000 00000000     0        0 12346 1 0000000000000000 100 0 0 10 0
`
	assertParseHosts(t, table, false, "192.0.2.22")
}

func TestParseProcSocketTableHostsIPv6(t *testing.T) {
	const table = `  sl  local_address                         rem_address                         st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:2390 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
`
	assertParseHosts(t, table, true, "::1")
}

func TestSpecificListenerHostRequiresOneNonLoopbackAddress(t *testing.T) {
	tests := []struct {
		name  string
		hosts []string
		want  string
		ok    bool
	}{
		{name: "specific", hosts: []string{"192.0.2.22"}, want: "192.0.2.22", ok: true},
		{name: "loopback", hosts: []string{"127.0.0.1", "::1"}},
		{name: "wildcard", hosts: []string{"0.0.0.0", "::"}},
		{name: "ambiguous", hosts: []string{"192.0.2.22", "198.51.100.22"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := specificListenerHost(tc.hosts)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("specificListenerHost() = %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestMergeCandidateVariablesPreservesDetectedValues(t *testing.T) {
	c := assist.ServiceCandidate{Variables: map[string]any{config.VariableKeyPort: 3300}}
	mergeCandidateVariables(&c, map[string]any{config.VariableKeyHost: "192.0.2.22"})
	if c.Variables[config.VariableKeyPort] != 3300 || c.Variables[config.VariableKeyHost] != "192.0.2.22" {
		t.Fatalf("variables = %#v, want existing port and detected host", c.Variables)
	}
}

func TestDaemonHasVariable(t *testing.T) {
	tree := map[string]any{config.SectionVariables: map[string]any{config.VariableKeyHost: "127.0.0.1"}}
	if !serviceHasVariable(tree, config.VariableKeyHost) {
		t.Fatal("serviceHasVariable(host) = false, want true")
	}
	if serviceHasVariable(tree, config.VariableKeyPort) {
		t.Fatal("serviceHasVariable(port) = true, want false")
	}
}

func TestResolveWizardServiceUnitPrefersActiveCandidate(t *testing.T) {
	resolver := servicemgr.UnitResolver{Probe: wizardProbe{paths: map[string]bool{
		"/etc/init.d/php-fpm": true,
		"/etc/init.d/php8.2":  true,
	}}}
	unit, status, err := resolveWizardServiceUnit(
		context.Background(),
		resolver,
		wizardManager{statuses: map[string]servicemgr.Status{"php-fpm": servicemgr.StatusInactive, "php8.2": servicemgr.StatusActive}},
		servicemgr.BackendOpenRC,
		[]string{"php-fpm", "php8.2"},
	)
	if err != nil {
		t.Fatalf("resolveWizardServiceUnit: %v", err)
	}
	if unit != "php8.2" || status != servicemgr.StatusActive {
		t.Fatalf("unit/status = %s/%s, want php8.2/active", unit, status)
	}
}

func TestParseSystemdActiveUnits(t *testing.T) {
	const stdout = `nginx.service loaded active running A high performance web server
sshd.service loaded active running OpenSSH server daemon
dbus.socket loaded active running D-Bus System Message Bus Socket
`
	got := servicemgr.ParseSystemdActiveUnits(stdout)
	want := []string{"nginx.service", "sshd.service"}
	assertStringsEqual(t, got, want)
}

func TestParseOpenRCActiveUnits(t *testing.T) {
	const stdout = `Runlevel: sysinit
 dmesg                                                             [  started  ]
 sysfs                                                             [  started  ]
Runlevel: boot
 localmount                                                        [  started  ]
Runlevel: default
 bluetooth                                                         [  started  ]
 mqtt-exporter                                                     [  crashed  ]
Dynamic Runlevel: needed/wanted
 rpcbind                                                           [  started  ]
Dynamic Runlevel: manual
 zigbee2mqtt                                                       [  started  ]
`
	got := servicemgr.ParseOpenRCActiveUnits(stdout)
	want := []string{"bluetooth", "rpcbind", "zigbee2mqtt"}
	assertStringsEqual(t, got, want)
}

// A service started in two matched runlevels, interleaved with other services,
// produces non-adjacent duplicates that slices.Compact would not collapse.
func TestParseOpenRCActiveUnitsDedupsAcrossRunlevels(t *testing.T) {
	const stdout = `Runlevel: default
 sshd                                                              [  started  ]
 cron                                                              [  started  ]
Dynamic Runlevel: manual
 sshd                                                              [  started  ]
`
	got := servicemgr.ParseOpenRCActiveUnits(stdout)
	want := []string{"sshd", "cron"}
	assertStringsEqual(t, got, want)
}

func TestDedupeWizardCatalogCandidatesByUnit(t *testing.T) {
	cands := []assist.ServiceCandidate{
		{Name: "mariadb", Unit: "mysql"},
		{Name: "mysql", Unit: "mysql"},
		{Name: "sshd", Unit: "sshd"},
		{Name: "customd", Unit: "mysql", Generic: true},
	}
	got := dedupeWizardCatalogCandidates(cands, servicemgr.BackendOpenRC)
	var names []string
	for _, c := range got {
		names = append(names, c.Name)
	}
	want := []string{"mariadb", "sshd", "customd"}
	assertStringsEqual(t, names, want)
}

func TestParseCephMonAddrsPrefersV2(t *testing.T) {
	host, port := parseCephMonAddrs("[v2:192.0.2.102:3300/0,v1:192.0.2.102:6789/0]")
	if host != "192.0.2.102" || port != 3300 {
		t.Fatalf("endpoint = %s:%d, want 192.0.2.102:3300", host, port)
	}
}

func TestParseCephMonAddrsFallsBackToV1(t *testing.T) {
	host, port := parseCephMonAddrs("[v1:192.0.2.102:6789/0]")
	if host != "192.0.2.102" || port != 6789 {
		t.Fatalf("endpoint = %s:%d, want 192.0.2.102:6789", host, port)
	}
}

func TestParseCephMonAddrsIPv6(t *testing.T) {
	host, port := parseCephMonAddrs("[v2:[fd00::102]:3300/0,v1:[fd00::102]:6789/0]")
	if host != "fd00::102" || port != 3300 {
		t.Fatalf("endpoint = %s:%d, want fd00::102:3300", host, port)
	}
}

func TestCephMonIDFromSystemdUnit(t *testing.T) {
	if got := cephMonID("ceph-mon@node1.service"); got != "node1" {
		t.Fatalf("cephMonID = %q, want node1", got)
	}
}

func TestServiceCleanupDirsUsesServicesDirOnly(t *testing.T) {
	tmp := t.TempDir()
	global := filepath.Join(tmp, "sermo.yml")

	got := serviceCleanupDirs(global, &config.Config{})
	want := []string{filepath.Join(tmp, servicesIncludeDir)}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("serviceCleanupDirs() = %v, want %v", got, want)
	}
}

func TestEnsureConfigPathListRecognizesAbsolutePathForRelativeTarget(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)
	root := map[string]any{
		"paths": map[string]any{
			"services": []any{filepath.Join(tmp, servicesIncludeDir)},
		},
	}

	changed, err := ensureConfigPathList(root, ".", "services", servicesIncludeDir, servicesIncludeDir)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("ensureConfigPathList changed config despite existing absolute path")
	}
	paths := root["paths"].(map[string]any)
	got, err := cfgval.StrictStringList(paths["services"])
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(tmp, servicesIncludeDir)}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("paths.services = %v, want %v", got, want)
	}
}

func TestDetectedServiceTargetKeysIncludeControlledServices(t *testing.T) {
	env := assist.Env{
		CatalogServices: func() ([]assist.ServiceCandidate, error) {
			return []assist.ServiceCandidate{{Name: "nginx"}}, nil
		},
		DockerContainers: func() ([]assist.DockerCandidate, error) {
			return []assist.DockerCandidate{{Name: "docker-web", Container: "web"}}, nil
		},
		VMs: func() ([]assist.VMCandidate, error) {
			return []assist.VMCandidate{{Name: "vm-web01", Domain: "web01"}}, nil
		},
	}
	keys := detectedTargetKeys(env, "service")
	for _, want := range []string{"service:nginx", "docker:web", "vm:web01"} {
		if !keys[want] {
			t.Fatalf("detected service keys = %v, missing %s", keys, want)
		}
	}
}

func TestServiceFileTargetControlledServices(t *testing.T) {
	tmp := t.TempDir()
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "docker",
			body: "name: docker-web\ncontrol: {type: docker, container: web}\n",
			want: "docker:web",
		},
		{
			name: "vm",
			body: "name: vm-web01\ncontrol: {type: libvirt, domain: web01}\n",
			want: "vm:web01",
		},
		{
			name: "catalog",
			body: "name: nginx-main\nuses: nginx\n",
			want: "service:nginx",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(tmp, tc.name+".yml")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := serviceFileTarget(path); got != tc.want {
				t.Fatalf("serviceFileTarget() = %q, want %q", got, tc.want)
			}
		})
	}
}

// assertWriteFilesRejectsExisting seeds subdir with an existing file, then asserts
// write refuses the collision ("already exists") without mutating the global
// config or leaving a .bak behind.
func assertWriteFilesRejectsExisting(t *testing.T, subdir, existingName, existingBody string, write func(string, map[string]map[string]any) (string, int, error), docs map[string]map[string]any) {
	t.Helper()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	original := []byte("engine:\n  interval: 30s\n")
	if err := os.WriteFile(cfgPath, original, 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tmp, subdir)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, existingName), []byte(existingBody), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := write(cfgPath, docs); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("write error = %v, want existing-file error", err)
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatalf("global config changed after rejected write:\n%s", after)
	}
	if _, err := os.Stat(cfgPath + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("backup should not be written when file preflight fails, stat err=%v", err)
	}
}

func TestWriteServiceFilesRejectsExistingFileBeforeUpdatingConfig(t *testing.T) {
	assertWriteFilesRejectsExisting(t, servicesIncludeDir, "docker-web.yml", "name: old\n", writeServiceFiles, map[string]map[string]any{
		"docker-web": {
			config.EntryKeyName: "docker-web",
			config.SectionControl: map[string]any{
				dockerctl.ControlKeyType:      dockerctl.ControlType,
				dockerctl.ControlKeyContainer: "web",
			},
		},
	})
}

func TestWizardManagedServiceName(t *testing.T) {
	if got := wizardManagedServiceName("docker", "/stack/web.1"); got != "docker-stack-web.1" {
		t.Fatalf("wizardManagedServiceName() = %q, want docker-stack-web.1", got)
	}
}

// assertStringsEqual fails the test unless got and want hold the same strings in
// the same order.
func assertStringsEqual(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

type wizardProbe struct {
	paths map[string]bool
}

func (p wizardProbe) CommandExists(string) bool { return false }
func (p wizardProbe) PathExists(path string) bool {
	return p.paths[path]
}
func (p wizardProbe) ReadFile(string) ([]byte, error) { return nil, wizardNotFoundError{} }

type wizardNotFoundError struct{}

func (wizardNotFoundError) Error() string { return "not found" }

type wizardManager struct {
	statuses map[string]servicemgr.Status
}

func (m wizardManager) Status(_ context.Context, service string) (servicemgr.ServiceStatus, error) {
	status := m.statuses[service]
	if status == "" {
		status = servicemgr.StatusUnknown
	}
	return servicemgr.ServiceStatus{Service: service, Backend: servicemgr.BackendOpenRC, Unit: service, Status: status}, nil
}
func (m wizardManager) Start(context.Context, string) error                  { return nil }
func (m wizardManager) Stop(context.Context, string) error                   { return nil }
func (m wizardManager) Restart(context.Context, string) error                { return nil }
func (m wizardManager) Reload(context.Context, string) error                 { return nil }
func (m wizardManager) SupportsReload(context.Context, string) (bool, error) { return false, nil }
func (m wizardManager) ResetState(context.Context, string) error             { return nil }
