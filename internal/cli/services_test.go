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
	daemonsDir := filepath.Join(root, "daemons") // root daemons → category service
	appsDir := filepath.Join(daemonsDir, "apps")
	enabledDir := filepath.Join(root, "enabled")
	binDir := filepath.Join(root, "bin")
	for _, d := range []string{daemonsDir, appsDir, enabledDir, binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	nginx := filepath.Join(binDir, "nginx")
	if err := os.WriteFile(nginx, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A service-category daemon (root) and an app-category daemon (apps/).
	write(filepath.Join(daemonsDir, "nginx.yml"), fmt.Sprintf(`kind: daemon
name: nginx
display_name: "Nginx"
service: { name: nginx }
binary: %q
preflight: { binary: { type: binary, path: "${binary}" } }
`, nginx))
	write(filepath.Join(appsDir, "git.yml"), "kind: daemon\nname: git\nbinary: /bin/git\n")
	write(filepath.Join(root, "sermo.yml"), fmt.Sprintf(`
engine: { backend: auto }
paths: { catalog: [ %s ], includes: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, daemonsDir, enabledDir))
	global := filepath.Join(root, "sermo.yml")

	var out bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &out, Stderr: &bytes.Buffer{}}
	if code := app.Run(context.Background(), []string{"--config", global, "services"}); code != exitSuccess {
		t.Fatalf("services exit = %d", code)
	}
	got := out.String()
	if !strings.Contains(got, "Nginx") || !strings.Contains(got, "ok") {
		t.Errorf("services should list the installed service Nginx:\n%s", got)
	}
	if strings.Contains(got, "git") {
		t.Errorf("services must not list app-category daemons:\n%s", got)
	}
}
