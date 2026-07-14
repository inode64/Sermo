package web

import (
	"os"
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
	page, err := assets.ReadFile(assetIndexHTML)
	if err != nil {
		t.Fatalf("read embedded %s: %v", assetIndexHTML, err)
	}
	doc, err := html.Parse(strings.NewReader(string(page)))
	if err != nil {
		t.Fatalf("parse %s: %v", assetIndexHTML, err)
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
		if !ok || nonce != templateNoncePlaceholder {
			t.Fatalf("<%s> nonce = %q, want %s", n.Data, nonce, templateNoncePlaceholder)
		}
	}

	if got := strings.Count(raw, templateNoncePlaceholder); got != 2 {
		t.Fatalf("%s count = %d, want 2", templateNoncePlaceholder, got)
	}
	if !strings.Contains(raw, templateVersionPlaceholder) {
		t.Fatalf("%s missing %s placeholder", assetIndexHTML, templateVersionPlaceholder)
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
	anchors := collectIndexShellAnchors(t)
	assertIndexShellIDs(t, anchors.ids)
	assertIndexShellSectionLinks(t, anchors.sectionLinks, anchors.sectionLinkLabels)
	assertIndexShellTable(t, anchors.headers, anchors.dialogs)
}

type indexShellAnchors struct {
	ids               map[string]bool
	headers           map[string]bool
	sectionLinks      map[string]string
	sectionLinkLabels map[string]string
	dialogs           int
}

func collectIndexShellAnchors(t *testing.T) indexShellAnchors {
	t.Helper()
	doc, _ := parsedIndex(t)
	anchors := indexShellAnchors{ids: map[string]bool{}, headers: map[string]bool{}, sectionLinks: map[string]string{}, sectionLinkLabels: map[string]string{}}
	walk(doc, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}
		if id, ok := attr(node, "id"); ok {
			anchors.ids[id] = true
		}
		switch node.DataAtom {
		case atom.Dialog:
			anchors.dialogs++
		case atom.Th:
			if node.FirstChild != nil && node.FirstChild.Type == html.TextNode {
				anchors.headers[strings.TrimSpace(node.FirstChild.Data)] = true
			}
		case atom.A:
			collectIndexSectionLink(node, &anchors)
		}
	})
	return anchors
}

func collectIndexSectionLink(node *html.Node, anchors *indexShellAnchors) {
	className, hasClass := attr(node, "class")
	if !hasClass || !strings.Contains(" "+className+" ", " section-jump ") {
		return
	}
	target, hasTarget := attr(node, "data-panel-target")
	href, hasHref := attr(node, "href")
	if !hasTarget || !hasHref {
		return
	}
	anchors.sectionLinks[target] = href
	var label strings.Builder
	walk(node, func(child *html.Node) {
		if child.Type == html.TextNode {
			label.WriteString(child.Data)
		}
	})
	anchors.sectionLinkLabels[target] = strings.TrimSpace(label.String())
}

func assertIndexShellIDs(t *testing.T, ids map[string]bool) {
	t.Helper()
	for _, id := range []string{
		"topbar", "section-nav", "favicon", "attention", "events", "target-search", "target-search-options",
		"services-section", "containers-section", "vms-section", "apps-section", "libraries-section", "watches-section", "events-section",
		"watch-controls", "mount-controls", "container-controls", "vm-controls", "container-rows", "vm-rows",
		"event-clear", "event-before", "event-reset-filters", "event-group", "event-more", "event-service", "event-watch", "event-kind", "event-status", "event-range",
		"state-compact-btn", "state-before", "app-rows", "library-rows", "locks-rows", "mount-search", "mount-category", "mount-filters", "mount-filter-count",
		"action-confirm", "confirm-no-cascade", "simple-confirm",
	} {
		if !ids[id] {
			t.Errorf("shell missing element id %q", id)
		}
	}
}

func assertIndexShellSectionLinks(t *testing.T, links, labels map[string]string) {
	t.Helper()
	for _, id := range []string{"services-section", "containers-section", "vms-section", "mounts-section", "apps-section", "libraries-section", "watches-section", "events-section", "locks-section", "notifiers-section", "daemon-section"} {
		if links[id] != "#"+id {
			t.Errorf("section nav link %q href = %q, want %q", id, links[id], "#"+id)
		}
	}
	if labels["mounts-section"] != "Mount units" {
		t.Errorf("mount section nav label = %q, want Mount units", labels["mounts-section"])
	}
}

func assertIndexShellTable(t *testing.T, headers map[string]bool, dialogs int) {
	t.Helper()
	if dialogs != 4 {
		t.Errorf("want 4 <dialog> elements, got %d", dialogs)
	}
	for _, header := range []string{"Uptime", "CPU total", "Memory", "FDs", "IO R/W", "State", "Type", "Group", "Processes", "Users", "Actions"} {
		if !headers[header] {
			t.Errorf("shell missing static <th> %q", header)
		}
	}
	if headers["Source"] {
		t.Errorf("shell still exposes static <th> Source")
	}
}

func TestSourceProvidesGlobalTargetSearch(t *testing.T) {
	src, err := os.ReadFile("src/app.js")
	if err != nil {
		t.Fatalf("read src/app.js: %v", err)
	}
	text := string(src)
	for _, marker := range []string{
		`function globalTargetRecords()`,
		`function clearGlobalTargetFilters(target)`,
		`function openGlobalTarget(target)`,
		`function submitGlobalTargetSearch()`,
		`history.replaceState(null, "", "#" + serviceExpansionKey(target.name))`,
		`record.value.toLowerCase().includes(query)`,
		`e.key.toLowerCase() === "k"`,
		`id="mount-row-${detailDomKey(m.name || m.path || "mount")}"`,
	} {
		if !strings.Contains(text, marker) {
			t.Errorf("source missing global target search marker %q", marker)
		}
	}
}

func TestEventFiltersUseGuidedSelects(t *testing.T) {
	doc, _ := parsedIndex(t)
	for _, id := range []string{"event-service", "event-watch", "event-kind", "event-status", "event-range"} {
		node := nodeByID(doc, id)
		if node == nil || node.DataAtom != atom.Select {
			t.Errorf("event filter %q is not a select", id)
		}
	}
}

func TestSourceUsesStableEventCursorPagination(t *testing.T) {
	src, err := os.ReadFile("src/app.js")
	if err != nil {
		t.Fatalf("read src/app.js: %v", err)
	}
	api, err := os.ReadFile("src/api.js")
	if err != nil {
		t.Fatalf("read src/api.js: %v", err)
	}
	apiText := string(api)
	for _, marker := range []string{
		`const apiQueryBeforeID = "before_id"`,
		`const apiQueryPage = "page"`,
	} {
		if !strings.Contains(apiText, marker) {
			t.Errorf("API module missing event pagination marker %q", marker)
		}
	}
	text := string(src)
	for _, marker := range []string{
		`params.set(apiQueryBeforeID, String(eventNextBeforeID))`,
		`function loadOlderEvents()`,
		`return e.id ?`,
	} {
		if !strings.Contains(text, marker) {
			t.Errorf("source missing event pagination marker %q", marker)
		}
	}
}

func TestSourceSeparatesAPIAndFormattingModules(t *testing.T) {
	app, err := os.ReadFile("src/app.js")
	if err != nil {
		t.Fatalf("read src/app.js: %v", err)
	}
	api, err := os.ReadFile("src/api.js")
	if err != nil {
		t.Fatalf("read src/api.js: %v", err)
	}
	format, err := os.ReadFile("src/format.js")
	if err != nil {
		t.Fatalf("read src/format.js: %v", err)
	}
	appText := string(app)
	for _, marker := range []string{`from "./api.js"`, `from "./format.js"`} {
		if !strings.Contains(appText, marker) {
			t.Errorf("app source missing module import %q", marker)
		}
	}
	for name, source := range map[string]string{"api": string(api), "format": string(format)} {
		if !strings.Contains(source, "export function") {
			t.Errorf("%s module exports no functions", name)
		}
	}
	for _, retired := range []string{"function csrfPostOptions()", "function fmtNum("} {
		if strings.Contains(appText, retired) {
			t.Errorf("app source still owns extracted helper %q", retired)
		}
	}
}

func TestSourceSharesWatchPanelDescriptorsWithBuilder(t *testing.T) {
	app, err := os.ReadFile("src/app.js")
	if err != nil {
		t.Fatalf("read src/app.js: %v", err)
	}
	descriptors, err := os.ReadFile("src/watch-panels.json")
	if err != nil {
		t.Fatalf("read src/watch-panels.json: %v", err)
	}
	if !strings.Contains(string(app), `import watchPanelDescriptors from "./watch-panels.json"`) {
		t.Fatal("app does not import shared watch panel descriptors")
	}
	for _, key := range []string{`"host"`} {
		if !strings.Contains(string(descriptors), `"key": `+key) {
			t.Errorf("watch panel descriptors missing key %s", key)
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

func hasDescendantAttr(root *html.Node, atomName atom.Atom, key, value string) bool {
	found := false
	walk(root, func(n *html.Node) {
		if found || n.Type != html.ElementNode || n.DataAtom != atomName {
			return
		}
		if got, ok := attr(n, key); ok && got == value {
			found = true
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

func bundledCSS(t *testing.T) string {
	t.Helper()
	doc, _ := parsedIndex(t)
	var css string
	walk(doc, func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Style && n.FirstChild != nil {
			css = n.FirstChild.Data
		}
	})
	if css == "" {
		t.Fatal("bundled style missing")
	}
	return css
}

func TestSourceLoadDefersWatchesWithoutStaleFastPathReference(t *testing.T) {
	src, err := os.ReadFile("src/app.js")
	if err != nil {
		t.Fatalf("read src/app.js: %v", err)
	}
	text := string(src)
	watchesFetch := strings.Index(text, `getJSONResult(apiWatchesPath, null)`)
	primaryRender := strings.Index(text, `renderStatus({`)
	if watchesFetch < 0 || watchesFetch < primaryRender {
		t.Fatalf("load() no longer defers api/watches")
	}
	if strings.Contains(text, "if (watches) renderWatches(watches);") {
		t.Fatalf("load() references stale watches binding before deferred fetch")
	}
}

func TestSourceLoadReportsPartialRefreshBeforeAdvancingFreshness(t *testing.T) {
	src, err := os.ReadFile("src/app.js")
	if err != nil {
		t.Fatalf("read src/app.js: %v", err)
	}
	text := string(src)
	for _, needle := range []string{
		`getJSONResult(apiServicesPath, null)`,
		`["watches", watchesResult]`,
		`["applications", appsResult]`,
		`["libraries", librariesResult]`,
		`["events", { ok: eventsOK }]`,
		`showPartialRefresh(failures)`,
		`fully updated ${fmtSince`,
	} {
		if !strings.Contains(text, needle) {
			t.Errorf("source missing partial-refresh marker %q", needle)
		}
	}
	failureCheck := strings.Index(text, "if (failures.length)")
	freshnessAdvance := strings.LastIndex(text, "lastRefresh = Date.now()")
	if failureCheck < 0 || freshnessAdvance < failureCheck {
		t.Fatalf("full-refresh timestamp advances before partial failures are handled")
	}
}

func TestSourceRendersBackendCacheObservationTimes(t *testing.T) {
	src, err := os.ReadFile("src/app.js")
	if err != nil {
		t.Fatalf("read src/app.js: %v", err)
	}
	text := string(src)
	for _, needle := range []string{
		"s.status_observed_at",
		"a.observed_at",
		"w.observed_at",
		"renderSLATimeline(w.segments, w.window, w.observed_at)",
		"const sampledMs = Date.parse(observedAt)",
	} {
		if !strings.Contains(text, needle) {
			t.Errorf("source missing cache-observation marker %q", needle)
		}
	}
}

func TestSourceFullyRefreshesExpandedServicesEveryDashboardPoll(t *testing.T) {
	src, err := os.ReadFile("src/app.js")
	if err != nil {
		t.Fatalf("read src/app.js: %v", err)
	}
	text := string(src)
	start := strings.Index(text, "function refreshExpandedServices(opts = {})")
	if start < 0 {
		t.Fatal("refreshExpandedServices source block not found")
	}
	end := strings.Index(text[start:], "async function refreshExpandedWatches()")
	if end < 0 {
		t.Fatal("refreshExpandedServices source block end not found")
	}
	body := text[start : start+end]
	if !strings.Contains(body, "Promise.all(keys.map(loadExpansionFor))") {
		t.Fatal("expanded services are not fully loaded on each dashboard refresh")
	}
	for _, marker := range []string{
		"expandedServicesPromise = refreshExpandedServices()",
		`["service details", { ok: expandedServicesOK }]`,
		`["watch details", { ok: expandedWatchesOK }]`,
		`["application details", { ok: expandedApplicationsOK }]`,
		"const results = await Promise.all(pending)",
		"return hydrateServiceDetail(detailData)",
		"const expLoading = new Map()",
	} {
		if !strings.Contains(text, marker) {
			t.Errorf("source missing coordinated detail-refresh marker %q", marker)
		}
	}
	for _, retired := range []string{"SVC_EXPAND_FULL_EVERY", "svcExpandRefreshTick", "refreshServiceExpansionLight", "periodicFull"} {
		if strings.Contains(text, retired) {
			t.Errorf("source still contains partial expansion refresh path %q", retired)
		}
	}
}

func TestSourceSerializesDashboardRefreshes(t *testing.T) {
	src, err := os.ReadFile("src/app.js")
	if err != nil {
		t.Fatalf("read src/app.js: %v", err)
	}
	text := string(src)
	for _, marker := range []string{
		"function load()",
		"async function runLoadQueue()",
		"await performLoad()",
		"async function performLoad()",
		"await load();\n    scheduleRefresh();",
	} {
		if !strings.Contains(text, marker) {
			t.Errorf("source missing serialized-refresh marker %q", marker)
		}
	}
	if strings.Contains(text, "setInterval(() => { if (document.hidden) return; load(); }") {
		t.Fatal("dashboard polling can still overlap through setInterval")
	}
}

func TestSourceUsesDashboardSnapshotWithGranularFallback(t *testing.T) {
	src, err := os.ReadFile("src/app.js")
	if err != nil {
		t.Fatalf("read src/app.js: %v", err)
	}
	text := string(src)
	for _, marker := range []string{
		"getJSONResult(dashboardAPI(daemonMetricWindow), null)",
		"if (aggregate.ok)",
		`getJSONResult(apiServicesPath, null)`,
		`snapshotResult(snapshot, "host_metrics", [])`,
	} {
		if !strings.Contains(text, marker) {
			t.Errorf("source missing aggregate-dashboard marker %q", marker)
		}
	}
}

func TestSourceMetricChartRendersZeroValuedSeries(t *testing.T) {
	src, err := os.ReadFile("src/app.js")
	if err != nil {
		t.Fatalf("read src/app.js: %v", err)
	}
	text := string(src)
	if strings.Contains(text, "if (maxV <= 0) return") {
		t.Fatalf("metric chart still treats all-zero samples as no data")
	}
	for _, needle := range []string{
		"if (!pts.length) return '<span class=\"muted\">No data yet for this window.</span>';",
		"const scaleMax = maxV > 0 ? maxV : 1;",
		"Service latency metric chart",
		"${label} runtime metric chart",
		"Daemon IO metric chart",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("metric chart missing zero-valued-series marker %q", needle)
		}
	}
}

func TestSourceKeepsMetricSelectionPerService(t *testing.T) {
	src, err := os.ReadFile("src/app.js")
	if err != nil {
		t.Fatalf("read src/app.js: %v", err)
	}
	text := string(src)
	for _, marker := range []string{
		`const serviceMetricStates = new Map()`,
		`function serviceMetricState(name)`,
		`serviceMetricStates: Object.fromEntries(serviceMetricStates)`,
		`data-window-service="${service || nothing}"`,
		`setMetricWin(val, windowBtn.dataset.windowService || "")`,
		`serviceMetricState(name).window !== win`,
	} {
		if !strings.Contains(text, marker) {
			t.Errorf("source missing per-service metric state marker %q", marker)
		}
	}
	for _, retired := range []string{`let metricCheck = ""`, `let metricWindow = "24h"`} {
		if strings.Contains(text, retired) {
			t.Errorf("source still contains global metric state %q", retired)
		}
	}
}

// TestIndexAccessibilityTargetSize pins WCAG 2.5.8 minimum hit targets in the
// committed CSS bundle (row toggles, table action buttons, event more/less).
func TestIndexAccessibilityTargetSize(t *testing.T) {
	css := strings.ReplaceAll(bundledCSS(t), " ", "")
	for _, needle := range []string{
		".row-toggle{",
		"min-height:24px",
		"min-width:24px",
		".event-msgbutton{",
		".actionsbutton{",
	} {
		if !strings.Contains(css, needle) {
			t.Errorf("bundled CSS missing target-size marker %q", needle)
		}
	}
}

// TestIndexAccessibilityForcedColors pins the Windows High Contrast Mode
// handling: the graphical meters whose value is carried only by colored
// width/segments must opt out of color flattening and keep a visible track
// border so they stay perceivable when author backgrounds collapse.
func TestIndexAccessibilityForcedColors(t *testing.T) {
	css := strings.ReplaceAll(bundledCSS(t), " ", "")
	for _, needle := range []string{
		"@media(forced-colors:active)",
		"forced-color-adjust:none",
		"1pxsolidCanvasText",
	} {
		if !strings.Contains(css, needle) {
			t.Errorf("bundled CSS missing forced-colors marker %q", needle)
		}
	}
}

func TestIndexWatchReadingLongValuesWrap(t *testing.T) {
	css := strings.ReplaceAll(bundledCSS(t), " ", "")
	for _, needle := range []string{
		".watch-reading-long{grid-column:1/-1}",
		".watch-reading-value{overflow-wrap:anywhere;word-break:break-word;white-space:normal}",
	} {
		if !strings.Contains(css, needle) {
			t.Errorf("bundled CSS missing long-reading wrap marker %q", needle)
		}
	}
	script := bundledScript(t)
	for _, needle := range []string{
		"issuer",
		"watch-reading-long",
		"watch-reading-value",
	} {
		if !strings.Contains(script, needle) {
			t.Errorf("bundled script missing long-reading marker %q", needle)
		}
	}
}

func TestIndexResponsiveTablesDoNotKeepDesktopMinWidth(t *testing.T) {
	css := strings.ReplaceAll(bundledCSS(t), " ", "")
	base := strings.Index(css, ".watch-table{min-width:72rem")
	responsive := strings.LastIndex(css, "@media(max-width:1420px){.services-table,.watch-table,.apps-table,.mount-table{min-width:0;max-width:100%}")
	if base < 0 {
		t.Fatal("bundled CSS missing watch-table desktop min-width")
	}
	if responsive < 0 {
		t.Fatal("bundled CSS missing responsive table min-width override")
	}
	if responsive < base {
		t.Fatal("responsive table override appears before desktop min-width and can be overridden")
	}
	for _, needle := range []string{
		"--topbar-h:0px",
		"top:var(--topbar-h)",
		".services-table.actions{min-width:7.5rem;max-width:11rem;white-space:normal}",
		".actionsbutton{margin-right:.25rem;margin-bottom:.25rem;",
		"#notifiers-sectionth:nth-child(3)",
		"#app-footer.footer-version{margin-left:0;flex-basis:100%;white-space:normal;overflow-wrap:anywhere;text-align:center}",
	} {
		if !strings.Contains(css, needle) {
			t.Errorf("bundled CSS missing responsive action marker %q", needle)
		}
	}
}

func TestIndexServiceActionsUseSinglePowerButton(t *testing.T) {
	script := bundledScript(t)
	for _, bad := range []string{"start-only", "start only", "data-no-cascade"} {
		if strings.Contains(script, bad) {
			t.Errorf("bundled script still contains removed service action marker %q", bad)
		}
	}
	for _, needle := range []string{
		"also applies to:",
		"can_reload",
		"service does not support reload",
		"service is already running",
		"service is already stopped",
		"Start service ",
		"Stop service ",
		"Reload service ",
	} {
		if !strings.Contains(script, needle) {
			t.Errorf("bundled script missing service action marker %q", needle)
		}
	}
}

func TestSourceCompactsRowActionsWithoutChangingDispatch(t *testing.T) {
	src, err := os.ReadFile("src/app.js")
	if err != nil {
		t.Fatalf("read src/app.js: %v", err)
	}
	text := string(src)
	for _, marker := range []string{
		`data-service-action="${action}"`,
		`data-watch-action="${actionUnmonitor}"`,
		`data-mount-action="${actionUmount}"`,
		`act(serviceAction.dataset.service || "", serviceAction.dataset.serviceAction || "")`,
	} {
		if !strings.Contains(text, marker) {
			t.Errorf("source missing compact action marker %q", marker)
		}
	}
}

// TestIndexAccessibilitySectionHeadings pins the per-section <h2> headings that
// let screen-reader users navigate the dashboard by heading. The <details>
// summaries cannot carry heading semantics (a summary's implicit button role
// makes its descendants presentational), so each major panel is preceded by a
// visually-hidden <h2>; the locks panel already ships a visible one.
func TestIndexAccessibilitySectionHeadings(t *testing.T) {
	doc, _ := parsedIndex(t)
	headings := map[string]bool{}
	walk(doc, func(n *html.Node) {
		if n.Type != html.ElementNode || n.DataAtom != atom.H2 {
			return
		}
		var sb strings.Builder
		walk(n, func(c *html.Node) {
			if c.Type == html.TextNode {
				sb.WriteString(c.Data)
			}
		})
		headings[strings.TrimSpace(sb.String())] = true
	})
	for _, want := range []string{
		"Services", "Containers", "Virtual machines", "Installed applications", "Installed libraries",
		"Host watches", "Events", "Mount units", "Notifiers",
		"Daemon / Engine settings",
	} {
		if !headings[want] {
			t.Errorf("missing section heading <h2> %q", want)
		}
	}
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
		"lock is still active",
		"SLA timeline data",
		"preflight not available for this action",
		"service is disabled in configuration",
		"Confirm:",
		"Start service ",
		"Monitor watch ",
		"tile-${t}-gauge",
		"service details",
		"Run preflight checks for ",
		"Show full event message",
		"Open service ",
		"group (",
	} {
		if !strings.Contains(script, needle) {
			t.Errorf("bundled script missing a11y marker %q", needle)
		}
	}
}

// TestIndexAccessibilityShell pins structural WCAG helpers in the static HTML
// shell: page language, skip link, live regions, labelled filter groups, and
// table captions with column scope.
//
//nolint:gocognit // The test intentionally keeps the complete shell accessibility contract in one reviewable specification.
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
	for _, id := range []string{"event-before-hint", "state-before-hint"} {
		if nodeByID(doc, id) == nil {
			t.Errorf("shell missing %q", id)
		}
	}

	for _, id := range []string{
		"svc-filters", "container-filters", "vm-filters", "watch-filters", "app-filters",
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
	if svcFilters := nodeByID(doc, "svc-filters"); svcFilters == nil {
		t.Fatal("shell missing #svc-filters")
	} else if !hasDescendantAttr(svcFilters, atom.Button, "data-f", "monitored") {
		t.Fatal(`#svc-filters missing monitored state button`)
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
	if captions < 11 {
		t.Errorf("want at least 11 <caption> elements, got %d", captions)
	}
	if thMissingScope > 0 {
		t.Errorf("want scope=col on every shell <th>, %d missing", thMissingScope)
	}

	for _, spec := range []struct {
		id     string
		live   string
		atomic string
	}{
		{"system-status", "off", ""},
		{"statusbar", "off", ""},
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
		if spec.live == "off" {
			if role, ok := attr(el, "role"); ok {
				t.Errorf("#%s role = %q, want no implicit live-region role", spec.id, role)
			}
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

	if panicBtn := nodeByID(doc, "panic-btn"); panicBtn != nil {
		if got, ok := attr(panicBtn, "aria-label"); !ok || !strings.Contains(got, "panic mode") {
			t.Errorf(`#panic-btn aria-label = %q, want panic mode wording`, got)
		}
	} else {
		t.Error(`shell missing #panic-btn`)
	}

	if preflightBtn := nodeByID(doc, "confirm-preflight-btn"); preflightBtn != nil {
		if got, ok := attr(preflightBtn, "aria-label"); !ok || got != "Run preflight checks" {
			t.Errorf(`#confirm-preflight-btn aria-label = %q, want "Run preflight checks"`, got)
		}
		if nodeByID(doc, "confirm-preflight-hint") == nil {
			t.Error(`shell missing #confirm-preflight-hint`)
		}
	} else {
		t.Error(`shell missing #confirm-preflight-btn`)
	}

	if cancel := nodeByID(doc, "panic-cancel-btn"); cancel != nil {
		if got, ok := attr(cancel, "aria-label"); !ok || got != "Cancel panic mode change" {
			t.Errorf(`#panic-cancel-btn aria-label = %q, want "Cancel panic mode change"`, got)
		}
	} else {
		t.Error(`shell missing #panic-cancel-btn`)
	}

	if cancel := nodeByID(doc, "confirm-cancel-btn"); cancel != nil {
		if got, ok := attr(cancel, "aria-label"); !ok || got != "Cancel service operation" {
			t.Errorf(`#confirm-cancel-btn aria-label = %q, want "Cancel service operation"`, got)
		}
	} else {
		t.Error(`shell missing #confirm-cancel-btn`)
	}

	if actionBtn := nodeByID(doc, "confirm-action-btn"); actionBtn != nil {
		if got, ok := attr(actionBtn, "aria-label"); !ok || got != "Confirm service operation" {
			t.Errorf(`#confirm-action-btn aria-label = %q, want "Confirm service operation"`, got)
		}
	} else {
		t.Error(`shell missing #confirm-action-btn`)
	}

	if refreshSel := nodeByID(doc, "refresh-select"); refreshSel != nil {
		if got, ok := attr(refreshSel, "aria-label"); !ok || got != "Dashboard auto-refresh interval" {
			t.Errorf(`#refresh-select aria-label = %q, want "Dashboard auto-refresh interval"`, got)
		}
	} else {
		t.Error(`shell missing #refresh-select`)
	}

	if refreshBtn := nodeByID(doc, "refresh-now"); refreshBtn != nil {
		if got, ok := attr(refreshBtn, "aria-label"); !ok || got != "Refresh dashboard now" {
			t.Errorf(`#refresh-now aria-label = %q, want "Refresh dashboard now"`, got)
		}
	} else {
		t.Error(`shell missing #refresh-now`)
	}

	if resetFilters := nodeByID(doc, "event-reset-filters"); resetFilters != nil {
		if got, ok := attr(resetFilters, "aria-label"); !ok || got != "Reset event filters" {
			t.Errorf(`#event-reset-filters aria-label = %q, want "Reset event filters"`, got)
		}
	} else {
		t.Error(`shell missing #event-reset-filters`)
	}

	if watches := nodeByID(doc, "watches-section"); watches != nil {
		if cls, ok := attr(watches, "class"); !ok || !strings.Contains(cls, "panel-hidden") {
			t.Errorf(`#watches-section class = %q, want panel-hidden`, cls)
		}
	} else {
		t.Error(`shell missing #watches-section`)
	}

	if eventClear := nodeByID(doc, "event-clear"); eventClear != nil {
		if cls, ok := attr(eventClear, "class"); !ok || !strings.Contains(cls, "admin-hidden") {
			t.Errorf(`#event-clear class = %q, want admin-hidden`, cls)
		}
	} else {
		t.Error(`shell missing #event-clear`)
	}
	if eventGroup := nodeByID(doc, "event-group"); eventGroup != nil {
		if _, checked := attr(eventGroup, "checked"); checked {
			t.Error("#event-group is checked by default")
		}
	} else {
		t.Error("shell missing #event-group")
	}

	for _, spec := range []struct {
		id    string
		label string
	}{
		{"event-clear", "Clear event log"},
		{"state-compact-btn", "Compact persisted state"},
		{"reload-btn", "Reload configuration"},
		{"simple-confirm-ok", "Confirm action"},
	} {
		el := nodeByID(doc, spec.id)
		if el == nil {
			t.Errorf("shell missing admin button id %q", spec.id)
			continue
		}
		if got, ok := attr(el, "aria-label"); !ok || got != spec.label {
			t.Errorf("#%s aria-label = %q, want %q", spec.id, got, spec.label)
		}
	}
}
