package servicemgr

import (
	"context"
	"os"
	"strings"
	"testing"

	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
)

func resolver(commands map[string]execxResultErr, paths map[string]bool) UnitResolver {
	return UnitResolver{
		Runner: scriptRunner(commands),
		Probe:  fakeProbe{paths: paths},
	}
}

type execxResultErr struct {
	exit int
	err  error
}

func TestResolveSystemdPicksFirstKnownCandidate(t *testing.T) {
	// apache2.service is unknown, httpd.service is known -> pick httpd.service.
	r := resolver(map[string]execxResultErr{
		"systemctl cat -- apache2.service": {exit: 1},
		"systemctl cat -- httpd.service":   {exit: 0},
	}, nil)

	unit, err := r.Resolve(context.Background(), BackendSystemd, []string{"apache2", "httpd.service"}, false)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if unit != "httpd.service" {
		t.Fatalf("unit = %q, want httpd.service", unit)
	}
}

func TestResolveSystemdNormalizesBareName(t *testing.T) {
	r := resolver(map[string]execxResultErr{"systemctl cat -- mysql.service": {exit: 0}}, nil)
	unit, err := r.Resolve(context.Background(), BackendSystemd, []string{"mysql"}, true)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if unit != "mysql.service" {
		t.Fatalf("unit = %q, want mysql.service", unit)
	}
}

func TestResolveSystemdNoExplicitCandidatesTrustsName(t *testing.T) {
	// The configured unit name is unknown to systemctl cat, but when it is the
	// trusted service name the resolver keeps it (sysv-generated units, etc.).
	r := resolver(map[string]execxResultErr{"systemctl cat -- weird.service": {exit: 4}}, nil)
	unit, err := r.Resolve(context.Background(), BackendSystemd, []string{"weird"}, true)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if unit != "weird.service" {
		t.Fatalf("unit = %q, want weird.service (trusted)", unit)
	}
}

func TestResolveSystemdCandidatesNoneResolveFails(t *testing.T) {
	r := resolver(map[string]execxResultErr{
		"systemctl cat -- apache2.service": {exit: 1},
		"systemctl cat -- httpd.service":   {exit: 1},
	}, nil)
	_, err := r.Resolve(context.Background(), BackendSystemd, []string{"apache2", "httpd.service"}, false)
	if err == nil {
		t.Fatal("Resolve() error = nil, want failure listing candidates")
	}
}

func TestResolveOpenRCByInitScript(t *testing.T) {
	// apache absent, apache2 has an init script.
	r := resolver(nil, map[string]bool{"/etc/init.d/apache2": true})
	unit, err := r.Resolve(context.Background(), BackendOpenRC, []string{"apache", "apache2"}, false)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if unit != "apache2" {
		t.Fatalf("unit = %q, want apache2 (no .service suffix on openrc)", unit)
	}
}

func TestResolvePrefersActiveKnownUnit(t *testing.T) {
	r := resolver(nil, map[string]bool{
		"/etc/init.d/php-fpm": true,
		"/etc/init.d/php8.2":  true,
	})
	r.Manager = resolverManager{statuses: map[string]Status{
		"php-fpm": StatusInactive,
		"php8.2":  StatusActive,
	}}
	unit, err := r.Resolve(context.Background(), BackendOpenRC, []string{"php-fpm", "php8.2"}, false)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if unit != "php8.2" {
		t.Fatalf("unit = %q, want active php8.2", unit)
	}
}

func TestMainPID(t *testing.T) {
	if pid, ok := MainPIDContext(context.Background(), execxtest.Fixed(execx.Result{Stdout: "4242\n"}, nil), BackendSystemd, "nginx.service"); !ok || pid != 4242 {
		t.Fatalf("MainPID = %d/%v, want 4242/true", pid, ok)
	}
	if _, ok := MainPIDContext(context.Background(), execxtest.Fixed(execx.Result{Stdout: "0\n"}, nil), BackendSystemd, "nginx.service"); ok {
		t.Error("MainPID 0 should report not found")
	}
	if _, ok := MainPIDContext(context.Background(), execxtest.Fixed(execx.Result{Stdout: "4242\n"}, nil), BackendOpenRC, "nginx"); ok {
		t.Error("OpenRC must report no MainPID")
	}
}

func TestCgroupPIDs(t *testing.T) {
	runner := execxtest.Fixed(execx.Result{Stdout: "/system.slice/nginx.service\n"}, nil)
	files := map[string]string{
		"/sys/fs/cgroup/system.slice/nginx.service/cgroup.procs": "1508\n1600\n1602\n",
	}
	readFile := func(path string) ([]byte, error) {
		if v, ok := files[path]; ok {
			return []byte(v), nil
		}
		return nil, os.ErrNotExist
	}

	pids, ok := CgroupPIDsContext(context.Background(), runner, readFile, BackendSystemd, "nginx.service")
	if !ok {
		t.Fatal("CgroupPIDs ok = false, want true")
	}
	want := []int{1508, 1600, 1602}
	if len(pids) != len(want) {
		t.Fatalf("pids = %v, want %v", pids, want)
	}
	for i := range want {
		if pids[i] != want[i] {
			t.Fatalf("pids = %v, want %v", pids, want)
		}
	}

	// Empty control group -> not found.
	if _, ok := CgroupPIDsContext(context.Background(), execxtest.Fixed(execx.Result{Stdout: "/\n"}, nil), readFile, BackendSystemd, "x"); ok {
		t.Error("root/empty control group should report no cgroup PIDs")
	}
	// OpenRC has no cgroup query.
	if _, ok := CgroupPIDsContext(context.Background(), runner, readFile, BackendOpenRC, "nginx"); ok {
		t.Error("OpenRC must report no cgroup PIDs")
	}
}

type propertyRunner struct{}

func (propertyRunner) Run(_ context.Context, _ string, args ...string) (execx.Result, error) {
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, systemctlPropertyMainPID):
		return execx.Result{Stdout: "1600\n"}, nil
	case strings.Contains(joined, systemctlPropertyCGroup):
		return execx.Result{Stdout: "/system.slice/nginx.service\n"}, nil
	default:
		return execx.Result{}, nil
	}
}

func TestBackendPIDsKeepsMainPIDFirst(t *testing.T) {
	readFile := func(path string) ([]byte, error) {
		if path != "/sys/fs/cgroup/system.slice/nginx.service/cgroup.procs" {
			return nil, os.ErrNotExist
		}
		return []byte("1508\n1600\n1602\n"), nil
	}
	pids := BackendPIDsFuncWithRunner(context.Background(), BackendSystemd, "nginx.service", propertyRunner{}, readFile)()
	want := []int{1600, 1508, 1602}
	if len(pids) != len(want) {
		t.Fatalf("pids = %v, want %v", pids, want)
	}
	for i := range want {
		if pids[i] != want[i] {
			t.Fatalf("pids = %v, want %v", pids, want)
		}
	}
}

func TestResolveDeduplicatesCandidates(t *testing.T) {
	// A unit name repeated in candidates is probed once.
	rr := scriptRunner(map[string]execxResultErr{"systemctl cat -- mysql.service": {exit: 0}})
	r := UnitResolver{Runner: rr, Probe: fakeProbe{}}
	if _, err := r.Resolve(context.Background(), BackendSystemd, []string{"mysql", "mysql.service", "mysql"}, false); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if rr.Count("systemctl", "cat", "--", "mysql.service") != 1 {
		t.Fatalf("mysql.service probed %d times, want 1 (deduped)", rr.Count("systemctl", "cat", "--", "mysql.service"))
	}
}

type resolverManager struct {
	statuses map[string]Status
}

func (m resolverManager) Status(_ context.Context, service string) (ServiceStatus, error) {
	status := m.statuses[service]
	if status == "" {
		status = StatusUnknown
	}
	return ServiceStatus{Service: service, Backend: BackendOpenRC, Unit: service, Status: status}, nil
}
func (m resolverManager) Start(context.Context, string) error                  { return nil }
func (m resolverManager) Stop(context.Context, string) error                   { return nil }
func (m resolverManager) Restart(context.Context, string) error                { return nil }
func (m resolverManager) Reload(context.Context, string) error                 { return nil }
func (m resolverManager) SupportsReload(context.Context, string) (bool, error) { return false, nil }
func (m resolverManager) ResetState(context.Context, string) error             { return nil }

func TestCgroupPIDsFiltersZeroAndEmpty(t *testing.T) {
	runner := execxtest.Fixed(execx.Result{Stdout: "/system.slice/x.service\n"}, nil)
	base := "/sys/fs/cgroup/system.slice/x.service/cgroup.procs"
	rf := func(content string) func(string) ([]byte, error) {
		return func(p string) ([]byte, error) {
			if p == base {
				return []byte(content), nil
			}
			return nil, os.ErrNotExist
		}
	}
	// PID 0 is not a real process and must be excluded.
	pids, ok := CgroupPIDsContext(context.Background(), runner, rf("0\n42\n"), BackendSystemd, "x.service")
	if !ok || len(pids) != 1 || pids[0] != 42 {
		t.Fatalf("pids = %v ok=%v, want [42]", pids, ok)
	}
	// A cgroup with no valid PIDs reports not-found.
	if _, ok := CgroupPIDsContext(context.Background(), runner, rf("0\n\n"), BackendSystemd, "x.service"); ok {
		t.Fatal("a cgroup with only invalid PIDs must report not-found")
	}
}

func TestResolveEmptyCandidates(t *testing.T) {
	r := resolver(nil, nil)
	// trust + no candidates must error (no index-out-of-range on candidates[0]).
	if _, err := r.Resolve(context.Background(), BackendSystemd, nil, true); err == nil {
		t.Fatal("empty candidates with trust must error, not panic")
	}
	// no candidates without trust yields the 'not available' error.
	if _, err := r.Resolve(context.Background(), BackendSystemd, nil, false); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("empty candidates error = %v, want 'not available'", err)
	}
}

// scriptRunner answers exact command lines with the scripted exit code and
// error; unknown commands succeed with no output.
func scriptRunner(results map[string]execxResultErr) *execxtest.Runner {
	byLine := make(map[string]execx.Result, len(results))
	errs := make(map[string]error, len(results))
	for line, res := range results {
		byLine[line] = execx.Result{ExitCode: res.exit}
		if res.err != nil {
			errs[line] = res.err
		}
	}
	return &execxtest.Runner{ByLine: byLine, Errs: errs}
}
