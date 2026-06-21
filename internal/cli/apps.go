// Package cli implements the sermoctl command-line interface.
package cli

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"sermo/internal/app"
	"sermo/internal/appinspect"
	"sermo/internal/config"
)

// runApps lists catalog apps (catalog/apps): which are
// installed (their binary is present and executable), the version their version
// command reports, and whether they resolve without error. Only installed apps
// are shown unless `apps all` is given.
func (a App) runApps(ctx context.Context, opts options) int {
	return a.listCategory(ctx, opts, config.CategoryApp, "apps", "installed applications", "APPLICATION")
}

// runLibs lists catalog libraries (catalog/libs) services can watch for
// changes, with the version each reports and whether it is present.
func (a App) runLibs(ctx context.Context, opts options) int {
	return a.listCategory(ctx, opts, config.CategoryLibrary, "libs", "libraries", "LIBRARY")
}

// runServices lists catalog service profiles (catalog/services): which
// are installed, the version their version command reports, and whether they
// resolve without error.
func (a App) runServices(ctx context.Context, opts options) int {
	return a.listCategory(ctx, opts, config.CategoryService, "services", "installed services", "SERVICE")
}

func (a App) listCategory(ctx context.Context, opts options, category, jsonKey, empty, heading string) int {
	if len(opts.args) > 1 || (len(opts.args) == 1 && opts.args[0] != "all") {
		return a.commandUsageError(jsonKey, fmt.Sprintf("%s accepts only optional `all`", jsonKey))
	}
	if len(opts.notifyNames) > 0 && category != config.CategoryService {
		return a.commandUsageError(jsonKey, "--notify is only supported by services")
	}
	includeMissing := len(opts.args) > 0 && opts.args[0] == "all"

	cfg, code := a.loadConfig(opts)
	if code != exitSuccess {
		return code
	}

	inspectOpts := []appinspect.Option{appinspect.WithUserLookup(app.EngineUserLookup(cfg, a.Runner))}
	if category == config.CategoryService {
		inspectOpts = append(inspectOpts, appinspect.WithOptionalVersion())
	}
	reports := appinspect.List(ctx, a.Runner, cfg, category, includeMissing, inspectOpts...)

	var notified []string
	if category == config.CategoryService && len(opts.notifyNames) > 0 {
		var code int
		notified, code = a.sendServicesReport(ctx, opts, cfg, reports, includeMissing)
		if code != exitSuccess {
			return code
		}
	}
	if opts.json {
		out := map[string]any{jsonKey: reports}
		if notified != nil {
			out["notified"] = notified
		}
		writeJSON(a.Stdout, out)
		return exitSuccess
	}
	a.printApps(reports, empty, opts.long, heading)
	if notified != nil && !opts.quiet {
		fmt.Fprintf(a.Stdout, "sent services report to %s\n", strings.Join(notified, ", "))
	}
	return exitSuccess
}

// printApps renders the report table. The VERSION column shows the short version
// by default; with long set it shows the full raw version string instead.
func (a App) printApps(reports []appinspect.Report, empty string, long bool, heading string) {
	if len(reports) == 0 {
		fmt.Fprintf(a.Stdout, "no %s\n", empty)
		return
	}
	tw := tabwriter.NewWriter(a.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\tVERSION\tSTATUS\n", heading)
	for _, r := range reports {
		version := r.VersionShort
		if long || version == "" {
			// Full string on request, and as a fallback when no short
			// version could be derived, so the column is never blanker
			// than it needs to be.
			version = r.Version
		}
		if version == "" {
			version = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", r.DisplayName, version, r.Status)
	}
	_ = tw.Flush()
}
