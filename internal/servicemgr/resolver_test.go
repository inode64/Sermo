package servicemgr

import (
	"context"
	"os"
	"testing"

	"sermo/internal/execx"
)

func resolver(commands map[string]execxResultErr, paths map[string]bool) UnitResolver {
	return UnitResolver{
		Runner: scriptRunner{results: commands, calls: map[string]int{}},
		Probe:  fakeProbe{paths: paths},
	}
}

type execxResultErr struct {
	exit int
	err  error
}

type scriptRunner struct {
	results map[string]execxResultErr
	calls   map[string]int
}

func (r scriptRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	key := name
	for _, a := range args {
		key += " " + a
	}
	if r.calls != nil {
		r.calls[key]++
	}
	res := r.results[key]
	return execx.Result{ExitCode: res.exit}, res.err
}

func TestResolveSystemdPicksFirstKnownAlias(t *testing.T) {
	// apache2.service is unknown, httpd.service is known -> pick httpd.service.
	r := resolver(map[string]execxResultErr{
		"systemctl cat -- apache2.service": {exit: 1},
		"systemctl cat -- httpd.service":   {exit: 0},
	}, nil)

	unit, err := r.Resolve(context.Background(), BackendSystemd, "apache2", []string{"httpd.service"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if unit != "httpd.service" {
		t.Fatalf("unit = %q, want httpd.service", unit)
	}
}

func TestResolveSystemdNormalizesBareName(t *testing.T) {
	r := resolver(map[string]execxResultErr{"systemctl cat -- mysql.service": {exit: 0}}, nil)
	unit, err := r.Resolve(context.Background(), BackendSystemd, "mysql", nil)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if unit != "mysql.service" {
		t.Fatalf("unit = %q, want mysql.service", unit)
	}
}

func TestResolveSystemdNoAliasesTrustsName(t *testing.T) {
	// service.name is unknown to systemctl cat, but with no aliases the resolver
	// trusts it rather than failing (sysv-generated units, etc.).
	r := resolver(map[string]execxResultErr{"systemctl cat -- weird.service": {exit: 4}}, nil)
	unit, err := r.Resolve(context.Background(), BackendSystemd, "weird", nil)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if unit != "weird.service" {
		t.Fatalf("unit = %q, want weird.service (trusted)", unit)
	}
}

func TestResolveSystemdAliasesNoneResolveFails(t *testing.T) {
	r := resolver(map[string]execxResultErr{
		"systemctl cat -- apache2.service": {exit: 1},
		"systemctl cat -- httpd.service":   {exit: 1},
	}, nil)
	_, err := r.Resolve(context.Background(), BackendSystemd, "apache2", []string{"httpd.service"})
	if err == nil {
		t.Fatal("Resolve() error = nil, want failure listing candidates")
	}
}

func TestResolveOpenRCByInitScript(t *testing.T) {
	// apache absent, apache2 has an init script.
	r := resolver(nil, map[string]bool{"/etc/init.d/apache2": true})
	unit, err := r.Resolve(context.Background(), BackendOpenRC, "apache", []string{"apache2"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if unit != "apache2" {
		t.Fatalf("unit = %q, want apache2 (no .service suffix on openrc)", unit)
	}
}

type stdoutRunner struct{ out string }

func (r stdoutRunner) Run(_ context.Context, _ string, _ ...string) (execx.Result, error) {
	return execx.Result{Stdout: r.out}, nil
}

func TestMainPID(t *testing.T) {
	if pid, ok := MainPID(stdoutRunner{out: "4242\n"}, BackendSystemd, "nginx.service"); !ok || pid != 4242 {
		t.Fatalf("MainPID = %d/%v, want 4242/true", pid, ok)
	}
	if _, ok := MainPID(stdoutRunner{out: "0\n"}, BackendSystemd, "nginx.service"); ok {
		t.Error("MainPID 0 should report not found")
	}
	if _, ok := MainPID(stdoutRunner{out: "4242\n"}, BackendOpenRC, "nginx"); ok {
		t.Error("OpenRC must report no MainPID")
	}
}

func TestCgroupPIDs(t *testing.T) {
	runner := stdoutRunner{out: "/system.slice/nginx.service\n"}
	files := map[string]string{
		"/sys/fs/cgroup/system.slice/nginx.service/cgroup.procs": "1508\n1600\n1602\n",
	}
	readFile := func(path string) ([]byte, error) {
		if v, ok := files[path]; ok {
			return []byte(v), nil
		}
		return nil, os.ErrNotExist
	}

	pids, ok := CgroupPIDs(runner, readFile, BackendSystemd, "nginx.service")
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
	if _, ok := CgroupPIDs(stdoutRunner{out: "/\n"}, readFile, BackendSystemd, "x"); ok {
		t.Error("root/empty control group should report no cgroup PIDs")
	}
	// OpenRC has no cgroup query.
	if _, ok := CgroupPIDs(runner, readFile, BackendOpenRC, "nginx"); ok {
		t.Error("OpenRC must report no cgroup PIDs")
	}
}

func TestResolveDeduplicatesCandidates(t *testing.T) {
	// service.name repeated in aliases is probed once.
	rr := scriptRunner{results: map[string]execxResultErr{"systemctl cat -- mysql.service": {exit: 0}}, calls: map[string]int{}}
	r := UnitResolver{Runner: rr, Probe: fakeProbe{}}
	if _, err := r.Resolve(context.Background(), BackendSystemd, "mysql", []string{"mysql.service", "mysql"}); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if rr.calls["systemctl cat -- mysql.service"] != 1 {
		t.Fatalf("mysql.service probed %d times, want 1 (deduped)", rr.calls["systemctl cat -- mysql.service"])
	}
}
