package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWatchPanelDescriptorsMatchShellMarkers(t *testing.T) {
	srcDir := filepath.Join("..", "src")
	panels, err := loadWatchPanels(filepath.Join(srcDir, watchPanelsFilename))
	if err != nil {
		t.Fatalf("loadWatchPanels: %v", err)
	}
	shell, err := os.ReadFile(filepath.Join(srcDir, webBuildShellFilename))
	if err != nil {
		t.Fatalf("read shell: %v", err)
	}
	for _, panel := range panels {
		marker := watchPanelMarker(panel.Key)
		if strings.Count(string(shell), marker) != 1 {
			t.Errorf("shell marker %q count != 1", marker)
		}
		markup, err := renderWatchPanel(panel)
		if err != nil {
			t.Fatalf("renderWatchPanel(%s): %v", panel.Key, err)
		}
		for _, expected := range []string{
			`id="` + panel.SectionID + `"`,
			`data-panel="` + panel.Key + `"`,
			`id="` + panel.RowsID + `"`,
			`data-wf="stale"`,
		} {
			if !strings.Contains(markup, expected) {
				t.Errorf("panel %q markup missing %q", panel.Key, expected)
			}
		}
	}
}

func TestLoadWatchPanelsRejectsDuplicateKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), watchPanelsFilename)
	data := `[
  {"key":"host","sectionId":"one","rowsId":"rows-one","columns":[{"label":"Name"}]},
  {"key":"host","sectionId":"two","rowsId":"rows-two","columns":[{"label":"Name"}]}
]`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write descriptors: %v", err)
	}
	if _, err := loadWatchPanels(path); err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("loadWatchPanels duplicate error = %v", err)
	}
}
