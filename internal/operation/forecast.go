package operation

import (
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// Forecast is the result an action on an active service would record, judged
// from the monitor's latest cycle instead of a fresh observation. It neither
// authorizes nor blocks anything: every operation observes afresh.
type Forecast struct {
	Action  string
	Status  ResultStatus
	Message string
}

// ForecastActive reports, for an active service with the given process
// identity, each lifecycle action the engine would refuse or that would fail
// its health check, in plan order. It follows run's phase order:
//   - repair refuses an active service before anything else (prepareRepair);
//   - a reconciling plan (restart, start) meets identityRefusal first;
//   - a plan that keeps the running generation (start, reload, resume) then
//     verifies it with healthIdentityError, which needs the processes the
//     cycle found, once its backend supports the action;
//   - stop and pause never reach either rule.
func (e Engine) ForecastActive(status servicemgr.Status, p process.PrincipalIdentity) []Forecast {
	if status != servicemgr.StatusActive {
		return nil
	}
	refusal := e.identityRefusal(p)
	var health error
	if p.Attributed {
		health = e.healthIdentityError(p)
	}
	var out []Forecast
	for _, plan := range lifecyclePlans {
		switch {
		case plan.repair || plan.reload && e.ReloadFunc == nil || plan.resume && e.ResumeFunc == nil:
			continue
		case plan.reconcile && refusal != nil:
			out = append(out, Forecast{Action: plan.action, Status: reconciliationStatus(refusal), Message: refusal.Error()})
		case !plan.stop && (plan.start || plan.reload || plan.resume) && health != nil:
			out = append(out, Forecast{Action: plan.action, Status: ResultFailed, Message: health.Error()})
		}
	}
	return out
}
