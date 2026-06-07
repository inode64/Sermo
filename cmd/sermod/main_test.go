package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRunRejectsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"enabled", "profiles"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	global := filepath.Join(dir, "sermo.yml")
	content := fmt.Sprintf(`engine:
  interval: notaduration
paths:
  profiles: [%s]
  enabled: [%s]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
`, filepath.Join(dir, "profiles"), filepath.Join(dir, "enabled"))
	if err := os.WriteFile(global, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := run([]string{"run", "--config", global}); code != exitConfigInvalid {
		t.Fatalf("run() exit = %d, want %d", code, exitConfigInvalid)
	}
}