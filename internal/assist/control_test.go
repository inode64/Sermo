package assist

import (
	"strings"
	"testing"
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
		"all", // select both; redis is already configured and will be skipped
		"y",   // shared settings
		"1",   // monitor enabled
		"",    // interval inherit
		"n",   // no dry-run
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
	control := svc["control"].(map[string]any)
	if control["type"] != "docker" || control["container"] != "web" || control["socket"] != "/run/docker.sock" {
		t.Fatalf("control = %v, want docker/web socket", control)
	}
	check := svc["checks"].(map[string]any)["docker"].(map[string]any)
	if check["type"] != "docker" || check["container"] != "web" || check["on_change"] != true {
		t.Fatalf("docker check = %v", check)
	}
	expect := check["expect"].(map[string]any)["container.status"].(map[string]any)
	if expect["op"] != "==" || expect["value"] != "running" {
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
	if svc["monitor"] != "previous" || svc["interval"] != "1m" {
		t.Fatalf("monitor/interval = %v/%v, want previous/1m", svc["monitor"], svc["interval"])
	}
	if svc["dry_run"] != true {
		t.Fatalf("dry_run = %v, want true", svc["dry_run"])
	}
	control := svc["control"].(map[string]any)
	if control["type"] != "libvirt" || control["domain"] != "web01" || control["uri"] != "qemu:///system" {
		t.Fatalf("control = %v, want libvirt web01", control)
	}
	check := svc["checks"].(map[string]any)["vm"].(map[string]any)
	if check["type"] != "libvirt" || check["domain"] != "web01" || check["query"] != "qemu:///system" {
		t.Fatalf("vm check = %v", check)
	}
}
