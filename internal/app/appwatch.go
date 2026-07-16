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
	samples.StoreAppVersion(name, report.Version, report.Status)
}

const appWatchCheckType = config.CategoryApp

// BuildAppWatches builds one app-watch per installed catalog application. Each
// reuses the whole Watch cycle: every engine.artifact_interval it inspects its app,
// and because FireOnFail is set it "fires" when the app is not ok — emitting a
// firing/recovered event on the App dimension and notifying the global default
// once on the rising edge (NotifyInterval 0 = first time only). Only installed
// apps are watched, matching the web Applications list.
func BuildAppWatches(ctx context.Context, cfg *config.Config, deps Deps) []*Watch {
	if cfg == nil {
		return nil
	}
	samples := deps.ArtifactSamples
	if samples == nil {
		samples = NewArtifactSamples()
	}
	runner := deps.ExecxRunner
	reports := appinspect.List(ctx, runner, cfg, config.CategoryApp, false,
		appinspect.WithUserLookup(deps.UserLookup))
	if len(reports) == 0 {
		return nil
	}
	notifiers := resolveNotifiers(deps.GlobalNotify, deps.Notifiers)
	out := make([]*Watch, 0, len(reports))
	for i := range reports {
		name := reports[i].Name
		samples.RegisterApp(name)
		check := artifactCheck{
			name:    name,
			samples: samples,
			store:   storeAppSample,
			inspect: func(ctx context.Context) appinspect.Report {
				return appinspect.InspectOne(ctx, runner, cfg, name,
					appinspect.WithUserLookup(deps.UserLookup))
			},
		}
		out = append(out, &Watch{
			Name:       name,
			App:        name,
			CheckType:  appWatchCheckType,
			Check:      check,
			FireOnFail: true,
			Interval:   artifactWatchInterval(cfg, config.CategoryApp, name),
			Notifiers:  notifiers,
			Settling:   deps.Settling,
			Now:        deps.Now,
			Emit:       deps.Emit,
			StateStore: deps.WatchState,
		})
	}
	return out
}
