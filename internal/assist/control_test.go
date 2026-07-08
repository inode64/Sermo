package assist

import (
	"strings"
	"testing"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/conn"
	"sermo/internal/dockerctl"
	"sermo/internal/virt"
)

func TestDockerAssistant(t *testing.T) {
	env := Env{
		ServiceNames: map[string]struct{}{"docker-redis": {}},
		DockerContainers: func() ([]DockerCandidate, error) {
			return []DockerCandidate{
				{Name: "docker-web", Title: "web", Container: "web", Status: "running", Socket: "/run/docker.sock"},
				{Name: "docker-redis", Title: "redis", Container: "redis", Status: "running", Socket: "/run/docker.sock"},
			}, nil
		},
	}
	script := strings.Join([]string{
		config.SelectionKeywordAll, // select both; redis is already configured and will be skipped
		"y",                        // shared settings
		"1",                        // monitor enabled
		"",                         // interval inherit
		"n",                        // no dry-run
	}, "\n") + "\n"
	var out strings.Builder
	p := NewPrompt(strings.NewReader(script), &out)
	res, err := dockerAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := res.Services["docker-redis"]; ok {
		t.Fatalf("already configured container was emitted: %v", res.Services["docker-redis"])
	}
	svc := res.Services["docker-web"].(map[string]any)
	control := svc[config.SectionControl].(map[string]any)
	if control[dockerctl.ControlKeyType] != dockerctl.ControlType || control[dockerctl.ControlKeyContainer] != "web" || control[dockerctl.ControlKeySocket] != "/run/docker.sock" {
		t.Fatalf("control = %v, want docker/web socket", control)
	}
	check := svc[config.SectionWatches].(map[string]any)[dockerctl.ControlType].(map[string]any)[config.WatchKeyCheck].(map[string]any)
	if check[checks.CheckKeyType] != dockerctl.ControlType || check[checks.CheckKeyContainer] != "web" || check[checks.CheckKeyOnChange] != true {
		t.Fatalf("docker check = %v", check)
	}
	expect := check[checks.CheckKeyExpect].(map[string]any)[conn.ExtraKeyContainerStatus].(map[string]any)
	if expect[checks.CheckKeyOp] != cfgval.CompareOpEqual || expect[checks.CheckKeyValue] != conn.DockerContainerStatusRunning {
		t.Fatalf("docker expect = %v, want running", expect)
	}
	if !strings.Contains(out.String(), `"docker-redis" is already configured`) {
		t.Fatalf("skip message missing from output:\n%s", out.String())
	}
}

func TestVMAssistant(t *testing.T) {
	env := Env{
		VMs: func() ([]VMCandidate, error) {
			return []VMCandidate{{Name: "vm-web01", Title: "web01", Domain: "web01", Status: "active", URI: "qemu:///system", Socket: "/run/libvirt/libvirt-sock"}}, nil
		},
	}
	script := strings.Join([]string{
		"1", // select web01
		"3", // restore previous state
		"1m",
		"y", // dry-run
	}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := vmAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	svc := res.Services["vm-web01"].(map[string]any)
	if svc[config.EntryKeyMonitor] != config.MonitorPrevious || svc[config.EntryKeyInterval] != "1m" {
		t.Fatalf("monitor/interval = %v/%v, want previous/1m", svc[config.EntryKeyMonitor], svc[config.EntryKeyInterval])
	}
	if svc[config.EntryKeyDryRun] != true {
		t.Fatalf("dry_run = %v, want true", svc[config.EntryKeyDryRun])
	}
	control := svc[config.SectionControl].(map[string]any)
	if control[virt.ControlKeyType] != virt.ControlType || control[virt.ControlKeyDomain] != "web01" || control[virt.ControlKeyURI] != "qemu:///system" {
		t.Fatalf("control = %v, want libvirt web01", control)
	}
	check := svc[config.SectionWatches].(map[string]any)[AssistantNameVM].(map[string]any)[config.WatchKeyCheck].(map[string]any)
	if check[checks.CheckKeyType] != virt.ControlType || check[checks.CheckKeyDomain] != "web01" || check[checks.CheckKeyQuery] != "qemu:///system" {
		t.Fatalf("vm check = %v", check)
	}
}
