package app

import (
	"context"
	"sermo/internal/appinspect"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/web"
	"strings"
)

// Applications returns the installed applications (catalog app daemons whose
// binary is present) with their version and binary location, reusing the same
// inspection the sermoctl `apps` listing uses so both surfaces agree.
func (b *WebBackend) Applications(ctx context.Context) []web.CatalogItem {
	return b.decorateApplications(b.catalogItems(ctx, &b.applications, b.loadApplications))
}

// Libraries returns installed catalog libraries with their version and file
// location, reusing the same inspection as sermoctl libs.
func (b *WebBackend) Libraries(ctx context.Context) []web.CatalogItem {
	return b.catalogItems(ctx, &b.libraries, b.loadLibraries)
}

func (b *WebBackend) loadApplications(ctx context.Context) []web.CatalogItem {
	return b.withApplicationSLA(b.loadCatalogItems(ctx, config.CategoryApp, true))
}

func (b *WebBackend) loadLibraries(ctx context.Context) []web.CatalogItem {
	return b.loadCatalogItems(ctx, config.CategoryLibrary, false)
}

func (b *WebBackend) loadCatalogItems(ctx context.Context, category string, exposeSettling bool) []web.CatalogItem {
	if b.cfg == nil {
		return nil
	}
	names := b.cfg.CatalogNamesInCategory(category)
	if len(names) == 0 {
		return nil
	}
	runner := b.execRunner
	runner = execx.RunnerOrDefault(runner)
	opts := appinspect.WithUserLookup(b.userLookup)
	type catalogResult struct {
		item web.CatalogItem
		ok   bool
	}
	results := make([]catalogResult, len(names))
	probeNames := make([]string, 0, len(names))
	probeIndices := make([]int, 0, len(names))
	for i, name := range names {
		if exposeSettling && b.settling != nil && !b.settling.Observed(SettlingAppKey(name)) {
			resolved, _ := b.cfg.ResolveCatalog(category, name)
			results[i] = catalogResult{ok: true, item: web.CatalogItem{
				Name:        name,
				DisplayName: config.DisplayName(resolved.Tree, name),
				Category:    config.CategoryLabel(resolved.Tree, category),
				State:       TargetStateStarting,
			}}
			continue
		}
		if report, ok := b.sampledAppReport(category, name); ok {
			if report.Installed {
				results[i] = catalogResult{item: catalogItemFromReport(report), ok: true}
			}
			continue
		}
		probeNames = append(probeNames, name)
		probeIndices = append(probeIndices, i)
	}
	for i, report := range appinspect.InspectCategory(ctx, runner, b.cfg, category, probeNames, catalogInspectionParallelism, opts) {
		if report.Installed {
			results[probeIndices[i]] = catalogResult{item: catalogItemFromReport(report), ok: true}
		}
	}
	out := make([]web.CatalogItem, 0, len(names))
	for i := range results {
		if results[i].ok {
			out = append(out, results[i].item)
		}
	}
	return out
}

// sampledAppReport returns the app watch's latest inspection of an app. Each
// installed app is already inspected by its own watch every artifact interval;
// re-running every version probe for the listing doubled the probe load and
// kept a viewer waiting on the slowest one. Apps without a watch sample (not
// installed at startup, or no watch yet) are still inspected directly.
func (b *WebBackend) sampledAppReport(category, name string) (appinspect.Report, bool) {
	if category != config.CategoryApp {
		return appinspect.Report{}, false
	}
	return b.artifactSamples.AppReport(name)
}

func catalogItemFromReport(r appinspect.Report) web.CatalogItem {
	return web.CatalogItem{
		Name:          r.Name,
		DisplayName:   r.DisplayName,
		Category:      r.Category,
		Binary:        r.Binary,
		Permissions:   r.Permissions,
		User:          r.User,
		Group:         r.Group,
		Version:       r.Version,
		VersionShort:  r.VersionShort,
		VersionSource: r.VersionSource,
		Status:        r.Status,
		State:         applicationStateFromReport(r),
	}
}

func applicationStateFromReport(r appinspect.Report) string {
	status := strings.TrimSpace(strings.ToLower(r.Status))
	if status == "" || status == appinspect.StatusOK || r.OK {
		return TargetStateOK
	}
	if status == appinspect.StatusNotInstalled || status == appinspect.StatusNoBinaryConfigured || strings.HasPrefix(status, appinspect.StatusPrefixError) {
		return TargetStateFailed
	}
	return TargetStateWarning
}

// withApplicationSLA marks each application in an owned slice that maps to a monitored service.
// Its availability is that service's, so the dashboard draws it with the
// service's own SLA panel and fetches it from the service's own endpoint; the
// flag says only that there is one to fetch.
func (b *WebBackend) withApplicationSLA(apps []web.CatalogItem) []web.CatalogItem {
	if len(apps) == 0 {
		return apps
	}
	for i := range apps {
		apps[i].KeepsSLA = b.entries[apps[i].Name] != nil
	}
	return apps
}

func (b *WebBackend) decorateApplications(apps []web.CatalogItem) []web.CatalogItem {
	if b.events == nil {
		return apps
	}
	for i := range apps {
		ev, ok := b.events.LastApp(apps[i].Name)
		if !ok {
			continue
		}
		webEv := loggedEventToWeb(ev)
		apps[i].LastEvent = &webEv
	}
	return apps
}
