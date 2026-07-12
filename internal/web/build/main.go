// Command webbuild bundles the dashboard sources in internal/web/src into the
// embedded internal/web/index.html.
//
// It runs esbuild in-process via its Go API (github.com/evanw/esbuild/pkg/api)
// — no Node, no npm, no spawned process — bundling src/app.js (and the modules
// it imports, including the vendored lit-html) into a single minified IIFE and
// minifying src/styles.css, then injecting both into the src/index.html shell.
//
// The {{CSP_NONCE}} (style + script) and {{VERSION}} placeholders are part of
// the shell and are left untouched for internal/web/server.go to fill per
// request. internal/web/index.html is a generated, committed artifact; run
// `make web` after editing anything under internal/web/src and commit the
// result. `make web-check` fails CI if the committed file is stale.
package main

import (
	"bytes"
	"encoding/json"
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
	FootnotePrefix    string             `json:"footnotePrefix"`
	FootnoteCode      string             `json:"footnoteCode"`
	FootnoteSuffix    string             `json:"footnoteSuffix"`
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
  <p class="muted panel-footnote">{{if .FootnoteCode}}{{.FootnotePrefix}}<code>{{.FootnoteCode}}</code>{{.FootnoteSuffix}}{{else}}{{.Footnote}}{{end}}</p>
</details>`))

func main() {
	srcDir := flag.String("src", "internal/web/src", "dashboard source directory")
	out := flag.String("out", "internal/web/index.html", "generated output file")
	flag.Parse()

	if err := build(*srcDir, *out); err != nil {
		fmt.Fprintln(os.Stderr, "webbuild:", err)
		os.Exit(webBuildFailureExitCode)
	}
}

func build(srcDir, out string) error {
	// srcDir/out come from CLI flags driven by the Makefile, not untrusted
	// input; this is a developer build tool, so path-traversal taint is moot.
	shell, err := os.ReadFile(filepath.Join(srcDir, webBuildShellFilename)) // #nosec G304
	if err != nil {
		return fmt.Errorf("read shell %s: %w", webBuildShellFilename, err)
	}
	watchPanels, err := loadWatchPanels(filepath.Join(srcDir, watchPanelsFilename))
	if err != nil {
		return fmt.Errorf("watch panels: %w", err)
	}
	css, err := bundleCSS(filepath.Join(srcDir, webBuildStylesFilename))
	if err != nil {
		return fmt.Errorf("css: %w", err)
	}
	js, err := bundleJS(filepath.Join(srcDir, webBuildScriptFilename))
	if err != nil {
		return fmt.Errorf("js: %w", err)
	}

	page := string(shell)
	for _, panel := range watchPanels {
		marker := watchPanelMarker(panel.Key)
		if strings.Count(page, marker) != webBuildReplaceOnce {
			return fmt.Errorf("watch panel marker %q must occur once", marker)
		}
		markup, err := renderWatchPanel(panel)
		if err != nil {
			return fmt.Errorf("watch panel %q: %w", panel.Key, err)
		}
		page = strings.Replace(page, marker, markup, webBuildReplaceOnce)
	}
	if !strings.Contains(page, cssMarker) {
		return fmt.Errorf("css marker %q not found in shell", cssMarker)
	}
	if !strings.Contains(page, jsMarker) {
		return fmt.Errorf("js marker %q not found in shell", jsMarker)
	}
	page = strings.Replace(page, cssMarker, css, webBuildReplaceOnce)
	page = strings.Replace(page, jsMarker, js, webBuildReplaceOnce)

	// A bundle that smuggled in a literal </script> or </style> would break the
	// inline blocks; esbuild never emits one, but guard regardless.
	if strings.Contains(js, "</script") {
		return fmt.Errorf("js bundle contains </script")
	}
	if strings.Contains(css, "</style") {
		return fmt.Errorf("css bundle contains </style")
	}
	// The server fills these per request; they must survive the build.
	for _, ph := range []string{"{{CSP_NONCE}}", "{{VERSION}}"} {
		if !strings.Contains(page, ph) {
			return fmt.Errorf("placeholder %s missing from generated page", ph)
		}
	}

	if err := os.WriteFile(out, []byte(page), webBuildOutputFileMode); err != nil { // #nosec G304 G703
		return fmt.Errorf("write %s: %w", out, err)
	}
	return nil
}

func loadWatchPanels(path string) ([]watchPanelDescriptor, error) {
	data, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		return nil, fmt.Errorf("read watch panels %s: %w", path, err)
	}
	var panels []watchPanelDescriptor
	if err := json.Unmarshal(data, &panels); err != nil {
		return nil, fmt.Errorf("decode watch panels %s: %w", path, err)
	}
	if len(panels) == 0 {
		return nil, fmt.Errorf("no descriptors")
	}
	seen := make(map[string]bool, len(panels))
	for _, panel := range panels {
		if panel.Key == "" || panel.SectionID == "" || panel.RowsID == "" || len(panel.Columns) == 0 {
			return nil, fmt.Errorf("descriptor has missing key, section, rows or columns")
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

func renderWatchPanel(panel watchPanelDescriptor) (string, error) {
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
