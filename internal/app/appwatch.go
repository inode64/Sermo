package app

import (
	"context"

	"sermo/internal/appinspect"
	"sermo/internal/config"
	"sermo/internal/execx"
)

func storeAppSample(samples *ArtifactSamples, name string, report appinspect.Report) {
	if samples == nil {
		return
	}
	samples.StoreAppVersion(name, report.Version, report.Status)
}

// BuildAppWatches builds one app-watch per installed catalog application. Each
// reuses the whole Watch cycle: every engine.artifact_interval it inspects its app,
// and because FireOnFail is set it "fires" when the app is not ok — emitting a
// firing/recovered event on the App dimension and notifying the global default
// once on the rising edge (NotifyInterval 0 = first time only). Only installed
// apps are watched, matching the web Applications list.
func BuildAppWatches(ctx context.Context, cfg *config.Config, deps Deps) []*Watch {
	return buildCatalogArtifactWatches(ctx, cfg, deps, catalogArtifactWatchSpec{
		category:  config.CategoryApp,
		watchName: func(name string) string { return name },
		appName:   func(name string) string { return name },
		register: func(samples *ArtifactSamples, report appinspect.Report) {
			samples.RegisterApp(report.Name)
		},
		store: storeAppSample,
		inspect: func(ctx context.Context, runner execx.Runner, cfg *config.Config, name string, lookup appinspect.Option) appinspect.Report {
			return appinspect.InspectOne(ctx, runner, cfg, name, lookup)
		},
	})
}
