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

func buildReportNotifiers(cfg *config.Config) (map[string]notify.Notifier, []string) {
	return notify.Build(cfg.Notifiers(), notify.WithoutTemplates())
}

func (a App) sendServicesReport(ctx context.Context, opts options, cfg *config.Config, reports []appinspect.Report, includeMissing bool) ([]string, int) {
	registry, warnings := a.BuildNotifiers(cfg)
	for _, warning := range warnings {
		fmt.Fprintf(a.Stderr, "warning: %s\n", warning)
	}
	selected, names, err := selectServicesReportNotifiers(opts.notifyNames, registry)
	if err != nil {
		return nil, a.commandUsageError("services", err.Error())
	}
	if len(selected) == 0 {
		return nil, a.fail(opts, "services --notify selected no enabled notifiers")
	}

	msg := servicesReportMessage(reports, includeMissing, time.Now())
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
	names := selection
	if slices.Contains(selection, "all") {
		names = slices.Sorted(maps.Keys(registry))
	}
	seen := map[string]struct{}{}
	selected := make([]notify.Notifier, 0, len(names))
	outNames := make([]string, 0, len(names))
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
		outNames = append(outNames, name)
	}
	return selected, outNames, nil
}

func servicesReportMessage(reports []appinspect.Report, includeMissing bool, now time.Time) notify.Message {
	stats := servicesReportSummary(reports)
	host := reportHostname()
	subject := fmt.Sprintf("[sermo] services report: %d ok, %d issue(s)", stats.OK, stats.Issues)
	if stats.NotInstalled > 0 {
		subject += fmt.Sprintf(", %d not installed", stats.NotInstalled)
	}
	body := servicesReportText(reports, stats, includeMissing, host, now)
	return notify.Message{
		Subject: subject,
		Body:    body,
		HTML:    servicesReportHTML(reports, stats, includeMissing, host, now),
		Fields: map[string]string{
			"SERMO_REPORT":         "services",
			"SERMO_REPORT_HOST":    host,
			"SERMO_REPORT_TOTAL":   strconv.Itoa(stats.Total),
			"SERMO_REPORT_OK":      strconv.Itoa(stats.OK),
			"SERMO_REPORT_ISSUES":  strconv.Itoa(stats.Issues),
			"SERMO_REPORT_MISSING": strconv.Itoa(stats.NotInstalled),
		},
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

func servicesReportText(reports []appinspect.Report, stats servicesReportStats, includeMissing bool, host string, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Sermo services report\n")
	fmt.Fprintf(&b, "Host: %s\n", host)
	fmt.Fprintf(&b, "Generated: %s\n", now.Format(time.RFC3339))
	fmt.Fprintf(&b, "Scope: %s\n\n", reportScope(includeMissing))
	fmt.Fprintf(&b, "Total: %d\nInstalled: %d\nOK: %d\nIssues: %d\nNot installed: %d\nVersions known: %d\n\n",
		stats.Total, stats.Installed, stats.OK, stats.Issues, stats.NotInstalled, stats.VersionKnown)
	if len(reports) == 0 {
		b.WriteString("No service catalog entries matched the report scope.\n")
		return b.String()
	}
	b.WriteString("SERVICE\tVERSION\tSTATUS\n")
	for _, r := range reports {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", r.DisplayName, reportVersion(r), r.Status)
	}
	return b.String()
}

func servicesReportHTML(reports []appinspect.Report, stats servicesReportStats, includeMissing bool, host string, now time.Time) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><body style="margin:0;padding:0;background:#f4f6fb;color:#182230;font-family:Arial,Helvetica,sans-serif;">`)
	b.WriteString(`<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="background:#f4f6fb;padding:24px 0;"><tr><td align="center">`)
	b.WriteString(`<table role="presentation" width="760" cellspacing="0" cellpadding="0" style="width:760px;max-width:100%;background:#ffffff;border:1px solid #dfe5ef;border-radius:14px;overflow:hidden;">`)
	b.WriteString(`<tr><td style="background:#0f172a;color:#ffffff;padding:24px 28px;">`)
	b.WriteString(`<div style="font-size:13px;letter-spacing:.08em;text-transform:uppercase;color:#93c5fd;">Sermo Services Report</div>`)
	b.WriteString(`<div style="font-size:28px;font-weight:700;line-height:1.2;margin-top:6px;">Service catalog health</div>`)
	fmt.Fprintf(&b, `<div style="font-size:13px;color:#cbd5e1;margin-top:8px;">Host %s · %s · %s</div>`, esc(host), esc(now.Format("2006-01-02 15:04 MST")), esc(reportScope(includeMissing)))
	b.WriteString(`</td></tr>`)
	b.WriteString(`<tr><td style="padding:22px 28px 8px;">`)
	b.WriteString(`<table role="presentation" width="100%" cellspacing="0" cellpadding="0"><tr>`)
	writeReportCard(&b, "OK", stats.OK, "#16a34a")
	writeReportCard(&b, "Issues", stats.Issues, "#dc2626")
	writeReportCard(&b, "Installed", stats.Installed, "#2563eb")
	writeReportCard(&b, "Not installed", stats.NotInstalled, "#64748b")
	b.WriteString(`</tr></table>`)
	b.WriteString(`</td></tr>`)
	b.WriteString(`<tr><td style="padding:10px 28px 18px;">`)
	writeDistributionBar(&b, stats)
	b.WriteString(`</td></tr>`)
	b.WriteString(`<tr><td style="padding:0 28px 28px;">`)
	b.WriteString(`<table role="presentation" width="100%" cellspacing="0" cellpadding="0" style="border-collapse:collapse;border:1px solid #e2e8f0;border-radius:10px;overflow:hidden;">`)
	b.WriteString(`<tr style="background:#f8fafc;">`)
	b.WriteString(`<th align="left" style="padding:11px 12px;font-size:12px;color:#475569;text-transform:uppercase;letter-spacing:.05em;border-bottom:1px solid #e2e8f0;">Service</th>`)
	b.WriteString(`<th align="left" style="padding:11px 12px;font-size:12px;color:#475569;text-transform:uppercase;letter-spacing:.05em;border-bottom:1px solid #e2e8f0;">Version</th>`)
	b.WriteString(`<th align="left" style="padding:11px 12px;font-size:12px;color:#475569;text-transform:uppercase;letter-spacing:.05em;border-bottom:1px solid #e2e8f0;">Status</th>`)
	b.WriteString(`</tr>`)
	if len(reports) == 0 {
		b.WriteString(`<tr><td colspan="3" style="padding:18px 12px;color:#64748b;">No service catalog entries matched the report scope.</td></tr>`)
	}
	for _, r := range reports {
		writeReportRow(&b, r)
	}
	b.WriteString(`</table>`)
	b.WriteString(`</td></tr>`)
	b.WriteString(`<tr><td style="padding:16px 28px;background:#f8fafc;border-top:1px solid #e2e8f0;color:#64748b;font-size:12px;">Generated by <strong>Sermo</strong>. This report is based on <code style="font-family:Menlo,Consolas,monospace;">sermoctl services</code> catalog probes.</td></tr>`)
	b.WriteString(`</table></td></tr></table></body></html>`)
	return b.String()
}

func writeReportCard(b *strings.Builder, label string, value int, color string) {
	fmt.Fprintf(b, `<td style="width:25%%;padding:0 7px 12px 0;"><div style="border:1px solid #e2e8f0;border-radius:12px;padding:14px;background:#ffffff;"><div style="font-size:12px;text-transform:uppercase;letter-spacing:.05em;color:#64748b;">%s</div><div style="font-size:28px;font-weight:700;color:%s;margin-top:4px;">%d</div></div></td>`, esc(label), color, value)
}

func writeDistributionBar(b *strings.Builder, stats servicesReportStats) {
	total := max(stats.Total, 1)
	okPct := float64(stats.OK) / float64(total) * 100
	issuePct := float64(stats.Issues) / float64(total) * 100
	missingPct := float64(stats.NotInstalled) / float64(total) * 100
	b.WriteString(`<div style="font-size:12px;text-transform:uppercase;letter-spacing:.05em;color:#64748b;margin-bottom:8px;">Distribution</div>`)
	fmt.Fprintf(b, `<div style="height:12px;border-radius:999px;overflow:hidden;background:#e2e8f0;"><span style="display:inline-block;height:12px;width:%.2f%%;background:#16a34a;"></span><span style="display:inline-block;height:12px;width:%.2f%%;background:#dc2626;"></span><span style="display:inline-block;height:12px;width:%.2f%%;background:#64748b;"></span></div>`, okPct, issuePct, missingPct)
}

func writeReportRow(b *strings.Builder, r appinspect.Report) {
	b.WriteString(`<tr>`)
	fmt.Fprintf(b, `<td style="padding:10px 12px;border-bottom:1px solid #e2e8f0;font-size:14px;color:#182230;font-weight:600;">%s</td>`, esc(r.DisplayName))
	fmt.Fprintf(b, `<td style="padding:10px 12px;border-bottom:1px solid #e2e8f0;font-size:13px;color:#475569;font-family:Menlo,Consolas,monospace;">%s</td>`, esc(reportVersion(r)))
	fmt.Fprintf(b, `<td style="padding:10px 12px;border-bottom:1px solid #e2e8f0;font-size:13px;">%s</td>`, statusBadge(r))
	b.WriteString(`</tr>`)
}

func statusBadge(r appinspect.Report) string {
	color := "#16a34a"
	bg := "#dcfce7"
	label := r.Status
	if !r.Installed {
		color, bg = "#64748b", "#f1f5f9"
	} else if !r.OK {
		color, bg = "#dc2626", "#fee2e2"
	}
	return fmt.Sprintf(`<span style="display:inline-block;border-radius:999px;padding:4px 9px;background:%s;color:%s;font-weight:700;">%s</span>`, bg, color, esc(label))
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
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func esc(s string) string {
	return html.EscapeString(s)
}
