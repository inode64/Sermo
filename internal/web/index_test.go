package web

import (
	"strings"
	"testing"
)

func TestIndexHTMLServiceProcessMetricsLayout(t *testing.T) {
	page, err := assets.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}
	html := string(page)

	for _, forbidden := range []string{
		"Availability · last 24h",
		"Availability (SLA)",
		"<h2>Availability</h2>",
		`class="sla"`,
		"chart-summary",
		"loadSeries(",
		"drawChart(points)",
		"1-core peak",
		"CPU/core",
		"exe unresolved",
		"unresolved exe",
		"exe is unresolved",
		"Config Review",
		"config-render",
		"config-diff",
		"config-meta",
		"data-config-render",
		"data-config-diff",
		"loadConfigRender(",
		"loadConfigDiff(",
		"/config/diff",
		`Last event<span class="sort-ind"`,
		`id="detail"`,
		"data-detail-service",
		"data-close-detail",
		"function detail(",
		"function renderDetail(",
		"function closeDetail(",
		"<th>PPID</th>",
		`style="width:auto; max-width:100%; font-size:.85rem;"`,
		`<h2>SLA <span class="muted">${winButtons(metricWins, metricWindow, "setMetricWin")}</span></h2>`,
		"sla-layout",
		"sla-chart-title",
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("index.html still contains %q", forbidden)
		}
	}

	wantProcessHead := `<th>CPU</th><th title="CPU used by this process, normalized to one core">Core peak</th><th>Mem</th>`
	if !strings.Contains(html, wantProcessHead) {
		t.Fatalf("index.html missing process CPU/core peak columns")
	}
	if !strings.Contains(html, "function procCpuCells(p)") {
		t.Fatalf("index.html missing process CPU/core peak cell renderer")
	}
	if !strings.Contains(html, "function procLabel(p)") || !strings.Contains(html, "function procCmd(p)") {
		t.Fatalf("index.html missing process command fallback renderer")
	}
	if !strings.Contains(html, "function processRows(procs)") || !strings.Contains(html, "function procTreeLabel(row)") {
		t.Fatalf("index.html missing process tree renderer")
	}
	if !strings.Contains(html, ".proc-tree") || !strings.Contains(html, "proc-branch") {
		t.Fatalf("index.html missing process tree styles")
	}
	if !strings.Contains(html, "function renderServiceDetail(d)") || !strings.Contains(html, "data-service-expand") {
		t.Fatalf("index.html missing inline service detail renderer")
	}
	if !strings.Contains(html, "<th>CMD</th>") || strings.Contains(html, "<th>Exe</th>") {
		t.Fatalf("index.html should label the process command column as CMD")
	}
	if !strings.Contains(html, ".truncate") || !strings.Contains(html, "check-message") {
		t.Fatalf("index.html missing truncation styles for CMD/check messages")
	}
	for _, want := range []string{
		`data-app-sort="state"`,
		"function displayName(item)",
		"function appStatusCell(a)",
		"state-warning",
		"row-warning",
		"row-failing",
		"<th>Uptime</th>",
		"<th>CPU total</th>",
		"<th>Memory</th>",
		"<th>IO R/W</th>",
		".service-detail table { width: 100%;",
		"let expDetailCache = {}",
		"const SVC_EXPAND_FULL_EVERY = 6",
		"const EVENT_FILTER_DEBOUNCE_MS = 300",
		"function scheduleLoadEvents()",
		"function flushLoadEvents()",
		"function refreshExpandedServices(opts = {})",
		"function refreshServiceExpansionLight(key)",
		"function isWatchAttention(w)",
		`target === "failed-watches"`,
		"function serviceRowParts(s)",
		"function svcRenderedStructure(list)",
		"function patchVisibleServiceRows(list)",
		"const SVC_PATCH_MIN = 50",
		"function serviceCpuCell(s)",
		"function loadServiceRuntimeMetrics(name)",
		"function loadServiceSLA(name)",
		"function drawSLAChart(points, win)",
		`id="diag-clean"`,
		"function cleanDiagnostics()",
		`api/diagnostics/clean`,
		"removes stale control state for unconfigured targets; metric/SLA/event history is kept",
		`r="3" fill="#1f6feb"`,
		`api/services/${encodeURIComponent(name)}/sla?since=${metricWindow}`,
		`aria-label="SLA timeline"`,
		`width="100%" role="img" aria-label="SLA timeline"`,
		`api/services/${encodeURIComponent(name)}/runtime?since=${metricWindow}`,
		"function renderSLAWindows(wins, compact)",
		`<div class="metric-panel metric-panel-wide">`,
		`<span class="metric-title">SLA timeline</span>`,
		`<th>SLA</th>`,
		`class="app-sla"`,
		`<h2>General data</h2>`,
		`<h2>Graphs <span class="muted">${winButtons(metricWins, metricWindow, "setMetricWin")}</span></h2>`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("index.html missing %q", want)
		}
	}

	if strings.Contains(html, `<th>Unit</th>
        <th class="sortable" data-sort="state"`) ||
		strings.Contains(html, `<th>Interval</th><th>Policy</th><th>Locks</th>`) ||
		strings.Contains(html, `<th>Next remediation</th>
        <th class="actions">Actions</th>`) {
		t.Fatalf("index.html still contains old services table columns")
	}
}
