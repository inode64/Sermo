// Command webbuild bundles the dashboard sources in internal/web/src into the
// embedded internal/web/index.html and the standalone login page
// internal/web/login.html.
//
// It runs esbuild in-process via its Go API (github.com/evanw/esbuild/pkg/api)
// — no Node, no npm, no spawned process — bundling src/app.js (and the modules
// it imports, including the vendored lit-html) into a single minified IIFE and
// minifying src/styles.css, then injecting both into the src/index.html shell.
// The login page gets src/login.css injected into the src/login.html shell; both
// stylesheets import src/tokens.css, so the palette is declared once.
//
// The {{CSP_NONCE}} (style + script) and {{VERSION}} placeholders are part of
// the shell and are left untouched for internal/web/server.go to fill per
// request; login.html keeps its html/template actions ({{.Nonce}} and friends)
// for internal/web/login.go. Both outputs are generated, committed artifacts;
// run `make web` after editing anything under internal/web/src and commit the
// result. `make web-check` fails CI if a committed file is stale.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// Markers in src/index.html where the bundled CSS and JS are injected. They sit
// inside the nonce'd <style>/<script> tags so the placeholders survive.
const (
	cssMarker = "/*__SERMO_CSS__*/"
	jsMarker  = "/*__SERMO_JS__*/"

	webBuildShellFilename  = "index.html"
	webBuildStylesFilename = "styles.css"
	loginShellFilename     = "login.html"
	loginStylesFilename    = "login.css"
	// loginNonceAction is the template action the login page's <style> and
	// <script> carry; internal/web/login.go fills it per request.
	loginNonceAction       = "{{.Nonce}}"
	webBuildScriptFilename = "app.js"
	watchPanelsFilename    = "watch-panels.json"

	webBuildExpectedOutputFiles = 1
	webBuildFailureExitCode     = 1
	webBuildOutputFileIndex     = 0
	webBuildOutputFileMode      = 0o600
	webBuildReplaceOnce         = 1
)

type watchPanelColumn struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type watchPanelDescriptor struct {
	Key               string             `json:"key"`
	SectionID         string             `json:"sectionId"`
	Heading           string             `json:"heading"`
	Title             string             `json:"title"`
	CountID           string             `json:"countId"`
	ControlsID        string             `json:"controlsId"`
	SearchID          string             `json:"searchId"`
	SearchLabel       string             `json:"searchLabel"`
	SearchPlaceholder string             `json:"searchPlaceholder"`
	TypeID            string             `json:"typeId"`
	TypeLabel         string             `json:"typeLabel"`
	AllTypesLabel     string             `json:"allTypesLabel"`
	FiltersID         string             `json:"filtersId"`
	FilterCountID     string             `json:"filterCountId"`
	RowsID            string             `json:"rowsId"`
	Caption           string             `json:"caption"`
	Columns           []watchPanelColumn `json:"columns"`
	Footnote          string             `json:"footnote"`
}

var watchPanelTemplate = template.Must(template.New("watch-panel").Parse(`<h2 class="visually-hidden">{{.Heading}}</h2>
<details id="{{.SectionID}}" open class="panel panel-hidden" data-panel="{{.Key}}">
  <summary><span class="summary-title">{{.Title}} <span id="{{.CountID}}" class="muted"></span></span></summary>
  <div id="{{.ControlsID}}" class="panel-controls">
    <button id="{{.Key}}-group-toggle" class="icon-btn" title="Group {{.Title}} by type" aria-label="Group {{.Title}} by type" aria-pressed="false">&#x25A6;</button>
    <button id="{{.Key}}-groups-toggle" class="icon-btn" title="Collapse {{.Title}} groups" aria-label="Collapse {{.Title}} groups">&#x25BE;</button>
    <label for="{{.SearchID}}" class="visually-hidden">{{.SearchLabel}}</label>
    <input id="{{.SearchID}}" type="search" placeholder="{{.SearchPlaceholder}}" aria-describedby="search-shortcut-hint">
{{if .TypeID}}    <select id="{{.TypeID}}" title="{{.TypeLabel}}" aria-label="{{.TypeLabel}}">
      <option value="all">{{.AllTypesLabel}}</option>
    </select>{{end}}
    <span id="{{.FiltersID}}" role="group" aria-label="{{.SearchLabel}} by state">
      <button data-wf="all" class="f-active" aria-pressed="true">all</button>
      <button data-wf="disabled" aria-pressed="false">disabled</button>
      <button data-wf="ok" aria-pressed="false">ok</button>
      <button data-wf="starting" aria-pressed="false">starting</button>
      <button data-wf="warning" aria-pressed="false">warning</button>
      <button data-wf="stale" aria-pressed="false">stale</button>
      <button data-wf="failed" aria-pressed="false">failed</button>
    </span>
    <span id="{{.FilterCountID}}" class="muted"></span>
  </div>
  <table class="watch-table">
    <caption class="visually-hidden">{{.Caption}}</caption>
    <thead><tr>{{range .Columns}}
      {{if .Key}}<th scope="col" class="sortable" data-watch-sort="{{.Key}}">{{.Label}}<span class="sort-ind" data-wi="{{.Key}}"></span></th>{{else}}<th scope="col">{{.Label}}</th>{{end}}{{end}}
    </tr></thead>
    <tbody id="{{.RowsID}}"></tbody>
  </table>
  <p class="muted panel-footnote">{{.Footnote}}</p>
</details>`))

func main() {
	srcDir := flag.String("src", "internal/web/src", "dashboard source directory")
	out := flag.String("out", "internal/web/index.html", "generated dashboard file")
	loginOut := flag.String("login-out", "internal/web/login.html", "generated login page file")
	flag.Parse()

	if err := build(*srcDir, *out, *loginOut); err != nil {
		fmt.Fprintln(os.Stderr, "webbuild:", err)
		//nolint:forbidigo // main cannot return an exit code; os.Exit here is the only way to propagate it.
		os.Exit(webBuildFailureExitCode)
	}
}

// build renders both pages before writing either, so a failure in one never
// leaves the other regenerated and the committed pair out of step.
func build(srcDir, out, loginOut string) error {
	dashboard, err := renderDashboard(srcDir)
	if err != nil {
		return err
	}
	login, err := renderLogin(srcDir)
	if err != nil {
		return err
	}
	for _, file := range []struct{ path, page string }{{out, dashboard}, {loginOut, login}} {
		if err := os.WriteFile(file.path, []byte(file.page), webBuildOutputFileMode); err != nil {
			return fmt.Errorf("write %s: %w", file.path, err)
		}
	}
	return nil
}

// readShell reads one HTML shell from srcDir, a Makefile-driven build flag.
func readShell(srcDir, name string) (string, error) {
	shell, err := os.ReadFile(filepath.Join(srcDir, name)) //nolint:gosec // G304: srcDir is a Makefile-driven build flag, not untrusted input.
	if err != nil {
		return "", fmt.Errorf("read shell %s: %w", name, err)
	}
	return string(shell), nil
}

// injectCSS bundles the stylesheet at cssEntry and puts it in place of the
// shell's single css marker, inside its nonce'd <style>.
func injectCSS(page, cssEntry string) (string, error) {
	css, err := bundleCSS(cssEntry)
	if err != nil {
		return "", fmt.Errorf("css %s: %w", filepath.Base(cssEntry), err)
	}
	if strings.Count(page, cssMarker) != webBuildReplaceOnce {
		return "", fmt.Errorf("css marker %q must occur once in the shell", cssMarker)
	}
	// A bundle that smuggled in a literal </style> would break the inline block,
	// and a template action would be executed by the server; esbuild never
	// emits either, but guard regardless.
	if strings.Contains(css, "</style") || strings.Contains(css, "{{") {
		return "", fmt.Errorf("css %s bundle contains </style or a template action", filepath.Base(cssEntry))
	}
	return strings.Replace(page, cssMarker, css, webBuildReplaceOnce), nil
}

// renderDashboard builds index.html: the shell with its watch panels, the
// minified stylesheet and the bundled script.
func renderDashboard(srcDir string) (string, error) {
	page, err := readShell(srcDir, webBuildShellFilename)
	if err != nil {
		return "", err
	}
	watchPanels, err := loadWatchPanels(filepath.Join(srcDir, watchPanelsFilename))
	if err != nil {
		return "", fmt.Errorf("watch panels: %w", err)
	}
	for i := range watchPanels {
		panel := &watchPanels[i]
		marker := watchPanelMarker(panel.Key)
		if strings.Count(page, marker) != webBuildReplaceOnce {
			return "", fmt.Errorf("watch panel marker %q must occur once", marker)
		}
		markup, err := renderWatchPanel(panel)
		if err != nil {
			return "", fmt.Errorf("watch panel %q: %w", panel.Key, err)
		}
		page = strings.Replace(page, marker, markup, webBuildReplaceOnce)
	}
	if page, err = injectCSS(page, filepath.Join(srcDir, webBuildStylesFilename)); err != nil {
		return "", err
	}
	js, err := bundleJS(filepath.Join(srcDir, webBuildScriptFilename))
	if err != nil {
		return "", fmt.Errorf("js: %w", err)
	}
	if strings.Count(page, jsMarker) != webBuildReplaceOnce {
		return "", fmt.Errorf("js marker %q must occur once in the shell", jsMarker)
	}
	if strings.Contains(js, "</script") {
		return "", errors.New("js bundle contains </script")
	}
	page = strings.Replace(page, jsMarker, js, webBuildReplaceOnce)
	// The server fills these per request; they must survive the build.
	for _, ph := range []string{"{{CSP_NONCE}}", "{{VERSION}}"} {
		if !strings.Contains(page, ph) {
			return "", fmt.Errorf("placeholder %s missing from generated page", ph)
		}
	}
	return page, nil
}

// renderLogin builds login.html: the shell with the minified login stylesheet,
// design tokens included. The result is an html/template the server executes,
// so it is parsed here too: a broken action fails the build, not sermod's start.
func renderLogin(srcDir string) (string, error) {
	page, err := readShell(srcDir, loginShellFilename)
	if err != nil {
		return "", err
	}
	if page, err = injectCSS(page, filepath.Join(srcDir, loginStylesFilename)); err != nil {
		return "", err
	}
	if !strings.Contains(page, loginNonceAction) {
		return "", fmt.Errorf("template action %s missing from the login page", loginNonceAction)
	}
	if _, err := template.New(loginShellFilename).Parse(page); err != nil {
		return "", fmt.Errorf("login page is not a valid template: %w", err)
	}
	return page, nil
}

func loadWatchPanels(path string) ([]watchPanelDescriptor, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is derived from the -src build flag of a developer tool, not untrusted input.
	if err != nil {
		return nil, fmt.Errorf("read watch panels %s: %w", path, err)
	}
	var panels []watchPanelDescriptor
	if err := json.Unmarshal(data, &panels); err != nil {
		return nil, fmt.Errorf("decode watch panels %s: %w", path, err)
	}
	if len(panels) == 0 {
		return nil, errors.New("no descriptors")
	}
	seen := make(map[string]bool, len(panels))
	for i := range panels {
		panel := &panels[i]
		if panel.Key == "" || panel.SectionID == "" || panel.RowsID == "" || len(panel.Columns) == 0 {
			return nil, errors.New("descriptor has missing key, section, rows or columns")
		}
		if seen[panel.Key] {
			return nil, fmt.Errorf("duplicate key %q", panel.Key)
		}
		seen[panel.Key] = true
	}
	return panels, nil
}

func watchPanelMarker(key string) string {
	return "<!--__SERMO_WATCH_PANEL:" + key + "__-->"
}

func renderWatchPanel(panel *watchPanelDescriptor) (string, error) {
	var out bytes.Buffer
	if err := watchPanelTemplate.Execute(&out, panel); err != nil {
		return "", fmt.Errorf("render watch panel %q: %w", panel.Key, err)
	}
	return out.String(), nil
}

func bundleJS(entry string) (string, error) {
	res := api.Build(api.BuildOptions{
		EntryPoints:       []string{entry},
		Bundle:            true,
		Format:            api.FormatIIFE,
		Target:            api.ES2020,
		Charset:           api.CharsetUTF8,
		MinifyWhitespace:  true,
		MinifyIdentifiers: true,
		MinifySyntax:      true,
		LogLevel:          api.LogLevelSilent,
		Write:             false,
	})
	js, err := single(res)
	if err != nil {
		return "", err
	}
	return normalizeVendoredJSLiterals(js), nil
}

func bundleCSS(entry string) (string, error) {
	res := api.Build(api.BuildOptions{
		EntryPoints:      []string{entry},
		Bundle:           true,
		Loader:           map[string]api.Loader{".css": api.LoaderCSS},
		Charset:          api.CharsetUTF8,
		MinifyWhitespace: true,
		MinifySyntax:     true,
		LogLevel:         api.LogLevelSilent,
		Write:            false,
	})
	return single(res)
}

func single(res api.BuildResult) (string, error) {
	if len(res.Errors) > 0 {
		msgs := api.FormatMessages(res.Errors, api.FormatMessagesOptions{Kind: api.ErrorMessage})
		return "", fmt.Errorf("%s", strings.Join(msgs, "\n"))
	}
	if len(res.OutputFiles) != webBuildExpectedOutputFiles {
		return "", fmt.Errorf("expected 1 output file, got %d", len(res.OutputFiles))
	}
	return string(res.OutputFiles[webBuildOutputFileIndex].Contents), nil
}

func normalizeVendoredJSLiterals(js string) string {
	// The pinned lit-html bundle carries regex whitespace sets with literal tabs
	// before newlines. Escaped forms keep the same runtime regex and avoid
	// trailing whitespace in the committed generated HTML.
	js = strings.ReplaceAll(js, "`[ \t\n\\f\\r]`", `"[ \t\n\f\r]"`)
	js = strings.ReplaceAll(js, "(?:[^ \t\n\\f\\r\"'\\`<>=]|", "(?:[^ \\t\\n\\f\\r\"'\\`<>=]|")
	return js
}
