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
	"flag"
	"fmt"
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
)

func main() {
	srcDir := flag.String("src", "internal/web/src", "dashboard source directory")
	out := flag.String("out", "internal/web/index.html", "generated output file")
	flag.Parse()

	if err := build(*srcDir, *out); err != nil {
		fmt.Fprintln(os.Stderr, "webbuild:", err)
		os.Exit(1)
	}
}

func build(srcDir, out string) error {
	// srcDir/out come from CLI flags driven by the Makefile, not untrusted
	// input; this is a developer build tool, so path-traversal taint is moot.
	shell, err := os.ReadFile(filepath.Join(srcDir, "index.html")) // #nosec G304
	if err != nil {
		return err
	}
	css, err := bundleCSS(filepath.Join(srcDir, "styles.css"))
	if err != nil {
		return fmt.Errorf("css: %w", err)
	}
	js, err := bundleJS(filepath.Join(srcDir, "app.js"))
	if err != nil {
		return fmt.Errorf("js: %w", err)
	}

	page := string(shell)
	if !strings.Contains(page, cssMarker) {
		return fmt.Errorf("css marker %q not found in shell", cssMarker)
	}
	if !strings.Contains(page, jsMarker) {
		return fmt.Errorf("js marker %q not found in shell", jsMarker)
	}
	page = strings.Replace(page, cssMarker, css, 1)
	page = strings.Replace(page, jsMarker, js, 1)

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

	return os.WriteFile(out, []byte(page), 0o600) // #nosec G304 G703
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
	if len(res.OutputFiles) != 1 {
		return "", fmt.Errorf("expected 1 output file, got %d", len(res.OutputFiles))
	}
	return string(res.OutputFiles[0].Contents), nil
}

func normalizeVendoredJSLiterals(js string) string {
	// The pinned lit-html bundle carries regex whitespace sets with literal tabs
	// before newlines. Escaped forms keep the same runtime regex and avoid
	// trailing whitespace in the committed generated HTML.
	js = strings.ReplaceAll(js, "`[ \t\n\\f\\r]`", `"[ \t\n\f\r]"`)
	js = strings.ReplaceAll(js, "(?:[^ \t\n\\f\\r\"'\\`<>=]|", "(?:[^ \\t\\n\\f\\r\"'\\`<>=]|")
	return js
}
