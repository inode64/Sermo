package app

import (
	"context"
	"errors"

	"sermo/internal/appinspect"
	"sermo/internal/checks"
	"sermo/internal/config"
)

// appCheck adapts a single application's inspection to a checks.Check: OK when
// the app reports appinspect.StatusOK, otherwise the Result message carries the
// error detail appinspect captured (e.g. "error: exit 1 (want 0): <stderr>").
type appCheck struct {
	name    string
	inspect func(context.Context) appinspect.Report
	samples *ArtifactSamples
}

func (c appCheck) Name() string { return c.name }

func (c appCheck) Run(ctx context.Context) checks.Result {
	rep := c.inspect(ctx)
	storeAppSample(c.samples, c.name, rep)
	res := checks.Result{
		Check:   c.name,
		OK:      rep.Status == appinspect.StatusOK,
		Message: rep.Status,
	}
	if !res.OK && rep.Output != "" {
		res.Data = map[string]any{checks.DataKeyOutput: rep.Output}
	}
	return res
}

func storeAppSample(samples *ArtifactSamples, name string, report appinspect.Report) {
	if samples == nil {
		return
	}
	var err error
	if report.Status != appinspect.StatusOK {
		err = errors.New(report.Status)
	}
	samples.StoreAppVersion(name, report.Version, err)
}

const appWatchCheckType = config.CategoryApp

// BuildAppWatches builds one app-watch per installed catalog application. Each
// reuses the whole Watch cycle: every engine.artifact_interval it inspects its app,
// and because FireOnFail is set it "fires" when the app is not ok — emitting a
// firing/recovered event on the App dimension and notifying the global default
// once on the rising edge (NotifyInterval 0 = first time only). Only installed
// apps are watched, matching the web Applications list.
func BuildAppWatches(cfg *config.Config, deps Deps) []*Watch {
	if cfg == nil {
		return nil
	}
	samples := deps.ArtifactSamples
	if samples == nil {
		samples = NewArtifactSamples()
	}
	runner := deps.ExecxRunner
	reports := appinspect.List(context.Background(), runner, cfg, config.CategoryApp, false,
		appinspect.WithUserLookup(deps.UserLookup))
	if len(reports) == 0 {
		return nil
	}
	notifiers := resolveNotifiers(deps.GlobalNotify, deps.Notifiers)
	out := make([]*Watch, 0, len(reports))
	for _, r := range reports {
		name := r.Name
		samples.RegisterApp(name)
		check := appCheck{
			name:    name,
			samples: samples,
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
