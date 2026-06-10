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
	profilesDir := filepath.Join(root, "profiles") // root profiles → category service
	appsDir := filepath.Join(profilesDir, "apps")
	enabledDir := filepath.Join(root, "enabled")
	binDir := filepath.Join(root, "bin")
	for _, d := range []string{profilesDir, appsDir, enabledDir, binDir} {
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
	// A service-category profile (root) and an app-category profile (apps/).
	write(filepath.Join(profilesDir, "nginx.yml"), fmt.Sprintf(`kind: profile
name: nginx
display_name: "Nginx"
service: { name: nginx }
variables: { binary: %q }
preflight: { binary: { type: binary, path: "${binary}" } }
`, nginx))
	write(filepath.Join(appsDir, "git.yml"), "kind: profile\nname: git\nvariables: { binary: /bin/git }\n")
	write(filepath.Join(root, "sermo.yml"), fmt.Sprintf(`
engine: { backend: auto }
paths: { profiles: [ %s ], includes: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, profilesDir, enabledDir))
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
		t.Errorf("services must not list app-category profiles:\n%s", got)
	}
}
