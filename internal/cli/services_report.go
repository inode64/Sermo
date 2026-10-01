package cli

import (
	"context"
	"fmt"
	"html"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"sermo/internal/appinspect"
	"sermo/internal/config"
	"sermo/internal/metrics"
	"sermo/internal/notify"
)

type servicesReportStats struct {
	Total        int
	Installed    int
	OK           int
	Issues       int
	NotInstalled int
	VersionKnown int
}

const (
	servicesReportDateLayout = "2006-01-02 15:04 MST"
	servicesReportFontSans   = "Arial,Helvetica,sans-serif"
	servicesReportFontMono   = "Menlo,Consolas,monospace"
)

const (
	servicesReportColorPageBG      = "#f4f6fb"
	servicesReportColorText        = "#182230"
	servicesReportColorPanel       = "#ffffff"
	servicesReportColorFrameBorder = "#dfe5ef"
	servicesReportColorBorder      = "#e2e8f0"
	servicesReportColorHeaderBG    = "#0f172a"
	servicesReportColorHeaderHint  = "#93c5fd"
	servicesReportColorHeaderMeta  = "#cbd5e1"
	servicesReportColorTableHeadBG = "#f8fafc"
	servicesReportColorSecondary   = "#475569"
	servicesReportColorMuted       = "#64748b"
	servicesReportColorOK          = "#16a34a"
	servicesReportColorIssue       = "#dc2626"
	servicesReportColorInfo        = "#2563eb"
	servicesReportColorOKBadgeBG   = "#dcfce7"
	servicesReportColorBadBadgeBG  = "#fee2e2"
	servicesReportColorMuteBadgeBG = "#f1f5f9"
)

func buildReportNotifiers(cfg *config.Config) (map[string]notify.Notifier, []string) {
	return notify.Build(cfg.Notifiers(), notify.WithoutTemplates())
}

func buildConfiguredNotifiers(cfg *config.Config) (map[string]notify.Notifier, []string) {
	return notify.Build(cfg.Notifiers(), notify.WithTemplateDir(cfg.Global.TemplateDir()))
}

// sendServicesReport delivers one services report through the notifiers named
// by --notify, returning their effective names.
func (a App) sendServicesReport(ctx context.Context, opts options, cfg *config.Config, msg notify.Message) ([]string, int) {
	registry, warnings := a.BuildReportNotifiers(cfg)
	for _, warning := range warnings {
		fmt.Fprintf(a.Stderr, cliWarningFormat, warning)
	}
	selected, names, err := selectServicesReportNotifiers(opts.notifyNames, registry)
	if err != nil {
		return nil, a.commandUsageError(commandServices, err.Error())
	}
	if len(selected) == 0 {
		return nil, a.fail(opts, "services --notify selected no enabled notifiers")
	}

	sendCtx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()
	for _, n := range selected {
		if err := n.Send(sendCtx, msg); err != nil {
			return nil, a.fail(opts, fmt.Sprintf("send services report to %s: %v", n.Name(), err))
		}
	}
	return names, exitSuccess
}

func selectServicesReportNotifiers(selection []string, registry map[string]notify.Notifier) ([]notify.Notifier, []string, error) {
	if len(selection) == 0 {
		return nil, nil, nil
	}
	names := servicesReportNotifierNames(selection, registry)
	seen := map[string]struct{}{}
	selected := make([]notify.Notifier, 0, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		n, ok := registry[name]
		if !ok {
			return nil, nil, fmt.Errorf("services --notify references unknown or disabled notifier %q", name)
		}
		seen[name] = struct{}{}
		selected = append(selected, n)
	}
	selected = notify.PreferWall(selected)
	outNames := make([]string, 0, len(selected))
	for _, notifier := range selected {
		outNames = append(outNames, notifier.Name())
	}
	return selected, outNames, nil
}

func servicesReportNotifierNames(selection []string, registry map[string]notify.Notifier) []string {
	if slices.Contains(selection, commandArgAll) {
		return slices.Sorted(maps.Keys(registry))
	}
	return selection
}

// reportTone classifies one report row for its badge colour and the
// distribution bar.
type reportTone int

const (
	reportToneOK reportTone = iota
	reportToneIssue
	reportToneMuted
)

// reportRow is one table row of a services report.
type reportRow struct {
	Name   string
	Detail string
	Status string
	Tone   reportTone
}

// reportStat is one labelled count; it is a plain-text summary line and, when
// Color is set, an HTML card.
type reportStat struct {
	Label string
	Value int
	Color string
}

// servicesReport is what both services reports render: the configured-services
// health report and the catalog inventory report.
type servicesReport struct {
	Kind          string // SERMO_REPORT
	Muted         string // what a muted row is, for the subject: "not installed", "unmonitored"
	Headline      string // HTML title
	Scope         string
	DetailHeading string // second table column
	Empty         string
	Source        string // footer: what the report is based on
	Stats         []reportStat
	Rows          []reportRow
	Fields        map[string]string // per-report counters
}

func servicesReportMessage(reports []appinspect.Report, includeMissing bool, now time.Time) notify.Message {
	stats := servicesReportSummary(reports)
	rows := make([]reportRow, 0, len(reports))
	for _, r := range reports {
		tone := reportToneOK
		if !r.Installed {
			tone = reportToneMuted
		} else if !r.OK {
			tone = reportToneIssue
		}
		rows = append(rows, reportRow{Name: r.DisplayName, Detail: reportVersion(r), Status: r.Status, Tone: tone})
	}
	return servicesReportNotification(servicesReport{
		Kind:          servicesReportKindCatalog,
		Muted:         "not installed",
		Headline:      "Service catalog health",
		Scope:         reportScope(includeMissing),
		DetailHeading: "Version",
		Empty:         "No service catalog entries matched the report scope.",
		Source:        "sermoctl services catalog",
		Stats: []reportStat{
			{Label: "Total", Value: stats.Total},
			{Label: "Installed", Value: stats.Installed, Color: servicesReportColorInfo},
			{Label: cliTextOK, Value: stats.OK, Color: servicesReportColorOK},
			{Label: "Issues", Value: stats.Issues, Color: servicesReportColorIssue},
			{Label: "Not installed", Value: stats.NotInstalled, Color: servicesReportColorMuted},
			{Label: "Versions known", Value: stats.VersionKnown},
		},
		Rows: rows,
		Fields: map[string]string{
			cliFieldSermoReportTotal:   strconv.Itoa(stats.Total),
			cliFieldSermoReportOK:      strconv.Itoa(stats.OK),
			cliFieldSermoReportIssues:  strconv.Itoa(stats.Issues),
			cliFieldSermoReportMissing: strconv.Itoa(stats.NotInstalled),
		},
	}, now)
}

// configuredServicesReportMessage reports the health of the services this host
// is configured to supervise.
func configuredServicesReportMessage(services []configuredService, now time.Time) notify.Message {
	rows := make([]reportRow, 0, len(services))
	for _, s := range services {
		rows = append(rows, reportRow{Name: s.DisplayName, Detail: s.Backend, Status: s.State, Tone: s.reportTone()})
	}
	counts := countTones(rows)
	ok, issues, unmonitored := counts[reportToneOK], counts[reportToneIssue], counts[reportToneMuted]
	return servicesReportNotification(servicesReport{
		Kind:          commandServices,
		Muted:         "unmonitored",
		Headline:      "Configured services health",
		Scope:         "configured services",
		DetailHeading: "Type",
		Empty:         "No services are configured.",
		Source:        "sermoctl services",
		Stats: []reportStat{
			{Label: "Total", Value: len(services), Color: servicesReportColorInfo},
			{Label: cliTextOK, Value: ok, Color: servicesReportColorOK},
			{Label: "Issues", Value: issues, Color: servicesReportColorIssue},
			{Label: "Unmonitored", Value: unmonitored, Color: servicesReportColorMuted},
		},
		Rows: rows,
		Fields: map[string]string{
			cliFieldSermoReportTotal:       strconv.Itoa(len(services)),
			cliFieldSermoReportOK:          strconv.Itoa(ok),
			cliFieldSermoReportIssues:      strconv.Itoa(issues),
			cliFieldSermoReportUnmonitored: strconv.Itoa(unmonitored),
		},
	}, now)
}

// toneCounts counts report rows per tone, indexed by reportTone.
type toneCounts [reportToneMuted + 1]int

func countTones(rows []reportRow) toneCounts {
	var counts toneCounts
	for _, row := range rows {
		counts[row.Tone]++
	}
	return counts
}

func servicesReportNotification(r servicesReport, now time.Time) notify.Message {
	counts := countTones(r.Rows)
	subject := fmt.Sprintf("[sermo] services report: %d ok, %d issue(s)", counts[reportToneOK], counts[reportToneIssue])
	if counts[reportToneMuted] > 0 {
		subject += fmt.Sprintf(", %d %s", counts[reportToneMuted], r.Muted)
	}
	host := reportHostname()
	fields := map[string]string{
		cliFieldSermoReport:     r.Kind,
		cliFieldSermoReportHost: host,
	}
	maps.Copy(fields, r.Fields)
	return notify.Message{
		Subject: subject,
		Body:    servicesReportText(r, host, now),
		HTML:    servicesReportHTML(r, counts, host, now),
		Fields:  fields,
	}
}

func servicesReportSummary(reports []appinspect.Report) servicesReportStats {
	var stats servicesReportStats
	stats.Total = len(reports)
	for _, r := range reports {
		if r.Installed {
			stats.Installed++
		} else {
			stats.NotInstalled++
		}
		if r.OK {
			stats.OK++
		}
		if r.Installed && !r.OK {
			stats.Issues++
		}
		if strings.TrimSpace(r.VersionShort) != "" || strings.TrimSpace(r.Version) != "" {
			stats.VersionKnown++
		}
	}
	return stats
}

func servicesReportText(r servicesReport, host string, now time.Time) string {
	var b strings.Builder
	fmt.Fprint(&b, "Sermo services report\n")
	fmt.Fprintf(&b, "Host: %s\n", host)
	fmt.Fprintf(&b, "Generated: %s\n", now.Format(time.RFC3339))
	fmt.Fprintf(&b, "Scope: %s\n\n", r.Scope)
	for _, stat := range r.Stats {
		fmt.Fprintf(&b, "%s: %d\n", stat.Label, stat.Value)
	}
	b.WriteString("\n")
	if len(r.Rows) == 0 {
		b.WriteString(r.Empty + "\n")
		return b.String()
	}
	fmt.Fprintf(&b, "SERVICE\t%s\tSTATUS\n", strings.ToUpper(r.DetailHeading))
	for _, row := range r.Rows {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", row.Name, row.Detail, row.Status)
	}
	return b.String()
}

func servicesReportHTML(r servicesReport, counts toneCounts, host string, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<!doctype html><html><body style="margin:0;padding:0;background:%s;color:%s;font-family:%s;">`,
		servicesReportColorPageBG, servicesReportColorText, servicesReportFontSans)
	fmt.Fprintf(&b, `<table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="background:%s;padding:24px 0;"><tr><td align="center">`, servicesReportColorPageBG)
	fmt.Fprintf(&b, `<table role="presentation" width="760" cellspacing="0" cellpadding="0" style="width:760px;max-width:100%%;background:%s;border:1px solid %s;border-radius:14px;overflow:hidden;">`,
		servicesReportColorPanel, servicesReportColorFrameBorder)
	fmt.Fprintf(&b, `<tr><td style="background:%s;color:%s;padding:24px 28px;">`, servicesReportColorHeaderBG, servicesReportColorPanel)
	fmt.Fprintf(&b, `<div style="font-size:13px;letter-spacing:.08em;text-transform:uppercase;color:%s;">Sermo Services Report</div>`, servicesReportColorHeaderHint)
	fmt.Fprintf(&b, `<div style="font-size:28px;font-weight:700;line-height:1.2;margin-top:6px;">%s</div>`, esc(r.Headline))
	fmt.Fprintf(&b, `<div style="font-size:13px;color:%s;margin-top:8px;">Host %s · %s · %s</div>`,
		servicesReportColorHeaderMeta, esc(host), esc(now.Format(servicesReportDateLayout)), esc(r.Scope))
	b.WriteString(`</td></tr>`)
	b.WriteString(`<tr><td style="padding:22px 28px 8px;">`)
	b.WriteString(`<table role="presentation" width="100%" cellspacing="0" cellpadding="0"><tr>`)
	for _, stat := range r.Stats {
		if stat.Color != "" {
			writeReportCard(&b, stat.Label, stat.Value, stat.Color)
		}
	}
	b.WriteString(`</tr></table>`)
	b.WriteString(`</td></tr>`)
	b.WriteString(`<tr><td style="padding:10px 28px 18px;">`)
	writeDistributionBar(&b, counts, len(r.Rows))
	b.WriteString(`</td></tr>`)
	b.WriteString(`<tr><td style="padding:0 28px 28px;">`)
	fmt.Fprintf(&b, `<table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="border-collapse:collapse;border:1px solid %s;border-radius:10px;overflow:hidden;">`, servicesReportColorBorder)
	fmt.Fprintf(&b, `<tr style="background:%s;">`, servicesReportColorTableHeadBG)
	writeReportHeaderCell(&b, "Service")
	writeReportHeaderCell(&b, r.DetailHeading)
	writeReportHeaderCell(&b, "Status")
	b.WriteString(`</tr>`)
	if len(r.Rows) == 0 {
		fmt.Fprintf(&b, `<tr><td colspan="3" style="padding:18px 12px;color:%s;">%s</td></tr>`, servicesReportColorMuted, esc(r.Empty))
	}
	for _, row := range r.Rows {
		writeReportRow(&b, row)
	}
	b.WriteString(`</table>`)
	b.WriteString(`</td></tr>`)
	fmt.Fprintf(&b, `<tr><td style="padding:16px 28px;background:%s;border-top:1px solid %s;color:%s;font-size:12px;">Generated by <strong>Sermo</strong>. This report is based on <code style="font-family:%s;">%s</code>.</td></tr>`,
		servicesReportColorTableHeadBG, servicesReportColorBorder, servicesReportColorMuted, servicesReportFontMono, esc(r.Source))
	b.WriteString(`</table></td></tr></table></body></html>`)
	return b.String()
}

func writeReportHeaderCell(b *strings.Builder, label string) {
	fmt.Fprintf(b, `<th align="left" style="padding:11px 12px;font-size:12px;color:%s;text-transform:uppercase;letter-spacing:.05em;border-bottom:1px solid %s;">%s</th>`,
		servicesReportColorSecondary, servicesReportColorBorder, esc(label))
}

func writeReportCard(b *strings.Builder, label string, value int, color string) {
	fmt.Fprintf(b, `<td style="width:25%%;padding:0 7px 12px 0;"><div style="border:1px solid %s;border-radius:12px;padding:14px;background:%s;"><div style="font-size:12px;text-transform:uppercase;letter-spacing:.05em;color:%s;">%s</div><div style="font-size:28px;font-weight:700;color:%s;margin-top:4px;">%d</div></div></td>`,
		servicesReportColorBorder, servicesReportColorPanel, servicesReportColorMuted, esc(label), color, value)
}

func servicesReportDistributionSegment(pct float64, color string) string {
	return fmt.Sprintf(`<span style="display:inline-block;height:12px;width:%.2f%%;background:%s;"></span>`, pct, color)
}

func writeDistributionBar(b *strings.Builder, counts toneCounts, rows int) {
	total := float64(max(rows, 1))
	pct := func(tone reportTone) float64 { return float64(counts[tone]) / total * metrics.PercentScale }
	fmt.Fprintf(b, `<div style="font-size:12px;text-transform:uppercase;letter-spacing:.05em;color:%s;margin-bottom:8px;">Distribution</div>`, servicesReportColorMuted)
	fmt.Fprintf(b, `<div style="height:12px;border-radius:999px;overflow:hidden;background:%s;">%s%s%s</div>`,
		servicesReportColorBorder,
		servicesReportDistributionSegment(pct(reportToneOK), servicesReportColorOK),
		servicesReportDistributionSegment(pct(reportToneIssue), servicesReportColorIssue),
		servicesReportDistributionSegment(pct(reportToneMuted), servicesReportColorMuted))
}

func writeReportRow(b *strings.Builder, r reportRow) {
	b.WriteString(`<tr>`)
	fmt.Fprintf(b, `<td style="padding:10px 12px;border-bottom:1px solid %s;font-size:14px;color:%s;font-weight:600;">%s</td>`,
		servicesReportColorBorder, servicesReportColorText, esc(r.Name))
	fmt.Fprintf(b, `<td style="padding:10px 12px;border-bottom:1px solid %s;font-size:13px;color:%s;font-family:%s;">%s</td>`,
		servicesReportColorBorder, servicesReportColorSecondary, servicesReportFontMono, esc(r.Detail))
	fmt.Fprintf(b, `<td style="padding:10px 12px;border-bottom:1px solid %s;font-size:13px;">%s</td>`, servicesReportColorBorder, statusBadge(r))
	b.WriteString(`</tr>`)
}

func statusBadge(r reportRow) string {
	var color, bg string
	switch r.Tone {
	case reportToneOK:
		color, bg = servicesReportColorOK, servicesReportColorOKBadgeBG
	case reportToneMuted:
		color, bg = servicesReportColorMuted, servicesReportColorMuteBadgeBG
	case reportToneIssue:
		color, bg = servicesReportColorIssue, servicesReportColorBadBadgeBG
	}
	return fmt.Sprintf(`<span style="display:inline-block;border-radius:999px;padding:4px 9px;background:%s;color:%s;font-weight:700;">%s</span>`, bg, color, esc(r.Status))
}

func reportVersion(r appinspect.Report) string {
	if r.VersionShort != "" {
		return r.VersionShort
	}
	if r.Version != "" {
		return r.Version
	}
	return "-"
}

func reportScope(includeMissing bool) string {
	if includeMissing {
		return "installed and not-installed catalog services"
	}
	return "installed catalog services"
}

func reportHostname() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "unknown-host"
	}
	return host
}

func splitFlagList(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
}

func esc(s string) string {
	return html.EscapeString(s)
}
