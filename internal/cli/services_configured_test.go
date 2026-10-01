package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sermoapp "sermo/internal/app"
	"sermo/internal/config"
	"sermo/internal/notify"
	"sermo/internal/servicemgr"
	"sermo/internal/web"
)

// lockedStatusRecorder records Status queries from the parallel local probes.
type lockedStatusRecorder struct {
	fakeManager
	mu    *sync.Mutex
	calls *[]string
}

func (m lockedStatusRecorder) Status(ctx context.Context, unit string) (servicemgr.ServiceStatus, error) {
	if m.calls != nil {
		m.mu.Lock()
		*m.calls = append(*m.calls, unit)
		m.mu.Unlock()
	}
	return m.fakeManager.Status(ctx, unit)
}

// configuredServicesTestApp answers every local status query as active through
// a fake init manager, so no test touches a real init system, Docker or libvirt.
func configuredServicesTestApp(load func(string, ...config.Option) (*config.Config, error), stdout, stderr *bytes.Buffer, statusCalls *[]string) App {
	mu := &sync.Mutex{}
	return App{
		Detector: fakeBackendDetector{detection: servicemgr.BackendSystemd},
		NewManager: func(servicemgr.Backend) (servicemgr.Manager, error) {
			return lockedStatusRecorder{status: servicemgr.ServiceStatus{Status: servicemgr.StatusActive}, mu: mu, calls: statusCalls}, nil
		},
		Env:        func(string) string { return "" },
		Stdout:     stdout,
		Stderr:     stderr,
		LoadConfig: load,
	}
}

func writeConfiguredServices(t *testing.T, servicesDir string) {
	t.Helper()
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(servicesDir, "web.yml"), "name: web\nservice: nginx\n")
	mustWrite(t, filepath.Join(servicesDir, "quiet.yml"), "name: quiet\nservice: quiet\nmonitor: disabled\n")
	// Disabled controlled services never reach their backend, so these stay
	// local-only even though no Docker or libvirt endpoint exists here.
	mustWrite(t, filepath.Join(servicesDir, "ctr.yml"), "name: ctr\nenabled: false\ncontrol: {type: docker, container: web}\n")
	mustWrite(t, filepath.Join(servicesDir, "vm.yml"), "name: vm\nenabled: false\ncontrol: {type: libvirt, domain: web01}\n")
}

func TestConfiguredServicesLocal(t *testing.T) {
	root, global := writeCatalogServiceConfig(t)
	servicesDir := filepath.Join(root, "services")
	if err := os.Remove(filepath.Join(servicesDir, "web.yml")); err != nil {
		t.Fatal(err)
	}
	writeConfiguredServices(t, servicesDir)

	var stdout, stderr bytes.Buffer
	var statusCalls []string
	app := configuredServicesTestApp(testLoadConfigWithCatalog(filepath.Join(root, "catalog")), &stdout, &stderr, &statusCalls)
	if code := app.Run(context.Background(), []string{"--config", global, "services"}); code != exitSuccess {
		t.Fatalf("services exit = %d, stderr = %s", code, stderr.String())
	}
	rows := map[string][]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(stdout.String()), "\n") {
		fields := strings.Fields(line)
		rows[fields[0]] = fields
	}
	want := map[string][]string{
		"SERVICE": {"SERVICE", "TYPE", "STATE", "MONITORED"},
		"web":     {"web", "systemd", localServiceState(servicemgr.ServiceStatus{Status: servicemgr.StatusActive}, monitorView{Configured: true, Enabled: true}), "yes"},
		"quiet":   {"quiet", "systemd", sermoapp.TargetStateStarted, "no"},
		"ctr":     {"ctr", "docker", sermoapp.TargetStateDisabled, "no"},
		"vm":      {"vm", "libvirt", sermoapp.TargetStateDisabled, "no"},
	}
	if len(rows) != len(want) {
		t.Fatalf("services output:\n%s", stdout.String())
	}
	for name, fields := range want {
		if strings.Join(rows[name], " ") != strings.Join(fields, " ") {
			t.Errorf("row %s = %v, want %v\n%s", name, rows[name], fields, stdout.String())
		}
	}
	slices.Sort(statusCalls)
	if strings.Join(statusCalls, ",") != "nginx.service,quiet.service" {
		t.Errorf("status queries = %v, want only the enabled init services", statusCalls)
	}

	stdout.Reset()
	if code := app.Run(context.Background(), []string{"--config", global, "--json", "services"}); code != exitSuccess {
		t.Fatalf("services --json exit = %d", code)
	}
	var payload struct {
		Services []configuredService `json:"services"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout.String())
	}
	byName := map[string]configuredService{}
	for _, s := range payload.Services {
		byName[s.Name] = s
	}
	if vm := byName["vm"]; vm.Backend != string(servicemgr.BackendLibvirt) || vm.Unit != "web01" || vm.Enabled || vm.State != sermoapp.TargetStateDisabled {
		t.Errorf("vm = %+v", vm)
	}
	if web := byName["web"]; web.Unit != "nginx.service" || !web.Monitored || !web.Enabled {
		t.Errorf("web = %+v", web)
	}
}

func TestConfiguredServicesPreferDaemonState(t *testing.T) {
	t.Setenv(config.EnvWebPassword, "secret")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != web.APIPathServices {
			http.NotFound(w, r)
			return
		}
		writeDaemonAPITestJSON(w, []web.Service{
			{Name: "web", DisplayName: "Web", Backend: "systemd", Unit: "nginx.service", State: sermoapp.TargetStateMonitored, Enabled: true, Monitored: true},
			{Name: "ctr", DisplayName: "Ctr", Backend: "docker", Unit: "web", State: sermoapp.TargetStatePaused, Enabled: true, Monitored: true},
		})
	}))
	defer srv.Close()

	root, global, _ := daemonAPITestConfig(t, srv.URL, `
web:
  address: HOST
  port: PORT
paths:
  services: [SERVICES]
defaults:
  policy: { cooldown: 5m }
`)
	servicesDir := filepath.Join(root, "services")
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(servicesDir, "web.yml"), "name: web\nservice: nginx\n")
	mustWrite(t, filepath.Join(servicesDir, "ctr.yml"), "name: ctr\ncontrol: {type: docker, container: web}\n")
	// Added after the daemon loaded its config: probed locally.
	mustWrite(t, filepath.Join(servicesDir, "late.yml"), "name: late\nservice: late\n")

	var stdout, stderr bytes.Buffer
	var statusCalls []string
	app := configuredServicesTestApp(config.Load, &stdout, &stderr, &statusCalls)
	if code := app.Run(context.Background(), []string{"--config", global, "--json", "services"}); code != exitSuccess {
		t.Fatalf("services exit = %d, stderr = %s", code, stderr.String())
	}
	var payload struct {
		Services []configuredService `json:"services"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout.String())
	}
	byName := map[string]configuredService{}
	for _, s := range payload.Services {
		byName[s.Name] = s
	}
	if ctr := byName["ctr"]; ctr.State != sermoapp.TargetStatePaused || ctr.Backend != "docker" || !ctr.Monitored {
		t.Errorf("ctr = %+v, want the daemon's paused docker view", ctr)
	}
	if w := byName["web"]; w.State != sermoapp.TargetStateMonitored || w.DisplayName != "Web" {
		t.Errorf("web = %+v, want the daemon's view", w)
	}
	if late := byName["late"]; late.Backend != string(servicemgr.BackendSystemd) || late.State == "" {
		t.Errorf("late = %+v, want a local probe", late)
	}
	if strings.Join(statusCalls, ",") != "late.service" {
		t.Errorf("local status queries = %v, want only the service the daemon did not report", statusCalls)
	}
}

func TestConfiguredServicesNotifySendsReport(t *testing.T) {
	root, global := writeCatalogServiceConfig(t)
	notifier := &fakeReportNotifier{name: "ops"}
	var stdout, stderr bytes.Buffer
	app := configuredServicesTestApp(testLoadConfigWithCatalog(filepath.Join(root, "catalog")), &stdout, &stderr, nil)
	app.BuildReportNotifiers = func(*config.Config) (map[string]notify.Notifier, []string) {
		return map[string]notify.Notifier{"ops": notifier}, nil
	}
	if code := app.Run(context.Background(), []string{"--config", global, "services", "--notify", "ops"}); code != exitSuccess {
		t.Fatalf("services --notify exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "sent services report to ops") {
		t.Fatalf("stdout missing sent confirmation:\n%s", stdout.String())
	}
	if !strings.Contains(notifier.msg.HTML, "Configured services health") || !strings.Contains(notifier.msg.Body, "web") {
		t.Fatalf("notifier message = %+v", notifier.msg)
	}
	if notifier.msg.Fields[cliFieldSermoReport] != commandServices {
		t.Fatalf("SERMO_REPORT = %q", notifier.msg.Fields[cliFieldSermoReport])
	}
}

func TestConfiguredServicesReportMessage(t *testing.T) {
	services := []configuredService{
		{Name: "web", DisplayName: "Web", Backend: "systemd", State: sermoapp.TargetStateMonitored, Enabled: true, Monitored: true},
		{Name: "bad", DisplayName: "<Bad>", Backend: "docker", State: sermoapp.TargetStateFailed, Enabled: true, Monitored: true},
		// Unresolvable: an issue even though nothing says it is monitored.
		{Name: "broken", DisplayName: "broken", State: configuredServiceStateError},
		{Name: "vm", DisplayName: "VM", Backend: "libvirt", State: sermoapp.TargetStateDisabled},
		{Name: "quiet", DisplayName: "Quiet", Backend: "systemd", State: sermoapp.TargetStateStopped, Enabled: true},
	}
	msg := configuredServicesReportMessage(services, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	if msg.Subject != "[sermo] services report: 1 ok, 2 issue(s), 2 unmonitored" {
		t.Fatalf("subject = %q", msg.Subject)
	}
	for _, want := range []string{"Unmonitored: 2", "SERVICE\tTYPE\tSTATUS", "<Bad>\tdocker\tfailed"} {
		if !strings.Contains(msg.Body, want) {
			t.Errorf("plain body missing %q:\n%s", want, msg.Body)
		}
	}
	if !strings.Contains(msg.HTML, "&lt;Bad&gt;") || strings.Contains(msg.HTML, "<Bad>") {
		t.Errorf("HTML body did not escape the display name:\n%s", msg.HTML)
	}
	if msg.Fields[cliFieldSermoReportIssues] != "2" || msg.Fields[cliFieldSermoReportUnmonitored] != "2" {
		t.Errorf("fields = %v", msg.Fields)
	}
}

func TestConfiguredServicesNoneConfigured(t *testing.T) {
	root, global := writeCatalogServiceConfig(t)
	if err := os.Remove(filepath.Join(root, "services", "web.yml")); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	app := configuredServicesTestApp(testLoadConfigWithCatalog(filepath.Join(root, "catalog")), &stdout, &bytes.Buffer{}, nil)
	if code := app.Run(context.Background(), []string{"--config", global, "services"}); code != exitSuccess {
		t.Fatalf("services exit = %d", code)
	}
	if got := strings.TrimSpace(stdout.String()); got != "no configured services" {
		t.Fatalf("output = %q", got)
	}
}

func TestConfiguredServicesApplyPersistedMonitorState(t *testing.T) {
	root, global := writeCatalogServiceConfig(t)
	app := monitorTestApp(root, nil)
	if code := app.Run(context.Background(), []string{"--config", global, "unmonitor", "web"}); code != exitSuccess {
		t.Fatalf("unmonitor exit = %d", code)
	}
	var stdout bytes.Buffer
	app.Stdout = &stdout
	if code := app.Run(context.Background(), []string{"--config", global, "--json", "services"}); code != exitSuccess {
		t.Fatalf("services exit = %d", code)
	}
	var payload struct {
		Services []configuredService `json:"services"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout.String())
	}
	if len(payload.Services) != 1 || payload.Services[0].Monitored || payload.Services[0].State != sermoapp.TargetStateStarted {
		t.Fatalf("services = %+v, want web unmonitored and started", payload.Services)
	}
}
