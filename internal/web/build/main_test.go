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
	for i := range panels {
		panel := &panels[i]
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

// The login page is built from the same design tokens as the dashboard, so a
// palette change reaches both; its html/template actions survive the build.
func TestRenderLoginSharesTheDashboardTokens(t *testing.T) {
	srcDir := filepath.Join("..", "src")
	page, err := renderLogin(srcDir)
	if err != nil {
		t.Fatalf("renderLogin: %v", err)
	}
	dashboard, err := renderDashboard(srcDir)
	if err != nil {
		t.Fatalf("renderDashboard: %v", err)
	}
	// A few declarations from tokens.css, in both themes, must reach both pages.
	for _, token := range []string{"--paused: #6639ba", "--paused: #a371f7", "--on-accent: #ffffff", "--crit: #b4232a"} {
		if !strings.Contains(page, token) || !strings.Contains(dashboard, token) {
			t.Errorf("token %q missing from the login page or the dashboard", token)
		}
	}
	for _, action := range []string{loginNonceAction, "{{.Message}}", "{{.Version}}"} {
		if !strings.Contains(page, action) {
			t.Errorf("login page lost the template action %s", action)
		}
	}
	if strings.Contains(page, cssMarker) {
		t.Error("css marker left in the login page")
	}
}

// A login shell the template engine rejects fails the build, and neither
// page is written: the committed pair never goes out of step.
func TestBuildWritesNothingWhenTheLoginPageIsBroken(t *testing.T) {
	src := t.TempDir()
	for _, name := range []string{webBuildShellFilename, webBuildStylesFilename, webBuildScriptFilename, watchPanelsFilename, loginStylesFilename, "tokens.css", "api.js", "format.js"} {
		data, err := os.ReadFile(filepath.Join("..", "src", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.CopyFS(filepath.Join(src, "vendor"), os.DirFS(filepath.Join("..", "src", "vendor"))); err != nil {
		t.Fatal(err)
	}
	broken := "<style nonce=\"{{.Nonce}}\">" + cssMarker + "</style>{{.Nonce}"
	if err := os.WriteFile(filepath.Join(src, loginShellFilename), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	out, loginOut := filepath.Join(outDir, "index.html"), filepath.Join(outDir, "login.html")
	if err := build(src, out, loginOut); err == nil || !strings.Contains(err.Error(), "not a valid template") {
		t.Fatalf("build = %v, want a template error", err)
	}
	for _, path := range []string{out, loginOut} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s was written despite the failure", filepath.Base(path))
		}
	}
}
