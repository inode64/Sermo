package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServicesCommand(t *testing.T) {
	root := t.TempDir()
	catalogDir := filepath.Join(root, "catalog")
	catalogServicesDir := filepath.Join(catalogDir, "services")
	appsDir := filepath.Join(catalogDir, "apps")
	servicesDir := filepath.Join(root, "services")
	binDir := filepath.Join(root, "bin")
	for _, d := range []string{catalogServicesDir, appsDir, servicesDir, binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	nginx := filepath.Join(binDir, "nginx")
	linked := filepath.Join(binDir, "linked")
	for _, p := range []string{nginx, linked} {
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A service-category catalog service (services/) and an app-category catalog service (apps/).
	write(filepath.Join(catalogServicesDir, "nginx.yml"), fmt.Sprintf(`
name: nginx
display_name: "Nginx"
service: nginx
variables:
  binary: %q
preflight: { binary: { type: binary, path: "${binary}" } }
`, nginx))
	write(filepath.Join(catalogServicesDir, "linked.yml"), `
name: linked
display_name: "Linked Service"
service: linked
apps: [linked]
`)
	write(filepath.Join(appsDir, "linked.yml"), fmt.Sprintf(`
name: linked
variables:
  binary: %q
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["/definitely-missing-sermo-version-probe"], timeout: 10s }
`, linked))
	write(filepath.Join(appsDir, "git.yml"), "name: git\nvariables: { binary: /bin/git }\n")
	write(filepath.Join(root, "sermo.yml"), fmt.Sprintf(`
engine: { backend: auto }
paths: { services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, servicesDir))
	global := filepath.Join(root, "sermo.yml")

	var out bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &out, Stderr: &bytes.Buffer{}, LoadConfig: testLoadConfigWithCatalog(catalogDir)}
	if code := app.Run(context.Background(), []string{"--config", global, "services"}); code != exitSuccess {
		t.Fatalf("services exit = %d", code)
	}
	got := out.String()
	if !strings.Contains(got, "SERVICE") {
		t.Errorf("services should label the first column SERVICE:\n%s", got)
	}
	if !strings.Contains(got, "Nginx") || !strings.Contains(got, "ok") {
		t.Errorf("services should list the installed service Nginx:\n%s", got)
	}
	if !strings.Contains(got, "Linked Service") || strings.Contains(got, "Linked Service  -  error") {
		t.Errorf("services should list app-linked services without failing on version probe errors:\n%s", got)
	}
	if strings.Contains(got, "git") {
		t.Errorf("services must not list app-category catalog services:\n%s", got)
	}
}
