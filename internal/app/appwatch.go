package app

import (
	"context"

	"sermo/internal/appinspect"
	"sermo/internal/config"
)

func storeAppSample(samples *ArtifactSamples, name string, report appinspect.Report) {
	if samples == nil {
		return
	}
	samples.StoreAppReport(name, report)
}

// buildAppWatches builds one app-watch per installed catalog application and
// returns the app names they sample. Each reuses the whole Watch cycle: every
// engine.artifact_interval it inspects its app, and because FireOnFail is set it
// "fires" when the app is not ok — emitting a firing/recovered event on the App
// dimension and notifying the global default once on the rising edge
// (NotifyInterval 0 = first time only). Only installed apps are watched,
// matching the web Applications list. appUsers names each application's
// enabled services: a broken application an active one runs is an error, any
// other a warning (nil grades by the watch alone).
func buildAppWatches(ctx context.Context, cfg *config.Config, deps Deps, appUsers map[string][]string) ([]*Watch, artifactOwned) {
	var activeUsers func(string) []string
	if appUsers != nil {
		activeUsers = appActiveUsers(appUsers, deps.Snapshots)
	}
	return buildCatalogArtifactWatches(ctx, cfg, deps, catalogArtifactWatchSpec{
		category:  config.CategoryApp,
		watchName: func(name string) string { return name },
		appName:   func(name string) string { return name },
		register: func(samples *ArtifactSamples, report appinspect.Report) string {
			samples.RegisterApp(report.Name)
			return report.Name
		},
		store:       storeAppSample,
		inspect:     appinspect.InspectOne,
		activeUsers: activeUsers,
	})
}
