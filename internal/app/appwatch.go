package app

import (
	"context"
	"time"

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
}

func (c appCheck) Name() string { return c.name }

func (c appCheck) Run(ctx context.Context) checks.Result {
	rep := c.inspect(ctx)
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

const appWatchCheckType = config.CategoryApp

// appWatchInterval is the cadence at which installed apps are inspected for
// errors (engine.app_interval, default 5m). Apps change rarely and each check
// runs the app's version/health binary, so the default is slow.
func appWatchInterval(cfg *config.Config) time.Duration {
	return EngineDuration(cfg, config.EngineKeyAppInterval, DefaultEngineAppInterval)
}

// BuildAppWatches builds one app-watch per installed catalog application. Each
// reuses the whole Watch cycle: every engine.app_interval it inspects its app,
// and because FireOnFail is set it "fires" when the app is not ok — emitting a
// firing/recovered event on the App dimension and notifying the global default
// once on the rising edge (NotifyInterval 0 = first time only). Only installed
// apps are watched, matching the web Applications list.
func BuildAppWatches(cfg *config.Config, deps Deps) []*Watch {
	if cfg == nil {
		return nil
	}
	interval := appWatchInterval(cfg)
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
		check := appCheck{
			name: name,
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
			Interval:   interval,
			Notifiers:  notifiers,
			Settling:   deps.Settling,
			Now:        deps.Now,
			Emit:       deps.Emit,
		})
	}
	return out
}
