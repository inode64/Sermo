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
		"/sla?since=",
		"loadSeries(",
		"drawChart(points)",
		"summarizeSLA(",
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
		"status-warning",
		"<th>Uptime</th>",
		"<th>CPU total</th>",
		"<th>Memory</th>",
		"<th>IO R/W</th>",
		"function serviceCpuCell(s)",
		"function loadServiceRuntimeMetrics(name)",
		`api/services/${encodeURIComponent(name)}/runtime?since=${metricWindow}`,
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
