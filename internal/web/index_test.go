package web

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// The dashboard markup is generated from internal/web/src by `make web` and
// committed as index.html (esbuild minifies the JS/CSS, so asserting on JS
// function names or formatted CSS is meaningless). These tests parse the
// generated document structurally instead: they pin the server contract
// (placeholders, the single nonce'd <style>/<script>), CSP hygiene (no inline
// handlers, no eval in the bundle), and the presence of the static shell
// anchors the JS and server depend on.

// parsedIndex parses the embedded, generated index.html once per test.
func parsedIndex(t *testing.T) (*html.Node, string) {
	t.Helper()
	page, err := assets.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}
	doc, err := html.Parse(strings.NewReader(string(page)))
	if err != nil {
		t.Fatalf("parse index.html: %v", err)
	}
	return doc, string(page)
}

// walk visits every node in the tree.
func walk(n *html.Node, fn func(*html.Node)) {
	fn(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, fn)
	}
}

func attr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

// TestIndexServerContract pins what internal/web/server.go fills in per request:
// a single nonce'd <style> and <script>, both carrying the {{CSP_NONCE}}
// placeholder, plus the {{VERSION}} placeholder in the footer.
func TestIndexServerContract(t *testing.T) {
	doc, raw := parsedIndex(t)

	var styles, scripts []*html.Node
	walk(doc, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		switch n.DataAtom {
		case atom.Style:
			styles = append(styles, n)
		case atom.Script:
			scripts = append(scripts, n)
		}
	})

	if len(styles) != 1 {
		t.Fatalf("want exactly 1 <style>, got %d", len(styles))
	}
	if len(scripts) != 1 {
		t.Fatalf("want exactly 1 <script>, got %d", len(scripts))
	}
	for _, n := range append(append([]*html.Node{}, styles...), scripts...) {
		nonce, ok := attr(n, "nonce")
		if !ok || nonce != "{{CSP_NONCE}}" {
			t.Fatalf("<%s> nonce = %q, want {{CSP_NONCE}}", n.Data, nonce)
		}
	}

	if got := strings.Count(raw, "{{CSP_NONCE}}"); got != 2 {
		t.Fatalf("{{CSP_NONCE}} count = %d, want 2", got)
	}
	if !strings.Contains(raw, "{{VERSION}}") {
		t.Fatalf("index.html missing {{VERSION}} placeholder")
	}
}

// TestIndexCSPHygiene guards the security posture the strict script-src nonce
// relies on: no inline on*= event handlers (all wiring goes through delegated
// listeners), and no eval/new Function in the bundle.
func TestIndexCSPHygiene(t *testing.T) {
	doc, _ := parsedIndex(t)

	var script string
	walk(doc, func(n *html.Node) {
		if n.Type == html.ElementNode {
			for _, a := range n.Attr {
				if strings.HasPrefix(a.Key, "on") {
					t.Errorf("inline event handler %q on <%s> breaks the CSP nonce model", a.Key, n.Data)
				}
			}
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.Script && n.FirstChild != nil {
			script = n.FirstChild.Data
		}
	})

	for _, bad := range []string{"eval(", "new Function("} {
		if strings.Contains(script, bad) {
			t.Errorf("bundled script contains %q", bad)
		}
	}
}

// TestIndexShellAnchors checks the static shell still carries the element ids,
// dialogs, and table headers that the JS and server reference. These survive
// minification because they live in the src/index.html shell, not the bundle.
func TestIndexShellAnchors(t *testing.T) {
	doc, _ := parsedIndex(t)

	ids := map[string]bool{}
	headers := map[string]bool{}
	dialogs := 0
	walk(doc, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		if id, ok := attr(n, "id"); ok {
			ids[id] = true
		}
		switch n.DataAtom {
		case atom.Dialog:
			dialogs++
		case atom.Th:
			if n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
				headers[strings.TrimSpace(n.FirstChild.Data)] = true
			}
		}
	})

	wantIDs := []string{
		"topbar", "favicon", "attention", "events",
		"services-section", "apps-section", "watches-section", "events-section",
		"storage-controls", "network-controls",
		"event-clear", "event-before", "event-reset-filters", "activity-clear",
		"state-compact-btn", "state-before", "app-rows", "locks-rows",
		"action-confirm", "confirm-no-cascade", "simple-confirm",
	}
	for _, id := range wantIDs {
		if !ids[id] {
			t.Errorf("shell missing element id %q", id)
		}
	}

	// action-confirm, panic-confirm and simple-confirm modals.
	if dialogs != 3 {
		t.Errorf("want 3 <dialog> elements, got %d", dialogs)
	}

	for _, h := range []string{"Uptime", "CPU total", "Memory", "IO R/W", "State", "Type", "Actions"} {
		if !headers[h] {
			t.Errorf("shell missing static <th> %q", h)
		}
	}
}

func nodeByID(doc *html.Node, id string) *html.Node {
	var found *html.Node
	walk(doc, func(n *html.Node) {
		if found != nil || n.Type != html.ElementNode {
			return
		}
		if v, ok := attr(n, "id"); ok && v == id {
			found = n
		}
	})
	return found
}

func bundledScript(t *testing.T) string {
	t.Helper()
	doc, _ := parsedIndex(t)
	var script string
	walk(doc, func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Script && n.FirstChild != nil {
			script = n.FirstChild.Data
		}
	})
	if script == "" {
		t.Fatal("bundled script missing")
	}
	return script
}

// TestIndexAccessibilityBundle pins a11y string markers that survive esbuild
// minification in the committed dashboard bundle (attribute names, SR hints,
// disclosure wiring). It does not execute JS or assert on mangled identifiers.
func TestIndexAccessibilityBundle(t *testing.T) {
	script := bundledScript(t)
	for _, needle := range []string{
		"aria-controls",
		"aria-describedby",
		"aria-expanded",
		"data-event-toggle",
		"operation in progress",
		"Disconnected — retrying",
		"services panel",
		"-msg",
		"-panel",
		"visually-hidden",
		"watch is starting",
		"event-grp-panel",
		"Graph time window",
		"Latency check",
		"chart-data",
		"Chart data",
	} {
		if !strings.Contains(script, needle) {
			t.Errorf("bundled script missing a11y marker %q", needle)
		}
	}
}

// TestIndexAccessibilityShell pins structural WCAG helpers in the static HTML
// shell: page language, skip link, live regions, labelled filter groups, and
// table captions with column scope.
func TestIndexAccessibilityShell(t *testing.T) {
	doc, _ := parsedIndex(t)

	var htmlLang string
	var skipHref string
	var mainTab string
	walk(doc, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		switch n.DataAtom {
		case atom.Html:
			if lang, ok := attr(n, "lang"); ok {
				htmlLang = lang
			}
		case atom.A:
			if cls, ok := attr(n, "class"); ok && strings.Contains(cls, "skip-link") {
				skipHref, _ = attr(n, "href")
			}
		case atom.Main:
			if id, ok := attr(n, "id"); ok && id == "main-content" {
				mainTab, _ = attr(n, "tabindex")
			}
		}
	})
	if htmlLang != "en" {
		t.Errorf(`<html lang> = %q, want "en"`, htmlLang)
	}
	if skipHref != "#main-content" {
		t.Errorf("skip link href = %q, want #main-content", skipHref)
	}
	if mainTab != "-1" {
		t.Errorf(`<main id="main-content"> tabindex = %q, want "-1"`, mainTab)
	}

	attention := nodeByID(doc, "attention")
	if attention == nil {
		t.Fatal(`shell missing #attention`)
	}
	for _, pair := range [][2]string{{"role", "alert"}, {"aria-live", "assertive"}, {"aria-atomic", "true"}} {
		if got, ok := attr(attention, pair[0]); !ok || got != pair[1] {
			t.Errorf(`#attention %s = %q, want %q`, pair[0], got, pair[1])
		}
	}

	eventControls := nodeByID(doc, "event-controls")
	if eventControls == nil {
		t.Fatal(`shell missing #event-controls`)
	}
	if role, ok := attr(eventControls, "role"); !ok || role != "group" {
		t.Errorf(`#event-controls role = %q, want "group"`, role)
	}
	if label, ok := attr(eventControls, "aria-label"); !ok || label != "Filter events" {
		t.Errorf(`#event-controls aria-label = %q, want "Filter events"`, label)
	}
	if nodeByID(doc, "search-shortcut-hint") == nil {
		t.Fatal(`shell missing #search-shortcut-hint`)
	}

	for _, id := range []string{
		"svc-filters", "storage-filters", "network-filters", "watch-filters", "app-filters",
	} {
		el := nodeByID(doc, id)
		if el == nil {
			t.Errorf("shell missing filter group id %q", id)
			continue
		}
		role, ok := attr(el, "role")
		if !ok || role != "group" {
			t.Errorf("#%s role = %q, want group", id, role)
		}
		if label, ok := attr(el, "aria-label"); !ok || label == "" {
			t.Errorf("#%s missing aria-label", id)
		}
	}

	captions := 0
	thMissingScope := 0
	walk(doc, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		switch n.DataAtom {
		case atom.Caption:
			captions++
		case atom.Th:
			if scope, ok := attr(n, "scope"); !ok || scope != "col" {
				thMissingScope++
			}
		}
	})
	if captions < 9 {
		t.Errorf("want at least 9 <caption> elements, got %d", captions)
	}
	if thMissingScope > 0 {
		t.Errorf("want scope=col on every shell <th>, %d missing", thMissingScope)
	}

	for _, spec := range []struct {
		id     string
		live   string
		atomic string
	}{
		{"system-status", "polite", "false"},
		{"statusbar", "polite", "false"},
		{"err", "polite", ""},
		{"op-live", "polite", ""},
	} {
		el := nodeByID(doc, spec.id)
		if el == nil {
			t.Errorf("shell missing live region id %q", spec.id)
			continue
		}
		if live, ok := attr(el, "aria-live"); !ok || live != spec.live {
			t.Errorf("#%s aria-live = %q, want %q", spec.id, live, spec.live)
		}
		if spec.atomic != "" {
			if atomic, ok := attr(el, "aria-atomic"); !ok || atomic != spec.atomic {
				t.Errorf("#%s aria-atomic = %q, want %q", spec.id, atomic, spec.atomic)
			}
		}
	}

	overview := nodeByID(doc, "overview")
	if overview == nil {
		t.Fatal(`shell missing #overview`)
	}
	if role, ok := attr(overview, "role"); !ok || role != "region" {
		t.Errorf(`#overview role = %q, want "region"`, role)
	}
	if label, ok := attr(overview, "aria-label"); !ok || label != "Overview" {
		t.Errorf(`#overview aria-label = %q, want "Overview"`, label)
	}

	footer := nodeByID(doc, "app-footer")
	if footer == nil {
		t.Fatal(`shell missing #app-footer`)
	}
	if role, ok := attr(footer, "role"); !ok || role != "contentinfo" {
		t.Errorf(`#app-footer role = %q, want "contentinfo"`, role)
	}

	panicBanner := nodeByID(doc, "panic-banner")
	if panicBanner == nil {
		t.Fatal(`shell missing #panic-banner`)
	}
	if live, ok := attr(panicBanner, "aria-live"); !ok || live != "assertive" {
		t.Errorf(`#panic-banner aria-live = %q, want "assertive"`, live)
	}

	brandDot := nodeByID(doc, "brand-dot")
	if brandDot == nil {
		t.Fatal(`shell missing #brand-dot`)
	}
	if hidden, ok := attr(brandDot, "aria-hidden"); !ok || hidden != "true" {
		t.Errorf(`#brand-dot aria-hidden = %q, want "true"`, hidden)
	}

	if nodeByID(doc, "simple-confirm") == nil {
		t.Error(`shell missing #simple-confirm dialog`)
	}
}
