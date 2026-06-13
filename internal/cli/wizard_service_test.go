package cli

import (
	"context"
	"strings"
	"testing"

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

func TestResolveWizardDaemonUnitPrefersActiveCandidate(t *testing.T) {
	resolver := servicemgr.UnitResolver{Probe: wizardProbe{paths: map[string]bool{
		"/etc/init.d/php-fpm": true,
		"/etc/init.d/php8.2":  true,
	}}}
	unit, status, err := resolveWizardDaemonUnit(
		context.Background(),
		resolver,
		wizardManager{statuses: map[string]servicemgr.Status{"php-fpm": servicemgr.StatusInactive, "php8.2": servicemgr.StatusActive}},
		servicemgr.BackendOpenRC,
		[]string{"php-fpm", "php8.2"},
	)
	if err != nil {
		t.Fatalf("resolveWizardDaemonUnit: %v", err)
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
	got := parseSystemdActiveUnits(stdout)
	want := []string{"nginx.service", "sshd.service"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("parseSystemdActiveUnits() = %v, want %v", got, want)
	}
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
	got := parseOpenRCActiveUnits(stdout)
	want := []string{"bluetooth", "rpcbind", "zigbee2mqtt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("parseOpenRCActiveUnits() = %v, want %v", got, want)
	}
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
	got := parseOpenRCActiveUnits(stdout)
	want := []string{"sshd", "cron"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("parseOpenRCActiveUnits() = %v, want %v (duplicates not collapsed)", got, want)
	}
}

type wizardProbe struct {
	paths map[string]bool
}

func (p wizardProbe) CommandExists(string) bool { return false }
func (p wizardProbe) PathExists(path string) bool {
	return p.paths[path]
}
func (p wizardProbe) ReadFile(string) ([]byte, error) { return nil, errNotFoundForWizard{} }

type errNotFoundForWizard struct{}

func (errNotFoundForWizard) Error() string { return "not found" }

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
