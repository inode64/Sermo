package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"sermo/internal/operation"
)

// The notice names catalog guards; a renamed or added backup guard in the
// catalog must show up here instead of silently losing the notice.
func TestBackupGuardNamesMatchTheCatalog(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "catalog", "services", "*.yml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("catalog services: %v, %v", paths, err)
	}
	var found []string
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Rules map[string]struct {
				Type   string   `yaml:"type"`
				Blocks []string `yaml:"blocks"`
			} `yaml:"rules"`
		}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for name, rule := range doc.Rules {
			if rule.Type == "guard" && slices.Contains(rule.Blocks, actionRestart) && strings.Contains(name, "backup") && !slices.Contains(found, name) {
				found = append(found, name)
			}
		}
	}
	slices.Sort(found)
	want := []string{guardBlockRestartDuringBackup, guardBlockRestartDuringBackupOrRestore}
	if !slices.Equal(found, want) {
		t.Fatalf("catalog backup restart guards = %v, want %v", found, want)
	}
	for _, name := range want {
		if !shouldNotifyInteractiveBlockedAction(operation.Result{Status: operation.ResultBlocked, Action: actionRestart, Guard: name}) {
			t.Errorf("guard %s does not raise the notice", name)
		}
	}
}
