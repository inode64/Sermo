package app

import (
	"context"
	"sermo/internal/appinspect"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/web"
	"slices"
	"strings"
	"sync"
	"time"
)

// Applications returns the installed applications (catalog app daemons whose
// binary is present) with their version and binary location, reusing the same
// inspection the sermoctl `apps` listing uses so both surfaces agree.
func (b *WebBackend) Applications(ctx context.Context) []web.Application {
	return b.decorateApplications(b.catalogItems(ctx, &b.applications, b.loadApplications))
}

// Libraries returns installed catalog libraries with their version and file
// location, reusing the same inspection as sermoctl libs.
func (b *WebBackend) Libraries(ctx context.Context) []web.Library {
	return b.catalogItems(ctx, &b.libraries, b.loadLibraries)
}

func (b *WebBackend) loadApplications(ctx context.Context) []web.CatalogItem {
	if b.applications.list != nil {
		return b.withApplicationSLA(b.applications.list(ctx))
	}
	return b.withApplicationSLA(b.loadCatalogItems(ctx, config.CategoryApp, true))
}

func (b *WebBackend) loadLibraries(ctx context.Context) []web.CatalogItem {
	if b.libraries.list != nil {
		return b.libraries.list(ctx)
	}
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
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	opts := appinspect.WithUserLookup(b.userLookup)
	type catalogResult struct {
		item web.CatalogItem
		ok   bool
	}
	results := make([]catalogResult, len(names))
	sem := make(chan struct{}, catalogInspectionParallelism)
	var wg sync.WaitGroup
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
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			r := appinspect.InspectCategoryOne(ctx, runner, b.cfg, category, name, opts)
			if r.Installed {
				results[i] = catalogResult{item: catalogItemFromReport(r), ok: true}
			}
		})
	}
	wg.Wait()
	out := make([]web.CatalogItem, 0, len(names))
	for i := range results {
		if results[i].ok {
			out = append(out, results[i].item)
		}
	}
	return out
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

func (b *WebBackend) withApplicationSLA(apps []web.Application) []web.Application {
	if len(apps) == 0 {
		return apps
	}
	out := slices.Clone(apps)
	now := b.webNow()
	for i := range out {
		if b.entries[out[i].Name] != nil {
			out[i].SLA = b.serviceSLAWindows(out[i].Name, now)
		}
	}
	return out
}

func decorateCatalogItems(items []web.CatalogItem, observedAt time.Time) []web.CatalogItem {
	if len(items) == 0 || observedAt.IsZero() {
		return items
	}
	out := slices.Clone(items)
	for i := range out {
		out[i].ObservedAt = observedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func (b *WebBackend) decorateApplications(apps []web.Application) []web.Application {
	if len(apps) == 0 {
		return apps
	}
	out := slices.Clone(apps)
	for i := range out {
		if b.events == nil {
			continue
		}
		ev, ok := b.events.LastApp(out[i].Name)
		if !ok {
			continue
		}
		webEv := loggedEventToWeb(ev)
		out[i].LastEvent = &webEv
	}
	return out
}
